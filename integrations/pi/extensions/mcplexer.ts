// mcplexer.ts — a Pi (pi.dev / Earendil) extension that bridges the agent to
// the local mcplexer gateway's *slim 4-tool surface* without dumping any
// downstream tool definitions into the context window.
//
// Design (matches Pi's "primitives over features" / MCP-skeptical ethos):
// instead of registering hundreds of MCP tools, this extension registers FOUR
// thin tools that mirror mcplexer's static surface. Each one shells out to the
// `mcpx-shim` CLI, which performs a single MCP tools/call against the daemon
// over its Unix socket. The agent discovers everything else (task, mesh,
// memory, skill, downstream MCP servers) at runtime via `mcpx_search`, then
// calls it inside a JS snippet through `mcpx_exec`. Read the bundled skill
// (`/skill:mcplexer` or skills/mcplexer/SKILL.md) for the full playbook.
//
// Token budget: ~4 small tool defs (~600 tokens) replace the 10k+/server an
// MCP integration would otherwise burn.

import { spawn } from "node:child_process";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { Type } from "@earendil-works/pi-ai";
import { complete, type UserMessage } from "@earendil-works/pi-ai/compat";
import type { ExtensionAPI, ExtensionContext } from "@earendil-works/pi-coding-agent";
import {
  formatBusyResponse,
  route,
  getRouterState,
  setRouterEnabled,
  formatRouteResult,
  reviewDelegation,
} from "./router/index.ts";
import type { InputMeta } from "./router/index.ts";

const here = dirname(fileURLToPath(import.meta.url));
// extensions/ and bin/ are siblings inside the package.
const SHIM = join(here, "..", "bin", "mcpx-shim.mjs");

interface ShimResult {
  ok: boolean;
  text: string;
}

let activeRouterController: AbortController | null = null;

function classifierModelRef(): { provider: string; modelId: string } {
  const ref = process.env.MCPLEXER_ROUTER_CLASSIFIER_MODEL ?? "local/qwen3.6-35b-a3b";
  const slash = ref.indexOf("/");
  if (slash <= 0 || slash === ref.length - 1) {
    throw new Error("MCPLEXER_ROUTER_CLASSIFIER_MODEL must be provider/model-id");
  }
  return { provider: ref.slice(0, slash), modelId: ref.slice(slash + 1) };
}

async function classifyWithLocalModel(ctx: ExtensionContext, prompt: string): Promise<string> {
  const { provider, modelId } = classifierModelRef();
  const model = ctx.modelRegistry.find(provider, modelId);
  if (!model) throw new Error(`classifier model ${provider}/${modelId} is not configured in Pi`);
  const auth = await ctx.modelRegistry.getApiKeyAndHeaders(model);
  if (!auth.ok || !auth.apiKey) {
    throw new Error(auth.ok ? `classifier model ${provider}/${modelId} has no API key` : auth.error);
  }
  const timeoutMs = Number(process.env.MCPLEXER_ROUTER_CLASSIFY_TIMEOUT_MS ?? 8000);
  const controller = new AbortController();
  const timeout = setTimeout(() => controller.abort(), Number.isFinite(timeoutMs) ? timeoutMs : 8000);
  const message: UserMessage = {
    role: "user",
    content: [{ type: "text", text: prompt }],
    timestamp: Date.now(),
  };
  try {
    const response = await complete(
      model,
      { messages: [message] },
      {
        apiKey: auth.apiKey,
        headers: auth.headers,
        env: auth.env,
        maxTokens: 512,
        signal: controller.signal,
      },
    );
    return response.content
      .filter((part): part is { type: "text"; text: string } => part.type === "text")
      .map((part) => part.text)
      .join("\n");
  } finally {
    clearTimeout(timeout);
  }
}

// runShim invokes `node mcpx-shim <tool>` and feeds the JSON arguments on
// stdin (avoids any shell-quoting hazards). Resolves with the shim's stdout
// and a success flag derived from its exit code. Honors the abort signal.
function runShim(tool: string, args: unknown, signal?: AbortSignal): Promise<ShimResult> {
  return new Promise((resolve) => {
    const child = spawn(process.execPath, [SHIM, tool], {
      stdio: ["pipe", "pipe", "pipe"],
      signal,
    });
    let out = "";
    let err = "";
    child.stdout.on("data", (d) => (out += d.toString()));
    child.stderr.on("data", (d) => (err += d.toString()));
    child.stdin.on("error", () => {}); // ignore EPIPE if the child died before stdin write
    child.on("error", (e) => resolve({ ok: false, text: `failed to run mcpx-shim: ${e.message}` }));
    child.on("close", (code) => {
      const text = out.trim() || err.trim();
      resolve({ ok: code === 0, text });
    });
    if (child.stdin.writable) {
      child.stdin.write(typeof args === "string" ? args : JSON.stringify(args ?? {}));
      child.stdin.end();
    }
  });
}

function toToolResult(tool: string, res: ShimResult) {
  return {
    content: [{ type: "text" as const, text: res.text || "(empty result)" }],
    details: { tool, ok: res.ok },
    isError: !res.ok,
  };
}

export default function (pi: ExtensionAPI) {
  // Pi normally exposes extension flags by session_start, but non-interactive
  // invocations can deliver their first input without that hook having applied
  // the flag. Keep an explicit command override so /router off still wins.
  let routerCommandOverride: boolean | null = null;
  const applyRouterFlag = () => {
    if (routerCommandOverride === null && pi.getFlag("mcpx-router") === true) {
      setRouterEnabled(true);
    }
  };

  // 1. Discovery — find any callable function (downstream MCP servers + the
  //    built-in task/mesh/memory/skill surfaces). Returns names + descriptions;
  //    pass detail:"full" for TypeScript signatures before writing a snippet.
  pi.registerTool({
    name: "mcpx_search",
    label: "MCPlexer: search tools",
    description:
      "Discover callable mcplexer functions (downstream MCP tools + built-in " +
      "task/mesh/memory/skill surfaces) without loading their definitions up front. " +
      'Pass queries and optionally detail:"full" for signatures. Returns matches as text.',
    parameters: Type.Object({
      queries: Type.Array(Type.String(), {
        description: 'Search terms, e.g. ["task create","github issues"].',
      }),
      detail: Type.Optional(
        Type.String({ description: '"compact" (default) or "full" for TS signatures.' })
      ),
    }),
    async execute(_toolCallId, params, signal) {
      const res = await runShim("mcpx__search_tools", params, signal);
      return toToolResult("mcpx__search_tools", res);
    },
  });

  // 2. Execution — run a JS snippet that calls discovered functions as
  //    `<namespace>.<tool>(args)`. Batch related calls; results auto-unwrap.
  pi.registerTool({
    name: "mcpx_exec",
    label: "MCPlexer: execute code",
    description:
      "Run a JavaScript snippet in mcplexer's Code Mode sandbox. Call any discovered " +
      "function as `namespace.tool(args)` (synchronous, no await), batch related calls, " +
      "and print only the filtered result. This is the universal entrypoint for every " +
      "downstream MCP tool and the task/mesh/memory/skill namespaces.",
    parameters: Type.Object({
      code: Type.String({ description: "JavaScript to execute; use print(...) for output." }),
    }),
    async execute(_toolCallId, params, signal) {
      const res = await runShim("mcpx__execute_code", params, signal);
      return toToolResult("mcpx__execute_code", res);
    },
  });

  // 3. Secret references — pass `secret://KEY` strings as tool args; plaintext
  //    is substituted at dispatch time and never enters the agent's context.
  pi.registerTool({
    name: "mcpx_secret_refs",
    label: "MCPlexer: list secret refs",
    description:
      "List the secret reference keys available to this workspace. Use the returned " +
      "`secret://KEY` strings as tool arguments inside mcpx_exec; the gateway substitutes " +
      "plaintext at dispatch time so the value never enters the context window.",
    parameters: Type.Object({}),
    async execute(_toolCallId, _params, signal) {
      const res = await runShim("secret__list_refs", {}, signal);
      return toToolResult("secret__list_refs", res);
    },
  });

  // 4. Interactive secret prompt — ask the human for a credential the agent
  //    must never see. The gateway captures it and returns only a reference.
  pi.registerTool({
    name: "mcpx_secret_prompt",
    label: "MCPlexer: prompt for secret",
    description:
      "Request a credential from the human without the agent ever seeing the value. " +
      "Provide the key to store under and a human-readable prompt; the gateway captures " +
      "the secret and returns a `secret://KEY` reference for later use.",
    parameters: Type.Object({
      key: Type.String({ description: "Reference key to store the secret under." }),
      prompt: Type.Optional(Type.String({ description: "Message shown to the human." })),
    }),
    async execute(_toolCallId, params, signal) {
      const res = await runShim("secret__prompt", params, signal);
      return toToolResult("secret__prompt", res);
    },
  });

  // Convenience command: point the agent (or the human) at the on-demand skill
  // rather than preloading its body. Keeps the context lean — read it when you
  // need the playbook, not before.
  pi.registerCommand("mcplexer", {
    description: "How to use the mcplexer gateway from Pi (reads the bundled skill).",
    handler: async (_args, ctx) => {
      ctx.ui.notify(
        "mcplexer: 4 tools — mcpx_search (discover) -> mcpx_exec (batch calls) -> " +
          "mcpx_secret_refs / mcpx_secret_prompt. Full playbook: /skill:mcplexer.",
        "info"
      );
    },
  });

  // --- Router mode (opt-in via --mcpx-router or /router on) ---
  pi.registerFlag("mcpx-router", {
    description: "Route substantive prompts through a fast local classifier and MCPlexer delegation",
    type: "boolean",
    default: false,
  });

  pi.on("session_start", applyRouterFlag);

  pi.registerCommand("router", {
    description: "MCPlexer router: on | off | status | cancel | score <0-100>",
    handler: async (args, ctx) => {
      const parts = args.trim().split(/\s+/).filter(Boolean);
      const sub = (parts[0] ?? "status").toLowerCase();
      if (sub === "on") {
        routerCommandOverride = true;
        setRouterEnabled(true);
        ctx.ui.notify("MCPlexer router enabled", "info");
      } else if (sub === "off") {
        routerCommandOverride = false;
        setRouterEnabled(false);
        ctx.ui.notify("MCPlexer router disabled; normal Pi restored", "info");
      } else if (sub === "cancel") {
        activeRouterController?.abort();
        ctx.ui.notify("Stopped waiting locally; an already-created delegation continues in MCPlexer", "warning");
      } else if (sub === "score") {
        const state = getRouterState();
        const score = Number(parts[1]);
        if (!state.last_delegation_id || !Number.isFinite(score) || score < 0 || score > 100) {
          ctx.ui.notify("Usage: /router score <0-100> after a routed result", "warning");
          return;
        }
        const reviewed = await reviewDelegation(runShim, state.last_delegation_id, score);
        ctx.ui.notify(reviewed.ok ? `Recorded router score ${Math.round(score)}` : "Could not record delegation score", reviewed.ok ? "info" : "warning");
      } else {
        const state = getRouterState();
        ctx.ui.notify(
          `Router ${state.enabled ? "ON" : "OFF"} · busy ${state.busy ? "yes" : "no"} · model ${state.last_route ?? "none"} · delegation ${state.last_delegation_id ?? "none"}`,
          "info",
        );
      }
    },
  });

  pi.on("input", async (event, ctx) => {
    applyRouterFlag();
    const meta: InputMeta = {
      has_images: Boolean(event.images?.length),
      is_slash_command: event.text.trimStart().startsWith("/"),
      origin: event.source,
      streaming: event.streamingBehavior !== undefined,
      text: event.text,
    };
    const state = getRouterState();
    if (state.enabled && state.busy && !meta.streaming && meta.origin !== "extension") {
      ctx.ui.notify(formatBusyResponse(), "warning");
      return { action: "handled" };
    }
    if (!state.enabled || meta.streaming || meta.origin === "extension" || meta.is_slash_command || meta.has_images || !meta.text.trim()) {
      return { action: "continue" };
    }

    activeRouterController = new AbortController();
    try {
      const result = await route(
        event.text,
        meta,
        (prompt) => classifyWithLocalModel(ctx, prompt),
        runShim,
        activeRouterController.signal,
      );
      if (!result) return { action: "continue" };
      pi.sendMessage({
        customType: "mcplexer-router-result",
        content: formatRouteResult(result),
        display: true,
        details: {
          delegation_id: result.delegation_id,
          model: result.chosen.candidate.id,
          score: result.chosen.score,
          breakdown: result.chosen.breakdown,
        },
      });
      return { action: "handled" };
    } catch (error) {
      ctx.ui.notify(
        `Router passed through to normal Pi: ${error instanceof Error ? error.message : String(error)}`,
        "warning",
      );
      return { action: "continue" };
    } finally {
      activeRouterController = null;
    }
  });
}
