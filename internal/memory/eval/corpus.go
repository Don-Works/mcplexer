// Package eval is an in-domain retrieval-quality CI gate for the memory
// subsystem. It seeds a small *labeled* fixture corpus into a real on-disk
// SQLite store, drives memory.Service.Recall (FTS5-only via the default
// NoopEmbedder — no network, no API key), and computes recall@k, nDCG@k,
// MRR plus ingest→retrieve latency percentiles. The companion test asserts
// thresholds so a ranking/recall regression fails the build, and verifies
// two lifecycle invariants (supersession + forget) on the same store.
//
// Why fixtures live in code, not testdata: the labels (query → relevant
// memory ids) are the whole point of the gate, and keeping them adjacent to
// the corpus makes the relevance contract reviewable in one diff. The
// corpus is deliberately small + opinionated so FTS-only retrieval has a
// fighting chance and the thresholds stay meaningful.
package eval

import "time"

// FixtureMemory is one labeled document in the eval corpus. Key is a stable
// human-readable handle used by queries to express relevance — it is mapped
// to the store-assigned ULID at seed time, so the labels never hard-code an
// id that changes per run.
//
// UpdatedAt/CreatedAt are the ADVERSARIAL knob: a zero value means "seed via
// memory.Service.Write and let the store stamp now()" (the original,
// everything-is-fresh behaviour), while a non-zero value backdates the row so
// the gate can exercise the recency term in ranking. See Harness.seed.
// Backdating is possible because (*sqlite.DB).WriteMemory only defaults
// CreatedAt/UpdatedAt/TValidStart when they are zero — it honours whatever the
// caller supplies. The Service.Write path cannot express this (registry.go
// builds the store.MemoryEntry literal without timestamp fields), which is why
// the harness drops to the store for backdated rows.
type FixtureMemory struct {
	Key     string // stable label handle (e.g. "editor-pref")
	Name    string
	Content string
	Tags    []string

	// UpdatedAt, when non-zero, is written verbatim to the row (and mirrored
	// onto created_at / t_valid_start) instead of time.Now().
	UpdatedAt time.Time
	// Pinned mirrors store.MemoryEntry.Pinned so a fixture can exercise the
	// pinnedBoost term. Defaults false for every generated document.
	Pinned bool
}

// FixtureQuery is one labeled retrieval probe. RelevantKeys lists the
// FixtureMemory.Key values that SHOULD surface for Query — graded relevance
// is binary here (1.0 if relevant, 0.0 otherwise), which keeps nDCG honest
// for a fixture set this size.
type FixtureQuery struct {
	Query        string
	RelevantKeys []string
}

// Corpus bundles the labeled documents + probes the harness runs.
type Corpus struct {
	Memories []FixtureMemory
	Queries  []FixtureQuery
}

// DefaultCorpus is the in-domain fixture set. The documents are realistic
// agent-memory rows (preferences, project facts, debugging notes,
// anti-patterns); the queries are phrased the way an agent would recall
// them. Relevance is hand-labeled. Distractors that share incidental tokens
// are included on purpose so the metrics measure ranking, not just presence.
func DefaultCorpus() Corpus {
	return Corpus{
		Memories: []FixtureMemory{
			{
				Key:     "editor-pref",
				Name:    "preferred-editor",
				Content: "Operator prefers neovim with telescope and treesitter for editing Go and TypeScript.",
				Tags:    []string{"editor", "preference", "neovim"},
			},
			{
				Key:     "shell-pref",
				Name:    "preferred-shell",
				Content: "The default interactive shell on this machine is zsh, configured via oh-my-zsh.",
				Tags:    []string{"shell", "preference", "zsh"},
			},
			{
				Key:     "db-choice",
				Name:    "database-engine",
				Content: "mcplexer stores all state in SQLite using modernc.org/sqlite so there is no CGO dependency.",
				Tags:    []string{"database", "sqlite", "architecture"},
			},
			{
				Key:     "secrets-rule",
				Name:    "secret-handling",
				Content: "Never embed plaintext secrets. Pass secret reference strings as tool arguments and the gateway substitutes plaintext at dispatch time.",
				Tags:    []string{"security", "secrets", "anti-pattern"},
			},
			{
				Key:     "shell-guard",
				Name:    "bash-shell-guard",
				Content: "The bash shell guard hard-blocks any command containing a pipe, semicolon, ampersand, backtick, or newline metacharacter.",
				Tags:    []string{"bash", "guard", "anti-pattern"},
			},
			{
				Key:     "payment-debug",
				Name:    "payment-flow-debugging",
				Content: "We debugged the payment flow last sprint: the retry loop double-charged because the idempotency key was regenerated on each attempt.",
				Tags:    []string{"debugging", "payment", "idempotency"},
			},
			{
				Key:     "fts-floor",
				Name:    "memory-fts-floor",
				Content: "Memory recall always runs FTS5 as the floor; vector KNN only augments ranking once an embedding provider is configured.",
				Tags:    []string{"memory", "fts5", "retrieval"},
			},
			{
				Key:     "task-lease",
				Name:    "task-lease-lifecycle",
				Content: "Tasks use a five minute lease; the gateway demotes a working task back to open when the owning session disconnects or the lease lapses.",
				Tags:    []string{"task", "lease", "lifecycle"},
			},
			{
				Key:     "worktree-hygiene",
				Name:    "worktree-hygiene",
				Content: "Git worktrees are ephemeral scratch space: land the branch, confirm it is on main, then remove the worktree in the same session.",
				Tags:    []string{"git", "worktree", "hygiene"},
			},
			{
				Key:     "go-style",
				Name:    "go-conventions",
				Content: "Go code in this repo is idiomatic with explicit error handling, sentinel errors, table-driven tests, and a 300 line per file cap.",
				Tags:    []string{"go", "conventions", "style"},
			},
		},
		Queries: []FixtureQuery{
			{Query: "which text editor does the operator use", RelevantKeys: []string{"editor-pref"}},
			{Query: "what shell is configured", RelevantKeys: []string{"shell-pref"}},
			{Query: "what database engine does mcplexer use", RelevantKeys: []string{"db-choice"}},
			{Query: "how should secrets be handled", RelevantKeys: []string{"secrets-rule"}},
			{Query: "why was a bash command blocked metacharacter", RelevantKeys: []string{"shell-guard"}},
			{Query: "payment idempotency double charge bug", RelevantKeys: []string{"payment-debug"}},
			{Query: "does memory recall use FTS5 vector", RelevantKeys: []string{"fts-floor"}},
			{Query: "task lease demote disconnect", RelevantKeys: []string{"task-lease"}},
			{Query: "git worktree cleanup hygiene", RelevantKeys: []string{"worktree-hygiene"}},
			{Query: "go style conventions table driven tests", RelevantKeys: []string{"go-style"}},
		},
	}
}

// relevantSet builds a membership set of relevant keys for one query.
func (q FixtureQuery) relevantSet() map[string]struct{} {
	m := make(map[string]struct{}, len(q.RelevantKeys))
	for _, k := range q.RelevantKeys {
		m[k] = struct{}{}
	}
	return m
}
