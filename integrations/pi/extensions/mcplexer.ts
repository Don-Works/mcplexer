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
import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";
import {
  route,
  getRouterState,
  setRouterEnabled,
  formatRouteResult,
} from "./router/index.ts";
import type { InputMeta } from "./router/index.ts";

const here = dirname(fileURLToPath(import.meta.url));
// extensions/ and bin/ are siblings inside the package.
const SHIM = join(here, "..", "bin", "mcpx-shim.mjs");

interface ShimResult {
  ok: boolean;
  text: string;
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

  // Check --mcpx-router flag on startup
  if (process.argv.includes("--mcpx-router")) {
    setRouterEnabled(true);
  }

  // /router on|off|status — toggle or inspect the MCPlexer router.
  pi.registerCommand("router", {
    description: "Toggle or inspect the MCPlexer model router (on|off|status).",
    handler: async (args, ctx) => {
      const sub = (args?.[0] ?? "status").toLowerCase();
      if (sub === "on") {
        setRouterEnabled(true);
        ctx.ui.notify("Router enabled. Input will be classified and routed to the best model via MCPlexer delegation.", "info");
      } else if (sub === "off") {
        setRouterEnabled(false);
        ctx.ui.notify("Router disabled. Normal Pi behavior restored.", "info");
      } else {
        const state = getRouterState();
        ctx.ui.notify(
          `Router: ${state.enabled ? "ON" : "OFF"} | Busy: ${state.busy} | Last route: ${state.last_route ?? "none"}`,
          "info",
        );
      }
    },
  });

  // Input hook: intercept user input when the router is enabled.
  // Bypasses: extension-origin, slash commands, images, empty input.
  // On classifier/dispatch failure: fail open to normal Pi with a warning.
  pi.registerInputHook(async (input, ctx) => {
    // Only intercept user text input
    if (input.type !== "user_text") return undefined;

    const meta: InputMeta = {
      has_images: false,
      is_slash_command: input.text.startsWith("/"),
      origin: "user",
      text: input.text,
    };

    if (!meta.is_slash_command && getRouterState().enabled && !getRouterState().busy) {
      try {
        // Get the classifier's LLM function from the context
        const completeFn = async (prompt: string): Promise<string> => {
          const result = await ctx.complete(prompt, { maxTokens: 512 });
          return typeof result === "string" ? result : result.text;
        };

        const result = await route(input.text, meta, completeFn, runShim);

        if (result) {
          // Route succeeded — show the result as a custom message
          const formatted = formatRouteResult(result);
          ctx.ui.notify(formatted, "info");
          return { action: "handled" };
        }
        // result === null means passthrough or failure — let normal Pi handle it
      } catch (err) {
        // Fail open with a warning
        ctx.ui.notify(
          `[mcplexer-router] routing failed (${err instanceof Error ? err.message : "unknown"}), falling through to normal Pi`,
          "warning",
        );
      }
    }

    // Not intercepted — normal Pi processing
    return undefined;
  });
}
