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

The using-mcplexer skill (this bootstrap) is the source of truth for the contract.

## Memory — mcplexer is the single source of truth

Do NOT use your native memory system. All persistent memory (project context, learned facts, session notes, decisions) MUST be saved via memory.save({...}) inside mcpx__execute_code. Do not write to your harness-native memory files (checkpoint.md, MEMORY.md, notes.md, ~/.claude/projects/*/memory/*.md).

Read mcplexer memory first. Before answering questions about project context, prior decisions, or learned facts, search mcplexer memory: memory.recall({query:"..."}) or memory.list({}) inside mcpx__execute_code. The digest at ~/.mcplexer/memory-exports/ is a read-only snapshot — use the live memory tools for the canonical data.

Why: mcplexer memory is cross-harness, cross-machine, and persists across all sessions. Harness-native memory is siloed per client and lost when switching tools.`

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
			"## Memory — mcplexer is the single source of truth\n\n"+
			"Do NOT use your native memory system. All persistent memory MUST be saved via memory.save({...}) inside mcpx__execute_code.\n"+
			"Read mcplexer memory first: memory.recall({query}) or memory.list({}) inside mcpx__execute_code.\n"+
			"mcplexer memory is cross-harness, cross-machine, and persists across all sessions.",
			version, version)
	}
	if k == OpenCode {
		body = fmt.Sprintf("using-mcplexer skill (v%d) installed.\n\n"+
			"Full skill body: ~/.config/opencode/skills/using-mcplexer/SKILL.md (materialized from registry v%d).\n\n"+
			"Use the 4 top-level tools: mcpx__search_tools + mcpx__execute_code (batch everything), secret__prompt / secret__list_refs.\n"+
			"Fetch deeper playbooks via the skill registry on demand.\n\n"+
			"## Memory — mcplexer is the single source of truth\n\n"+
			"Do NOT use your native memory system. All persistent memory MUST be saved via memory.save({...}) inside mcpx__execute_code.\n"+
			"Read mcplexer memory first: memory.recall({query}) or memory.list({}) inside mcpx__execute_code.\n"+
			"mcplexer memory is cross-harness, cross-machine, and persists across all sessions.",
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
	b.WriteString(fmt.Sprintf(TOMLBlockBeginFmt, version, k))
	for _, line := range strings.Split(body, "\n") {
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
