// Package skills embeds the MCPlexer skill markdown files for install-time
// delivery. The canonical source is this directory; the CLI setup command
// writes the content to ~/.mcplexer/skills/ and symlinks it into Claude Code
// and OpenCode command directories so agents see the current skill docs for
// the server they're actually running against.
package skills

import _ "embed"

//go:embed agent-mesh.md
var AgentMesh string

//go:embed cross-machine-test.md
var CrossMachineTest string

//go:embed mcplexer-in-cmux.md
var McplexerInCmux string

//go:embed token-preserving-delegation.md
var TokenPreservingDelegation string
