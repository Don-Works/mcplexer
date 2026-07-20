// citation-gate.js — a reusable `post_execute_script` for mcpx__delegate_worker.
//
// WHY: a delegated worker's value is that the parent trusts a citation without
// re-reading the file. Measured on qwen3.6-35b-a3b (pi_cli/qwen-local), 3 of 4
// answers were semantically correct but cited the WRONG line, and mcplexer
// reported all four as clean successes. A wrong line that passes as "success"
// is worse than an outright failure, because it is silently believed.
//
// This gate re-derives every `file.go:NN` claim from the shared code index and
// REJECTS the output when a citation is positively contradicted.
//
// USAGE (note the two non-obvious requirements):
//
//   mcpx__delegate_worker({
//     ...,
//     worker_isolation: "none",              // required for CLI providers
//     tool_allowlist_json: " [\"mcpx__execute_code\",\"index__summary\"," +
//                          "\"index__symbols\"]",   // <- LEADING SPACE, see below
//     post_execute_script: <contents of this file>
//   })
//
//   1. `index__summary` + `index__symbols` MUST be in tool_allowlist_json. They
//      are NOT in the default delegation allowlist, and hooks are fail-closed —
//      without them every run is rejected on a dispatch error.
//   2. tool_allowlist_json needs a LEADING SPACE. coerceStringifiedArgs
//      (internal/gateway/args_coerce.go:50) re-parses any string starting with
//      '[' into an array, which the string-typed field then refuses. The space
//      sidesteps it; Go's json.Unmarshal skips leading whitespace.
//
// `mcpx__workspace_read_file` would be the more direct primitive, but it needs a
// live isolated-worker scope (internal/gateway/builtin_tools_workspace.go:42)
// and CLI providers are hard-refused worktree isolation
// (internal/workers/runner/runner.go:318), so it is unavailable exactly where
// this gate is needed.
//
// DESIGN RULE: reject only on positive evidence of a contradiction. Absence of
// citations, an unresolvable basename, or a line we cannot anchor to a symbol
// all PASS. A gate that fires on ignorance is worse than no gate — it destroys
// good output and trains the operator to disable it.

var STOP = {};
(function () {
  var w = ("the a an and or of in on at to is are was were be been it its this that these those " +
    "for with from by as if then else return func type const var package import struct interface " +
    "map string int bool error nil true false line lines file files code source repo defines " +
    "defined definition declared value constant function method field see at which where when " +
    "answer question note actual real found here there also does not have has").split(" ");
  for (var i = 0; i < w.length; i++) STOP[w[i]] = true;
})();

// Bound total index traffic so a citation-dense answer cannot stall finalize.
var CALLS = 0;
var MAX_CALLS = 120;

function symbolsOf(q) {
  if (CALLS >= MAX_CALLS) return [];
  CALLS++;
  try {
    var r = index.symbols({ query: q, limit: 25 });
    return (r && r.symbols) || [];
  } catch (e) { return []; }
}

function summaryOf(f) {
  if (CALLS >= MAX_CALLS) return null;
  CALLS++;
  try { return index.summary({ file: f }); } catch (e) { return null; }
}

// parseCitations pulls {file,line} out of free text in two shapes:
//   internal/pkg/file.go:123      and      "line 123 of internal/pkg/file.go"
function parseCitations(text) {
  var out = [], seen = {};
  function add(file, line) {
    if (!file || !line) return;
    file = file.replace(/^\.\//, "").replace(/[.,;:)\]]+$/, "");
    var n = parseInt(line, 10);
    if (!(n > 0)) return;
    var k = file + ":" + n;
    if (seen[k]) return;
    seen[k] = true;
    out.push({ file: file, line: n, key: k });
  }
  var ext = "(?:go|ts|tsx|js|jsx|mjs|sh|ya?ml|sql|md|json)";
  var FILE = "([A-Za-z0-9_][A-Za-z0-9_./-]*\\." + ext + ")";
  // Models emit citations wrapped in markdown: **line 108** in `path/file.go`.
  // Without tolerating the wrappers the phrase form silently never matches,
  // which reads as "no citations present" and waves the answer through.
  var WRAP = "[`'\"*()\\[\\]]*";
  var m;
  var reColon = new RegExp(FILE + "\\s*:\\s*(\\d+)", "g");
  while ((m = reColon.exec(text)) !== null) add(m[1], m[2]);
  var rePhrase = new RegExp(
    "lines?\\s+" + WRAP + "(\\d+)" + WRAP + "(?:\\s*[-–]\\s*\\d+)?\\s+(?:of|in|from|at)\\s+" + WRAP + FILE,
    "gi");
  while ((m = rePhrase.exec(text)) !== null) add(m[2], m[1]);
  return out;
}

// identifiersNear harvests candidate symbol names from the window around a
// citation — these are what we ask the index to locate for real.
function identifiersNear(text, key, radius) {
  var idx = text.indexOf(key);
  if (idx < 0) {
    var bare = key.split(":")[0];
    idx = text.indexOf(bare);
    if (idx < 0) idx = 0;
  }
  var lo = Math.max(0, idx - radius), hi = Math.min(text.length, idx + radius);
  var win = text.slice(lo, hi);
  var re = /[A-Za-z_][A-Za-z0-9_]{2,}/g, m, out = [], seen = {};
  while ((m = re.exec(win)) !== null) {
    var t = m[0];
    if (STOP[t.toLowerCase()] || seen[t]) continue;
    seen[t] = true;
    out.push(t);
  }
  return out;
}

function basename(p) { return p.slice(p.lastIndexOf("/") + 1); }

function sameFile(symPath, cited) {
  if (!symPath) return false;
  if (symPath === cited) return true;
  if (cited.indexOf("/") < 0) return basename(symPath) === cited;
  return symPath.length > cited.length && symPath.slice(-(cited.length + 1)) === "/" + cited;
}

// resolveFile maps a cited path to a real repo path. A bare basename the index
// cannot place is NOT evidence of a bad citation — only a fully-qualified path
// that misses is.
function resolveFile(text, c) {
  var s = summaryOf(c.file);
  if (s) return { path: c.file, summary: s };
  if (c.file.indexOf("/") >= 0) return { notFound: true };
  var idents = identifiersNear(text, c.key, 240), seen = {}, keys = [];
  for (var i = 0; i < idents.length; i++) {
    var syms = symbolsOf(idents[i]);
    for (var j = 0; j < syms.length; j++) {
      var p = syms[j].path;
      if (p && syms[j].name === idents[i] && basename(p) === c.file && !seen[p]) {
        seen[p] = true;
        keys.push(p);
      }
    }
  }
  if (keys.length === 1) {
    var s2 = summaryOf(keys[0]);
    if (s2) return { path: keys[0], summary: s2 };
  }
  return { ambiguous: true };
}

// verifyCitation returns one of:
//   ok           — consistent with the index, or nothing contradicts it
//   bad          — positively contradicted; grounds for rejection
//   inconclusive — cannot be checked; must NOT reject
function verifyCitation(text, c) {
  var rf = resolveFile(text, c);
  if (rf.notFound) {
    return { verdict: "bad", detail: "file does not exist in the repo: " + c.file };
  }
  if (rf.ambiguous) {
    return { verdict: "inconclusive", detail: "cannot resolve '" + c.file + "' to a unique repo path" };
  }
  var lc = (rf.summary && rf.summary.line_count) ? rf.summary.line_count : 0;
  if (lc > 0 && c.line > lc) {
    return {
      verdict: "bad",
      detail: rf.path + " has only " + lc + " lines; cited line " + c.line + " is out of range"
    };
  }
  // Widen in stages. Answers routinely name the symbol in an opening sentence
  // and park the citation in a closing "Location:" line, several hundred chars
  // apart — a tight window alone reports "inconclusive" and lets real drift
  // through. Locality is still preferred: the first radius that anchors wins.
  var ranges = [];
  var radii = [240, 800];
  for (var rr = 0; rr < radii.length && !ranges.length; rr++) {
    var idents = identifiersNear(text, c.key, radii[rr]), checked = 0;
    for (var i = 0; i < idents.length && checked < 8; i++) {
      var syms = symbolsOf(idents[i]), hits = [];
      for (var j = 0; j < syms.length; j++) {
        if (syms[j].name === idents[i] && sameFile(syms[j].path, rf.path)) hits.push(syms[j]);
      }
      if (!hits.length) continue;
      checked++;
      for (var k = 0; k < hits.length; k++) {
        var s = hits[k];
        var lo = s.line;
        var hi = (s.end_line && s.end_line >= s.line) ? s.end_line : s.line;
        ranges.push({ name: s.name, lo: lo, hi: hi });
        // A line inside a symbol's body is a legitimate citation of that symbol.
        if (c.line >= lo && c.line <= hi) {
          return { verdict: "ok", detail: "line " + c.line + " lies within " + s.name + " [" + lo + "-" + hi + "]" };
        }
      }
    }
  }
  if (!ranges.length) {
    return { verdict: "inconclusive", detail: "no anchoring symbol resolved in " + rf.path };
  }
  var near = ranges[0];
  for (var r = 1; r < ranges.length; r++) {
    if (Math.abs(ranges[r].lo - c.line) < Math.abs(near.lo - c.line)) near = ranges[r];
  }
  return {
    verdict: "bad",
    detail: "cited " + c.file + ":" + c.line + " for '" + near.name + "' but it is at " +
      near.lo + (near.hi !== near.lo ? "-" + near.hi : "")
  };
}

function verifyAll(text) {
  var cites = parseCitations(text);
  var bad = [], results = [];
  for (var i = 0; i < cites.length; i++) {
    var v = verifyCitation(text, cites[i]);
    results.push({ key: cites[i].key, verdict: v.verdict, detail: v.detail });
    if (v.verdict === "bad") bad.push(cites[i].key + " — " + v.detail);
  }
  return { citations: cites.length, bad: bad, results: results };
}

// ---- gate entry point ----------------------------------------------------
// Everything is wrapped: a bug in this gate must not reject a good run. Only a
// confirmed-wrong citation calls abort().
(function () {
  var text = (hook && hook.run && hook.run.output) ? String(hook.run.output) : "";
  if (!text) return;

  var verdict;
  try {
    verdict = verifyAll(text);
  } catch (e) {
    print("citation-gate: verification error, passing run through — " + String(e).slice(0, 200));
    return;
  }

  if (!verdict.citations) {
    print("citation-gate: no citations found; nothing to verify");
    return;
  }
  if (!verdict.bad.length) {
    print("citation-gate: " + verdict.citations + " citation(s) checked, none contradicted");
    return;
  }
  abort("citation verification failed (" + verdict.bad.length + " of " +
    verdict.citations + " citations wrong): " + verdict.bad.join(" | "));
})();
