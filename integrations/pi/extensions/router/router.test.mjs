import { readFile } from "node:fs/promises";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import test, { describe } from "node:test";
import assert from "node:assert/strict";

import {
  ROUTER_SENTINEL,
  ROUTER_META_SENTINEL,
  ROUTER_OUTPUT_SENTINEL,
  ROUTER_ROW_SENTINEL,
  capacityRowsToCandidates,
  compileCapabilities,
  delegationSnapshot,
  encodeJSONStringArgument,
  parseCandidateOverrides,
  parseClassifierOutput,
  parseDelegationPollText,
  parseSentinelJSON,
  parseSentinelJSONLines,
  rankCandidates,
  shouldInterceptInput,
} from "./core.mjs";

const HERE = dirname(fileURLToPath(import.meta.url));

function decision(overrides = {}) {
  return {
    action: "delegate",
    task_kind: "coding",
    quality: "medium",
    worker_mode: "execute",
    tool_intents: ["repo_read", "repo_write", "tests_run"],
    risk: "safe",
    requirements: ["code", "tool_use"],
    reason: "bounded coding task",
    ...overrides,
  };
}

function candidate(id, overrides = {}) {
  const slash = id.indexOf("/");
  return {
    id,
    provider: id.slice(0, slash),
    model_id: id.slice(slash + 1),
    label: id,
    capabilities: ["code", "reasoning", "analysis", "tool_use", "workspace_tools"],
    modalities: ["text"],
    task_priors: {},
    benchmark_scores: {},
    operator_priority: 50,
    tags: [],
    live: {},
    ...overrides,
  };
}

describe("classifier contract", () => {
  test("accepts the strict schema", () => {
    const parsed = parseClassifierOutput(JSON.stringify(decision()));
    assert.equal(parsed?.worker_mode, "execute");
    assert.deepEqual(parsed?.tool_intents, ["repo_read", "repo_write", "tests_run"]);
  });

  test("accepts fenced JSON but rejects prose and malformed JSON", () => {
    assert.equal(parseClassifierOutput(`\`\`\`json\n${JSON.stringify(decision())}\n\`\`\``)?.task_kind, "coding");
    assert.equal(parseClassifierOutput(`result: ${JSON.stringify(decision())}`), null);
    assert.equal(parseClassifierOutput("{"), null);
  });

  test("rejects extra fields and unknown permission intents", () => {
    assert.equal(parseClassifierOutput(JSON.stringify({ ...decision(), model_id: "opus" })), null);
    assert.equal(parseClassifierOutput(JSON.stringify(decision({ tool_intents: ["github__delete_repo"] }))), null);
  });

  test("uses MCPlexer's real review worker mode", () => {
    assert.equal(parseClassifierOutput(JSON.stringify(decision({ worker_mode: "review" })))?.worker_mode, "review");
    assert.equal(parseClassifierOutput(JSON.stringify(decision({ worker_mode: "explore" }))), null);
  });
});

describe("live candidate catalog", () => {
  const rows = [
    {
      model_provider: "mimo_cli",
      model_id: "xiaomi/mimo-v2.5-pro",
      model_profile_id: "profile-mimo",
      label: "MiMo",
      available: true,
      review_count: 3,
      review_score: 88,
      success_rate: 0.9,
      avg_duration_ms: 20_000,
      cost_usd: 0.02,
      accounting_known: true,
      running: 0,
    },
    {
      model_provider: "claude_cli",
      model_id: "opus",
      model_profile_id: "profile-claude",
      available: true,
      review_count: 0,
      running: 1,
    },
    { model_provider: "broken", model_id: "offline", available: false },
  ];

  test("turns live capacity rows into dispatchable candidates", () => {
    const candidates = capacityRowsToCandidates(rows);
    assert.equal(candidates.length, 2);
    assert.equal(candidates[0].model_profile_id, "profile-mimo");
    assert.equal(candidates[0].live.review_score, 88);
    assert.ok(candidates[0].operator_priority > candidates[1].operator_priority);
  });

  test("overlays task benchmark, speed, and capability evidence", () => {
    const overrides = [{
      id: "mimo_cli/xiaomi/mimo-v2.5-pro",
      benchmark_scores: { coding: 82 },
      task_priors: { review: 91 },
      speed_score: 87,
      capabilities: ["code", "reasoning", "analysis", "tool_use", "vision"],
    }];
    const [mimo] = capacityRowsToCandidates(rows, overrides);
    assert.equal(mimo.benchmark_scores.coding, 82);
    assert.equal(mimo.speed_score, 87);
    assert.ok(mimo.capabilities.includes("vision"));
  });

  test("parses only structurally usable overrides", () => {
    const parsed = parseCandidateOverrides(JSON.stringify([
      { provider: "x", model_id: "y" },
      { nonsense: true },
    ]));
    assert.equal(parsed.length, 1);
  });
});

describe("evidence-weighted ranking", () => {
  test("SWE-bench-style coding evidence can outrank a generic favorite", () => {
    const models = [
      candidate("a/general", { benchmark_scores: { coding: 55 }, operator_priority: 95 }),
      candidate("b/coder", { benchmark_scores: { coding: 92 }, operator_priority: 50 }),
    ];
    assert.equal(rankCandidates(models, decision({ quality: "high" }))[0].candidate.id, "b/coder");
  });

  test("task-specific attributes change the winner", () => {
    const models = [
      candidate("a/code", { benchmark_scores: { coding: 90, review: 45 } }),
      candidate("b/reviewer", { benchmark_scores: { coding: 55, review: 94 } }),
    ];
    assert.equal(rankCandidates(models, decision())[0].candidate.id, "a/code");
    assert.equal(rankCandidates(models, decision({ task_kind: "review", worker_mode: "review", tool_intents: ["repo_read"], requirements: ["code", "reasoning"] }))[0].candidate.id, "b/reviewer");
  });

  test("reviewed runtime evidence overtakes neutral priors", () => {
    const models = [
      candidate("a/unreviewed", { live: { review_count: 0, success_rate: 0.5 } }),
      candidate("b/proven", { live: { review_count: 8, review_score: 95, success_rate: 0.98 } }),
    ];
    assert.equal(rankCandidates(models, decision())[0].candidate.id, "b/proven");
  });

  test("speed matters more for low-quality routing", () => {
    const models = [
      candidate("a/slow", { benchmark_scores: { general: 75 }, speed_score: 10 }),
      candidate("b/fast", { benchmark_scores: { general: 60 }, speed_score: 100 }),
    ];
    const routed = decision({ task_kind: "general", quality: "low", worker_mode: "review", tool_intents: [], requirements: [] });
    assert.equal(rankCandidates(models, routed)[0].candidate.id, "b/fast");
  });

  test("missing CLI accounting is neutral rather than free", () => {
    const missing = candidate("a/missing", { live: { accounting_known: false, cost_usd: 0 } });
    const knownFree = candidate("b/free", { cost_score: 100, live: { accounting_known: true, cost_usd: 0 } });
    const ranked = rankCandidates([missing, knownFree], decision({ quality: "low" }));
    assert.equal(ranked.find((row) => row.candidate.id === "a/missing").breakdown.cost, 50);
    assert.equal(ranked[0].candidate.id, "b/free");
  });

  test("cumulative live cost does not penalize models with more history", () => {
    const newModel = candidate("a/new", { live: { accounting_known: true, cost_usd: 0.02 } });
    const established = candidate("b/established", { live: { accounting_known: true, cost_usd: 12 } });
    const ranked = rankCandidates([newModel, established], decision());
    assert.equal(ranked[0].breakdown.cost, 50);
    assert.equal(ranked[1].breakdown.cost, 50);
  });

  test("active load and capability gates affect eligibility", () => {
    const loaded = candidate("a/loaded", { live: { running: 4 } });
    const idle = candidate("b/idle", { live: { running: 0 } });
    assert.equal(rankCandidates([loaded, idle], decision())[0].candidate.id, "b/idle");
    const vision = decision({ requirements: ["code", "tool_use", "vision"] });
    assert.equal(rankCandidates([loaded, idle], vision).length, 0);
  });

  test("ties are deterministic", () => {
    const ids = rankCandidates([candidate("z/model"), candidate("a/model")], decision()).map((row) => row.candidate.id);
    assert.deepEqual(ids, ["a/model", "z/model"]);
  });
});

describe("capability policy", () => {
  test("keeps JSON-string allowlists intact through legacy gateway coercion", () => {
    const encoded = encodeJSONStringArgument(["mcpx__execute_code", "index__context"]);
    assert.equal(encoded[0], " ");
    assert.deepEqual(JSON.parse(encoded), ["mcpx__execute_code", "index__context"]);
  });

  test("uses real MCPlexer coder fields and gateway tool names", () => {
    const bundle = compileCapabilities(decision());
    assert.equal(bundle.capability_preset, "coder");
    assert.equal(bundle.worker_mode, "execute");
    assert.ok(bundle.tool_allowlist.includes("index__context"));
    assert.ok(!bundle.tool_allowlist.includes("edit"));
    assert.equal(bundle.capability_profile.features.may_use_secrets, false);
    assert.equal(bundle.capability_profile.features.may_create_subdelegation, false);
  });

  test("research web access grants only the explicit fetch tool", () => {
    const bundle = compileCapabilities(decision({
      task_kind: "research",
      worker_mode: "review",
      tool_intents: ["web_read"],
      requirements: ["reasoning"],
    }));
    assert.equal(bundle.capability_preset, "researcher");
    assert.ok(bundle.tool_allowlist.includes("fetch__fetch"));
    assert.ok(!bundle.tool_allowlist.some((tool) => tool.startsWith("brw__")));
    assert.ok(!bundle.capability_profile.namespace_allow.includes("brw"));
    assert.equal(bundle.capability_profile.features.may_write_tasks, false);
  });

  test("dangerous, external-write, secret, and admin routes are blocked", () => {
    for (const toolIntent of ["github_write", "secrets", "admin", "deploy", "external_write", "destructive_write"]) {
      assert.match(compileCapabilities(decision({ tool_intents: [toolIntent] })).blocked_reason, /requires/);
    }
    assert.match(compileCapabilities(decision({ risk: "elevated" })).blocked_reason, /risk level/);
  });

  test("classifier strings never become permissions", () => {
    const bundle = compileCapabilities(decision({ tool_intents: ["repo_read"] }));
    assert.ok(!bundle.tool_allowlist.includes("repo_read"));
  });
});

describe("gateway response parsing", () => {
  test("extracts sentinel JSON despite code-mode footer text", () => {
    const text = `${ROUTER_SENTINEL}{"delegation_id":"del-1"}\n--- 1 tool call executed`;
    assert.equal(parseSentinelJSON(text).delegation_id, "del-1");
  });

  test("parses independently printed capacity rows without rich-string truncation", () => {
    const text = [
      `${ROUTER_ROW_SENTINEL}{"model_provider":"mimo_cli","model_id":"mimo"}`,
      `${ROUTER_ROW_SENTINEL}{"model_provider":"claude_cli","model_id":"sonnet"}`,
      "--- 1 tool call executed",
    ].join("\n");
    assert.equal(parseSentinelJSONLines(text, ROUTER_ROW_SENTINEL).length, 2);
  });

  test("reassembles chunked delegation output", () => {
    const text = [
      `${ROUTER_META_SENTINEL}{"status":"needs_review","success":1,"failure":0}`,
      `${ROUTER_OUTPUT_SENTINEL}"hello "`,
      `${ROUTER_OUTPUT_SENTINEL}"world"`,
    ].join("\n");
    const snapshot = parseDelegationPollText(text);
    assert.equal(snapshot.successful, true);
    assert.equal(snapshot.output, "hello world");
  });

  test("extracts output from the real delegation tree shape", () => {
    const snapshot = delegationSnapshot({
      status: "needs_review",
      aggregate: { success: 1, failure: 0 },
      workers: [{ latest_run: { output_text: "done", error: "" } }],
    });
    assert.equal(snapshot.terminal, true);
    assert.equal(snapshot.successful, true);
    assert.equal(snapshot.output, "done");
  });

  test("keeps running and dispatched states non-terminal", () => {
    assert.equal(delegationSnapshot({ status: "running", aggregate: {}, workers: [] }).terminal, false);
    assert.equal(delegationSnapshot({ status: "dispatched", aggregate: {}, workers: [] }).terminal, false);
  });
});

test("an abort after delegation creation never replays the prompt", {
  skip: Number.parseInt(process.versions.node, 10) < 23,
}, async () => {
  const { dispatch } = await import("./dispatch.ts");
  const routeDecision = decision();
  const chosen = rankCandidates([candidate("test/model")], routeDecision)[0];
  const bundle = compileCapabilities(routeDecision);
  const controller = new AbortController();
  let calls = 0;
  const runShim = async () => {
    calls += 1;
    if (calls !== 1) throw new Error("polling must not start after abort");
    setTimeout(() => controller.abort(), 0);
    return {
      ok: true,
      text: `${ROUTER_SENTINEL}${JSON.stringify({ delegation_id: "del-test" })}`,
    };
  };

  const result = await dispatch(
    runShim,
    "bounded test task",
    chosen,
    bundle,
    routeDecision,
    1,
    1_000,
    controller.signal,
  );

  assert.equal(result.started, true);
  assert.equal(result.delegation_id, "del-test");
  assert.equal(result.status, "cancelled_wait");
  assert.match(result.error ?? "", /continues in MCPlexer/);
  assert.equal(calls, 1);
});

describe("input interception", () => {
  const meta = { origin: "interactive", streaming: false, is_slash_command: false, has_images: false, text: "review this" };
  test("intercepts only enabled, idle, plain user input", () => {
    assert.equal(shouldInterceptInput(true, false, meta), true);
    assert.equal(shouldInterceptInput(false, false, meta), false);
    assert.equal(shouldInterceptInput(true, true, meta), false);
    assert.equal(shouldInterceptInput(true, false, { ...meta, origin: "extension" }), false);
    assert.equal(shouldInterceptInput(true, false, { ...meta, streaming: true }), false);
    assert.equal(shouldInterceptInput(true, false, { ...meta, is_slash_command: true }), false);
    assert.equal(shouldInterceptInput(true, false, { ...meta, has_images: true }), false);
  });
});

test("extension uses installed Pi 0.80 input and completion APIs", async () => {
  const source = await readFile(join(HERE, "..", "mcplexer.ts"), "utf8");
  assert.match(source, /pi\.on\("input"/);
  assert.match(source, /registerFlag\("mcpx-router"/);
  assert.match(source, /await complete\(/);
  assert.doesNotMatch(source, /registerInputHook/);
  assert.doesNotMatch(source, /ctx\.complete/);
});
