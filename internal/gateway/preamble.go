package gateway

import _ "embed"

//go:embed preamble.md
var workerPreamble string

//go:embed preamble_cli.md
var workerPreambleCLI string

// WorkerPreamble returns the system-prompt prefix mcplexer injects into every
// scheduled worker run driven by an API-provider adapter. It tells the worker
// what environment it's in, that it sees only mcpx__search_tools +
// mcpx__execute_code, and where the rest (mesh, secrets, memory) lives. The
// content is owned by the gateway because it describes mcplexer-the-environment,
// not the worker package.
func WorkerPreamble() string { return workerPreamble }

// WorkerPreambleCLI returns the preamble for CLI-backed workers
// (models.IsCLIProvider: pi_cli, claude_cli, opencode_cli, …).
//
// Those adapters shell out to a coding agent that runs its own loop with its
// own native tools and DISCARDS the tool list the runner assembled — pi_cli
// drops req.Tools entirely (internal/models/pi_cli.go). So the API-provider
// preamble's central promise, "your top-level tool surface is exactly
// mcpx__search_tools and mcpx__execute_code", is simply false for them: it
// bills every CLI worker ~210 tokens to be told something untrue about its own
// environment, which is worse than saying nothing. This variant states only
// what actually holds — you're in your own harness; mcplexer's namespaces are
// reachable if its MCP server is wired in; nothing but memory/tasks/commits
// survives the run.
func WorkerPreambleCLI() string { return workerPreambleCLI }
