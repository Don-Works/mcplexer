#!/usr/bin/env node
// mcpx-shim — a tiny, dependency-free MCP client that performs ONE tools/call
// round-trip against the local mcplexer daemon and prints the result text.
//
// Why this exists: Pi (pi.dev / Earendil) is deliberately MCP-skeptical — a
// classic MCP server dumps thousands of tokens of tool definitions into the
// context window at startup. mcplexer answers that critique with a slim
// 4-tool surface (mcpx__search_tools, mcpx__execute_code, secret__prompt,
// secret__list_refs). This shim lets a Pi extension (or a human at a shell,
// matching Pi's "CLI + README, not MCP bloat" ethos) reach exactly those four
// tools without registering anything heavy with Pi.
//
// Transport: it spawns `mcplexer connect --socket=<path>`, which bridges
// stdin/stdout to the daemon's Unix socket and replays the MCP handshake on
// reconnect. We speak newline-delimited JSON-RPC 2.0 over that pipe:
//   1. initialize
//   2. notifications/initialized
//   3. tools/call { name, arguments }
// then read the matching response, print result content, and exit.
//
// Usage:
//   mcpx-shim <tool> '<json-args>'
//   echo '<json-args>' | mcpx-shim <tool>
//
// Env:
//   MCPLEXER_BIN          path to the mcplexer binary   (default: "mcplexer")
//   MCPLEXER_SOCKET_PATH  daemon socket                 (default: /tmp/mcplexer.sock,
//                                                         or $XDG_RUNTIME_DIR/mcplexer.sock on Linux)
//   MCPLEXER_CLIENT_CWD   workspace root reported to the gateway (default: cwd)
//
// Exit codes: 0 ok, 1 tool/transport error, 2 bad usage.

import { spawn } from "node:child_process";
import { createInterface } from "node:readline";
import { existsSync } from "node:fs";
import { homedir } from "node:os";

const PROTOCOL_VERSION = "2025-06-18";

// Resolve the mcplexer binary by ABSOLUTE path so the shim works regardless of
// the (often minimal) PATH a daemon-spawned worker subprocess inherits — PATH is
// the last resort only. Permanent, machine-independent fix for "mcplexer gateway
// unavailable" inside pi_cli workers: a launchd/systemd daemon's PATH need not
// contain the dir holding `mcplexer`, but the canonical install path
// ~/.mcplexer/bin/mcplexer exists on every machine that runs the daemon.
function resolveMcplexerBin() {
  if (process.env.MCPLEXER_BIN) return process.env.MCPLEXER_BIN;
  const home = homedir();
  const candidates = [
    home + "/.mcplexer/bin/mcplexer",
    home + "/bin/mcplexer",
    "/opt/homebrew/bin/mcplexer",
    "/usr/local/bin/mcplexer",
  ];
  for (let i = 0; i < candidates.length; i++) {
    try { if (existsSync(candidates[i])) return candidates[i]; } catch (e) {}
  }
  return "mcplexer";
}

function defaultSocketPath() {
  if (process.env.MCPLEXER_SOCKET_PATH) return process.env.MCPLEXER_SOCKET_PATH;
  if (process.platform === "linux" && process.env.XDG_RUNTIME_DIR) {
    return `${process.env.XDG_RUNTIME_DIR}/mcplexer.sock`;
  }
  return "/tmp/mcplexer.sock";
}

function die(code, msg) {
  process.stderr.write(`mcpx-shim: ${msg}\n`);
  process.exit(code);
}

async function readStdin() {
  if (process.stdin.isTTY) return "";
  const chunks = [];
  for await (const c of process.stdin) chunks.push(c);
  return Buffer.concat(chunks).toString("utf8").trim();
}

async function main() {
  const tool = process.argv[2];
  if (!tool || tool === "-h" || tool === "--help") {
    die(2, "usage: mcpx-shim <tool> '<json-args>'   (args may also arrive on stdin)");
  }

  let rawArgs = process.argv[3];
  if (rawArgs === undefined) rawArgs = await readStdin();
  if (!rawArgs) rawArgs = "{}";

  let args;
  try {
    args = JSON.parse(rawArgs);
  } catch (e) {
    die(2, `arguments are not valid JSON: ${e.message}`);
  }

  const bin = resolveMcplexerBin();
  const socket = defaultSocketPath();

  // Abort on SIGINT/SIGTERM so an interrupt that lands before (or during) the
  // spawn is observable as signal.aborted rather than leaving a dangling child.
  const ac = new AbortController();
  const signal = ac.signal;
  const onSig = () => ac.abort();
  process.once("SIGINT", onSig);
  process.once("SIGTERM", onSig);

  // Bail before spawning anything if we were already interrupted.
  if (signal?.aborted) {
    die(1, "aborted before spawn");
  }

  const child = spawn(bin, ["connect", `--socket=${socket}`], {
    signal,
    stdio: ["pipe", "pipe", "inherit"],
    env: {
      ...process.env,
      MCPLEXER_CLIENT_CWD: process.env.MCPLEXER_CLIENT_CWD || process.cwd(),
    },
  });

  child.on("error", (e) => die(1, `failed to spawn ${bin}: ${e.message}`));
  // A broken pipe (daemon exits before we finish writing) must not crash the
  // shim with an unhandled 'error' event — swallow it; the read side reports
  // the real failure via rl 'close'.
  child.stdin.on("error", () => {});

  // Guard every write: if the child's stdin is no longer writable (it exited,
  // or the pipe broke), drop the frame instead of throwing.
  const send = (obj) => {
    if (!child.stdin.writable) return;
    child.stdin.write(JSON.stringify(obj) + "\n");
  };

  // MCP handshake, then the single tools/call.
  send({
    jsonrpc: "2.0",
    id: 1,
    method: "initialize",
    params: {
      protocolVersion: PROTOCOL_VERSION,
      capabilities: {},
      clientInfo: { name: "pi-coding-agent", version: pkgVersion() },
    },
  });
  send({ jsonrpc: "2.0", method: "notifications/initialized" });
  send({ jsonrpc: "2.0", id: 2, method: "tools/call", params: { name: tool, arguments: args } });

  const rl = createInterface({ input: child.stdout });
  let settled = false;

  const finish = (code, text) => {
    if (settled) return;
    settled = true;
    rl.close();
    child.stdin.end();
    child.kill();
    if (text) process.stdout.write(text.endsWith("\n") ? text : text + "\n");
    process.exit(code);
  };

  rl.on("line", (line) => {
    line = line.trim();
    if (!line) return;
    let msg;
    try {
      msg = JSON.parse(line);
    } catch {
      return; // ignore non-JSON noise (reconnect notices go to stderr anyway)
    }

    // The initialize (id:1) response: if the handshake itself failed, finish
    // immediately with that error instead of waiting out the timeout for the
    // tools/call (id:2) response that will never come.
    if (msg.id === 1) {
      if (msg.error) {
        finish(1, `initialize error ${msg.error.code}: ${msg.error.message}`);
      }
      return; // successful init — keep waiting for the id:2 response
    }

    if (msg.id !== 2) return; // wait for the tools/call response

    if (msg.error) {
      finish(1, `error ${msg.error.code}: ${msg.error.message}`);
      return;
    }
    finish(toolResultExit(msg.result), renderResult(msg.result));
  });

  rl.on("close", () => {
    if (!settled) die(1, "daemon closed the connection before responding (is the daemon running?)");
  });

  // Safety timeout so a hung daemon never wedges Pi.
  const timeoutMs = Number(process.env.MCPLEXER_SHIM_TIMEOUT_MS || 60000);
  setTimeout(() => {
    if (!settled) die(1, `timed out after ${timeoutMs}ms waiting for ${tool}`);
  }, timeoutMs).unref();
}

// renderResult flattens the MCP tools/call result envelope into plain text.
// Text content is concatenated; everything else is JSON-stringified so the
// caller (the Pi extension or a human) sees something useful regardless.
function renderResult(result) {
  if (!result) return "";
  const content = Array.isArray(result.content) ? result.content : [];
  const parts = content.map((c) =>
    c && c.type === "text" && typeof c.text === "string" ? c.text : JSON.stringify(c)
  );
  return parts.join("\n");
}

function toolResultExit(result) {
  return result && result.isError ? 1 : 0;
}

function pkgVersion() {
  return process.env.MCPLEXER_PI_VERSION || "0.0.1";
}

main().catch((e) => die(1, e && e.message ? e.message : String(e)));
