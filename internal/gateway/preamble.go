package gateway

import _ "embed"

//go:embed preamble.md
var workerPreamble string

// WorkerPreamble returns the system-prompt prefix mcplexer injects into every
// scheduled worker run. It tells the worker what environment it's in, that
// it sees only mcpx__search_tools + mcpx__execute_code, and where the rest
// (mesh, secrets, memory) lives. The content is owned by the gateway because
// it describes mcplexer-the-environment, not the worker package.
func WorkerPreamble() string { return workerPreamble }
