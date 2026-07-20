package gateway

import "strings"

// MCP server names clients use when qualifying tools/list entries as
// {server}__{tool}. Must stay in sync with internal/install serverName.
const (
	mcpServerName       = "mcplexer"
	legacyMCPServerName = "mx"
)

// HarnessProfile describes how an MCP client surfaces tools from a named server.
type HarnessProfile int

const (
	// HarnessDirect clients call tools exactly as tools/list advertises them
	// (Claude Code, Codex, OpenCode, …).
	HarnessDirect HarnessProfile = iota
	// HarnessServerPrefixed clients qualify every tool as {server}__{name}.
	// When our slim-surface tools already contain "__" (mcpx__execute_code),
	// the qualified name becomes mcplexer__mcpx__execute_code and Grok skips
	// registration entirely ("qualified name contains '__' more than once").
	HarnessServerPrefixed
)

// slimSurfaceHarnessAliases maps single-segment tool names — what a
// server-prefixed harness sees after qualification — back to canonical
// gateway names. Only the slim-surface keep-list is aliased; admin and
// mesh tools stay off the static list.
var slimSurfaceHarnessAliases = map[string]string{
	"execute_code": "mcpx__execute_code",
	"search_tools": "mcpx__search_tools",
	"prompt":       "secret__prompt",
	"list_refs":    "secret__list_refs",
	"retrieve":     "mcpx__retrieve",
}

var canonicalToHarnessAlias map[string]string

func init() {
	canonicalToHarnessAlias = make(map[string]string, len(slimSurfaceHarnessAliases))
	for alias, canonical := range slimSurfaceHarnessAliases {
		canonicalToHarnessAlias[canonical] = alias
	}
}

// harnessProfileForClient maps initialize.clientInfo.name to a harness profile.
func harnessProfileForClient(clientType string) HarnessProfile {
	lower := strings.ToLower(strings.TrimSpace(clientType))
	if lower == "" {
		return HarnessDirect
	}
	// Pi (pi.dev / Earendil) is MCP-skeptical by design and reaches the
	// gateway either through its native mcplexer extension (a CLI shim that
	// speaks raw MCP tools/call using the advertised names verbatim) or via
	// the generic pi-mcp-adapter proxy tool, which invokes discovered tool
	// names directly. Neither path concatenates the server name with a "__"
	// separator, so Pi never produces the mcplexer__mcpx__execute_code
	// double-"__" pathology that defines HarnessServerPrefixed — it is a
	// HarnessDirect client and must be matched before the prefixed list so a
	// future "pi-cursor"-style name can't be misclassified.
	if isPiHarness(lower) {
		return HarnessDirect
	}
	// Clients known to prefix the configured MCP server name onto tool names.
	for _, needle := range []string{
		"grok", "cursor", "windsurf", "gemini", "picoclaw",
	} {
		if strings.Contains(lower, needle) {
			return HarnessServerPrefixed
		}
	}
	return HarnessDirect
}

// isPiHarness reports whether a lower-cased clientInfo.name belongs to the Pi
// coding agent (pi.dev, originally badlogic/pi-mono, now earendil-works/pi,
// also published as @mariozechner/pi-coding-agent). Pi's clientInfo.name has
// surfaced as "pi", "pi-coding-agent", "@mariozechner/pi-coding-agent",
// "pi.dev", or carried the "earendil" org marker, so all of those are matched.
//
// Matching is intentionally token/marker-bounded so it does NOT swallow
// unrelated names that merely contain the letters "pi". It must STILL return
// false for: "raspberry-pi" (bare trailing "pi" token is NOT matched),
// "picoclaw", "copilot", "openai", "cursor", "pip", "pixel".
func isPiHarness(lower string) bool {
	if strings.Contains(lower, "earendil") {
		return true
	}
	// "pi-coding" anywhere catches @mariozechner/pi-coding-agent and the like
	// (the org/scope prefix means a bare "pi-" HasPrefix check would miss it).
	if strings.Contains(lower, "pi-coding") {
		return true
	}
	// Exact bare token.
	if lower == "pi" {
		return true
	}
	// A name that BEGINS with a "pi" token: pi-, pi_, pi/, "pi " (space), or
	// "pi." (pi.dev). The trailing token-boundary char keeps "picoclaw",
	// "pip", and "pixel" out (their 3rd char is a letter, not a boundary).
	for _, prefix := range []string{"pi-", "pi ", "pi_", "pi/", "pi."} {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return false
}

func (h *handler) harnessProfile() HarnessProfile {
	if h == nil || h.sessions == nil {
		return HarnessDirect
	}
	return harnessProfileForClient(h.sessions.clientType())
}

// applyHarnessToolListNames rewrites slim-surface keeper names for harnesses
// that prefix the MCP server name. Claude/Codex sessions are untouched.
func applyHarnessToolListNames(profile HarnessProfile, tools []Tool) []Tool {
	if profile != HarnessServerPrefixed {
		return tools
	}
	out := make([]Tool, len(tools))
	for i, t := range tools {
		out[i] = t
		if alias, ok := canonicalToHarnessAlias[t.Name]; ok {
			out[i].Name = alias
		}
	}
	return out
}

// resolveHarnessToolName normalizes tool names arriving from server-prefixed
// harnesses (and their stale qualified caches) back to canonical gateway names.
// Safe to call for every client — direct harnesses pass canonical names through.
func resolveHarnessToolName(name string) string {
	name = normalizeBuiltinName(name)
	stripped := stripMCPServerPrefixes(name)
	if canonical, ok := slimSurfaceHarnessAliases[stripped]; ok {
		return canonical
	}
	if stripped == name {
		return name
	}
	// If stripping left a bare name (no namespace), the original carried a
	// server prefix that IS the tool's own namespace — e.g.
	// mcplexer__list_workspaces (admin tool) or mx__delete_route (legacy
	// prefix). Map back to the mcplexer__ canonical form so admin gate,
	// routing, and IsAdminTool see the correct namespace.
	if strings.Contains(stripped, "__") {
		return stripped
	}
	return mcpServerName + "__" + stripped
}

// harnessKeyForClientInfo maps initialize.clientInfo.name (from the MCP
// handshake) to the stable harness key used by /api/v1/setup/* and
// harness-sync receipts. Best-effort; unknown clients are ignored.
func harnessKeyForClientInfo(name string) (string, bool) {
	lower := strings.ToLower(strings.TrimSpace(name))
	if lower == "" {
		return "", false
	}
	switch {
	case strings.Contains(lower, "claude"):
		return "claude", true
	case strings.Contains(lower, "codex"):
		return "codex", true
	case strings.Contains(lower, "opencode"):
		return "opencode", true
	case strings.Contains(lower, "gemini"):
		return "gemini", true
	case strings.Contains(lower, "grok"):
		return "grok", true
	case strings.Contains(lower, "mimocode") || strings.Contains(lower, "mimo"):
		return "mimo", true
	case isPiHarness(lower):
		return "pi", true
	}
	return "", false
}

func stripMCPServerPrefixes(name string) string {
	for {
		changed := false
		for _, prefix := range []string{mcpServerName + "__", legacyMCPServerName + "__"} {
			if after, ok := strings.CutPrefix(name, prefix); ok {
				name = after
				changed = true
				break
			}
		}
		if !changed {
			return name
		}
	}
}

// buildCodeModeInstructionsForClient returns the initialize instructions for a
// connecting client, chosen by clientInfo.name.
//
// Pi fronts local models (Qwen-class, ~100k window) where the handshake
// instructions are a fixed tax on a small budget, so it gets a compact variant
// carrying only the rules a snippet cannot be written without. Every other
// harness gets the full text unchanged — this is a Pi-only narrowing, not a
// global trim.
func buildCodeModeInstructionsForClient(clientName string, meshEnabled bool) string {
	if isPiHarness(strings.ToLower(strings.TrimSpace(clientName))) {
		return buildCompactCodeModeInstructions(meshEnabled)
	}
	return buildCodeModeInstructions(harnessProfileForClient(clientName), meshEnabled)
}

// buildCompactCodeModeInstructions is the reduced handshake for local-model
// harnesses. It keeps the five rules a worker cannot write a correct snippet
// without — the `<namespace>.<tool>(args)` call form, synchronous/no-await
// semantics, auto-unwrapped results, batch-into-one-snippet, and never print
// raw responses — plus the null-on-failure contract of parallel(), and drops
// the discovery prose, browser routing, skill-registry naming, and
// context-eviction advice that the full variant spends most of its bytes on.
// Pi always uses canonical (HarnessDirect) tool names.
func buildCompactCodeModeInstructions(meshEnabled bool) string {
	var b strings.Builder
	b.WriteString("# MCPlexer Code Mode\n\n")
	b.WriteString("tools/list shows only `mcpx__execute_code`, `mcpx__search_tools`, " +
		"`secret__prompt`, `secret__list_refs`, and `mcpx__retrieve`. Everything else — " +
		"task, mesh, memory, index, skills, downstream MCP servers — is called INSIDE " +
		"`mcpx__execute_code`. Find it with `mcpx__search_tools` (`detail: \"full\"` for " +
		"signatures), or `help()` / `help('memory')` inside a snippet.\n\n")
	b.WriteString("Call form — JavaScript, `<namespace>.<tool>(args)`:\n\n")
	b.WriteString("```js\n")
	b.WriteString("const snap = customer.get_customer_snapshot({ slug: \"acme\" });\n")
	b.WriteString("print(snap.name, snap.tier);\n")
	b.WriteString("```\n\n")
	b.WriteString("Rules:\n")
	b.WriteString("- Calls are synchronous — no `await`. Use `sleep(ms)` for poll loops.\n")
	b.WriteString("- Results are **auto-unwrapped**: read `result.id`, never " +
		"`JSON.parse(result.content[0].text)`.\n")
	b.WriteString("- ALWAYS batch related calls into ONE snippet — that is the point of Code Mode.\n")
	b.WriteString("- NEVER print raw responses; filter and summarize first.\n")
	b.WriteString("- `parallel()` returns null entries for failed calls instead of throwing — " +
		"check for null.\n")
	if meshEnabled {
		b.WriteString("- Other agents can message you: `mesh.receive(...)` / `mesh.send(...)` " +
			"inside `mcpx__execute_code`.\n")
	}
	return b.String()
}

// buildCodeModeInstructions returns initialize instructions tailored to the
// connecting harness. Server-prefixed clients see mcplexer__* qualified names.
func buildCodeModeInstructions(profile HarnessProfile, meshEnabled bool) string {
	var b strings.Builder
	b.WriteString("# MCPlexer Code Mode\n\n")

	switch profile {
	case HarnessServerPrefixed:
		b.WriteString("This MCP server is registered as **mcplexer**. Your harness qualifies " +
			"tool names as `mcplexer__<tool>`. Use your harness search/discovery to find " +
			"`mcplexer`, then call:\n\n")
		b.WriteString("1. `mcplexer__search_tools` — discover every callable function " +
			"(downstream MCP servers + built-in mesh/memory/secret surfaces)\n")
		b.WriteString("2. `mcplexer__execute_code` — batch downstream calls in one JavaScript snippet\n")
		b.WriteString("3. `mcplexer__prompt` / `mcplexer__list_refs` — secret handling\n")
		b.WriteString("4. `mcplexer__retrieve` — expand a `[[ccr key=...]]` compression marker\n\n")
	default:
		b.WriteString("This server runs in Code Mode. tools/list returns ONLY the built-in " +
			"`mcpx__execute_code`, `mcpx__search_tools`, `secret__prompt`, and " +
			"`secret__list_refs`, plus `mcpx__retrieve` for compression markers. It does NOT list downstream tools or built-in " +
			"namespaces such as task, mesh, memory, customer, linear, or github, " +
			"even though many are routed to your session.\n\n")
		b.WriteString("Discover what's available with `mcpx__search_tools`. Pass `detail: \"full\"` " +
			"to get TypeScript signatures for the exact arguments and return types. " +
			"Fetch skills from the registry only when needed, starting with " +
			"`mcpx.skill_search(...)` and `mcpx.skill_get({name:\"using-mcplexer\"})` " +
			"inside `mcpx__execute_code`.\n\n")
	}

	b.WriteString("For browser or browsing tasks, search for `brw`/browser tools first; " +
		"if the `brw` namespace is available, prefer it as the mcplexer browser-control " +
		"surface. For non-trivial browser workflows, fetch an installed browser skill " +
		"from the registry, such as `generic-browser-operator`, `playwright-browser`, " +
		"or `cmux-browser`.\n\n")

	b.WriteString("To call a downstream tool, invoke ")
	if profile == HarnessServerPrefixed {
		b.WriteString("`mcplexer__execute_code`")
	} else {
		b.WriteString("`mcpx__execute_code`")
	}
	b.WriteString(" with a JavaScript snippet that calls the function directly:\n\n")
	b.WriteString("```js\n")
	b.WriteString("const snap = customer.get_customer_snapshot({ slug: \"acme\" });\n")
	b.WriteString("// snap is the parsed result — access fields directly, no JSON.parse needed.\n")
	b.WriteString("print(snap.name, snap.tier);\n")
	b.WriteString("```\n\n")
	b.WriteString("Tools are exposed as `<namespace>.<tool_name>(args)`. The sandbox is a full " +
		"JavaScript environment for pure computation (math, parsing, dedupe, date arithmetic), " +
		"with or without tool calls. Calls are synchronous (no await); `sleep(ms)` clamps each " +
		"call to 60000ms — use for poll loops inside the execution timeout, e.g. " +
		"`while(!done){ sleep(2000); ... }`. Results are **auto-unwrapped**: when a tool " +
		"returns the MCP envelope `{content:[{type:'text',text:'...'}],isError:false}` and the " +
		"text is JSON, you get the parsed object directly — read `result.id`, never " +
		"`JSON.parse(result.content[0].text)`. For plain-text content you get the raw string; " +
		"isError throws. `parallel()` returns null entries for failed calls and does not throw — " +
		"check for null; successful entries unwrap the same way. ALWAYS batch related calls into " +
		"one execute_code invocation — that's the whole point of Code Mode. NEVER print raw API " +
		"responses; filter and summarize first.\n\n")
	b.WriteString("Skill registry calls live under `mcpx` (`mcpx.skill_search`, `mcpx.skill_get`). " +
		"The separate `skill` namespace is run telemetry only. Compression markers can be expanded " +
		"with the top-level retrieve tool or `mcpx.retrieve({key})` inside code mode.\n\n")
	b.WriteString("Unsure what's callable? Call `help()` to list namespaces, or `help('memory')` for a " +
		"namespace's tool signatures — no search round-trip. Typos and nested-path mistakes " +
		"(e.g. `mcpx.memory.recall()`) return a did-you-mean naming the correct flat call form.\n\n")
	b.WriteString("Tool results may be cleared later in the conversation — write down anything " +
		"you need to remember.")

	if meshEnabled {
		b.WriteString("\n\n## Agent Mesh\n\n")
		if profile == HarnessServerPrefixed {
			b.WriteString("This server supports inter-agent communication. Other agents connected to " +
				"this gateway may send you messages; pending messages are appended to tool results " +
				"automatically. Downstream mesh tools are callable via `mcplexer__execute_code` " +
				"after `mcplexer__search_tools` — they are not in the static tools/list surface.")
		} else {
			b.WriteString("This server supports inter-agent communication. Other agents connected to " +
				"this gateway may send you messages; pending messages are appended to tool results " +
				"automatically. Mesh is reached dynamically: search with `mcpx__search_tools`, then " +
				"call functions such as `mesh.receive(...)` and `mesh.send(...)` inside " +
				"`mcpx__execute_code`.")
		}
	}
	return b.String()
}
