package agentrules

// contentV10 adds the code-index family: the indexer only pays off if agents
// know to ask it first, so the block names the headline calls and the
// ask-before-reading contract.
func contentV10() string {
	return contentV9() + `

### Code index — ask the index before reading the repo

The gateway ships a built-in local codebase indexer (` + "`index__*`" + `, per-workspace symbol map + import graph + test ownership). Inside ` + "`mcpx__execute_code`" + `: ` + "`index.context({query, budget_tokens})`" + ` returns a ranked, token-budgeted pack of the right files (summaries, key symbols with line numbers, owning tests, recent commits) — call it FIRST for any "what's relevant to this task / where do I look" question instead of reading the repo wholesale. Also: ` + "`index.symbols`" + ` (find a function/type/class by words — camelCase is word-split), ` + "`index.deps`" + ` (imports + importers = blast radius), ` + "`index.tests_for`" + `, ` + "`index.summary`" + `, ` + "`index.recent_changes`" + `, and ` + "`index.map_failure`" + ` (paste a failing test or stack trace → ranked candidate files). Queries auto-build the index on first use; run ` + "`index.build`" + ` after big edits or branch switches, ` + "`index.status`" + ` to check freshness.`
}
