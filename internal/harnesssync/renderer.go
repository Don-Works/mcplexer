package harnesssync

import (
	"fmt"
	"strings"
)

// usingMcplexerPointer is the slim managed-block body for non-Claude
// harnesses. The canonical full body lives in
// internal/skillregistry/seeds/using-mcplexer.md and is materialized to
// ~/.claude/skills/using-mcplexer/SKILL.md on Claude install only.
const usingMcplexerPointer = `Use the 4 top-level tools (mcpx__search_tools + mcpx__execute_code for batch, secret__prompt/list_refs).

For the full contract, fetch mcpx.skill_get({name:"using-mcplexer"}) directly. Use mcpx.skill_search only for unknown/deeper playbooks: mcplexer-features / mcplexer-tasks / agent-mesh / token-preserving-delegation.

Start mcpx__search_tools in summary or exact-tool mode; avoid broad detail:"full" searches.

Code index: ask the index BEFORE reading the repo. Inside mcpx__execute_code, index.context({query, budget_tokens}) returns a ranked, token-budgeted pack with source snippets; index.search({query, limit}) finds implementation/behavior in citation-ready source chunks; index.symbols finds definitions (camelCase word-split); index.map_failure maps a pasted failure. Lexical search always works; semantic search is optional, explicit, and local-only.

Browser/browser-control tasks: assume brw may be installed. Search brw/browser tools first and prefer the brw namespace when available; for non-trivial browser workflows fetch an installed browser skill (for example generic-browser-operator, playwright-browser, or cmux-browser) with mcpx.skill_search/get.

The using-mcplexer skill (this bootstrap) is the source of truth for the contract.

## Memory — prefer the gateway store

mcplexer memory (memory.save / memory.recall inside mcpx__execute_code) is cross-harness, cross-machine, and survives every session; harness-native memory files are siloed per client. Recall when past sessions may have settled a question; save decisions with rationale, preferences, and project facts not derivable from the repo. Knowledge that should outlive this client belongs in the gateway store, not in harness-local memory files. The digest at ~/.mcplexer/memory-exports/ is a read-only snapshot — the live memory tools are canonical.`

// Render produces the managed block content (markers + body) for the
// given harness. The caller writes it into TargetPath. For claude and
// opencode the returned block is a slim pointer to the SKILL.md sidecar;
// the full registry seed body is written separately by Install.
func Render(k HarnessKey, version int) string {
	body := usingMcplexerPointer
	if k == Claude {
		body = fmt.Sprintf("using-mcplexer skill (v%d) installed.\n\n"+
			"Full skill body: ~/.claude/skills/using-mcplexer/SKILL.md (materialized from registry v%d).\n\n"+
			"Use the 4 top-level tools: mcpx__search_tools + mcpx__execute_code (batch everything), secret__prompt / secret__list_refs.\n"+
			"Fetch deeper playbooks via the skill registry on demand.\n\n"+
			"Code index: ask it BEFORE reading the repo — index.context({query, budget_tokens}) returns a ranked pack with snippets; index.search({query, limit}) finds citation-ready implementation source; index.symbols finds definitions; index.map_failure maps failures. Lexical search is always available; semantics are explicit and local-only.\n\n"+
			"Browser work: search for brw/browser tools and prefer brw when installed; fetch an installed browser skill such as generic-browser-operator, playwright-browser, or cmux-browser for non-trivial workflows.\n\n"+
			"## Memory — prefer the gateway store\n\n"+
			"mcplexer memory (memory.save / memory.recall inside mcpx__execute_code) is cross-harness, cross-machine, and survives every session; harness-local memory files are siloed per client.\n"+
			"Recall when past sessions may have settled a question; save decisions with rationale, preferences, and project facts not derivable from the repo.\n"+
			"Knowledge that should outlive this client belongs in the gateway store.",
			version, version)
	}
	if k == OpenCode {
		body = fmt.Sprintf("using-mcplexer skill (v%d) installed.\n\n"+
			"Full skill body: ~/.config/opencode/skills/using-mcplexer/SKILL.md (materialized from registry v%d).\n\n"+
			"Use the 4 top-level tools: mcpx__search_tools + mcpx__execute_code (batch everything), secret__prompt / secret__list_refs.\n"+
			"Fetch deeper playbooks via the skill registry on demand.\n\n"+
			"Code index: ask it BEFORE reading the repo — index.context({query, budget_tokens}) returns a ranked pack with snippets; index.search({query, limit}) finds citation-ready implementation source; index.symbols finds definitions; index.map_failure maps failures. Lexical search is always available; semantics are explicit and local-only.\n\n"+
			"Browser work: search for brw/browser tools and prefer brw when installed; fetch an installed browser skill such as generic-browser-operator, playwright-browser, or cmux-browser for non-trivial workflows.\n\n"+
			"## Memory — prefer the gateway store\n\n"+
			"mcplexer memory (memory.save / memory.recall inside mcpx__execute_code) is cross-harness, cross-machine, and survives every session; harness-local memory files are siloed per client.\n"+
			"Recall when past sessions may have settled a question; save decisions with rationale, preferences, and project facts not derivable from the repo.\n"+
			"Knowledge that should outlive this client belongs in the gateway store.",
			version, version)
	}
	if k == Grok {
		return renderTOMLCommentBlock(k, version, body)
	}
	begin := fmt.Sprintf(BlockBeginFmt, version, k)
	return begin + body + "\n\n" + BlockEnd
}

func renderTOMLCommentBlock(k HarnessKey, version int, body string) string {
	var b strings.Builder
	fmt.Fprintf(&b, TOMLBlockBeginFmt, version, k)
	for line := range strings.SplitSeq(body, "\n") {
		if line == "" {
			b.WriteString("#\n")
			continue
		}
		b.WriteString("# ")
		b.WriteString(line)
		b.WriteByte('\n')
	}
	b.WriteString("#\n")
	b.WriteString(TOMLBlockEnd)
	return b.String()
}

// RenderedHash returns the content hash used for bootstrap receipts
// (normalized body inside the markers for the version).
func RenderedHash(k HarnessKey, version int) string {
	return contentHash(Render(k, version))
}
