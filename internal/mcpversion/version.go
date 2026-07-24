// Package mcpversion defines the MCP protocol revisions that MCPlexer can
// negotiate. Keeping this policy in a dependency-free package prevents the
// data-plane gateway, control server, and downstream clients from drifting.
package mcpversion

import (
	"errors"
	"fmt"
	"strings"
)

const (
	Version20241105 = "2024-11-05"
	Version20250326 = "2025-03-26"
	Version20250618 = "2025-06-18"
	Version20251125 = "2025-11-25"

	// Latest is the revision MCPlexer proposes when acting as an MCP client
	// and selects when a connecting client proposes an unsupported revision.
	Latest = Version20251125
)

var (
	// ErrUnsupported is returned when a peer selects a revision MCPlexer does
	// not implement.
	ErrUnsupported = errors.New("unsupported MCP protocol version")

	supported = []string{
		Version20241105,
		Version20250326,
		Version20250618,
		Version20251125,
	}
)

// Supported returns the MCP revisions implemented by MCPlexer, oldest first.
// The returned slice is a copy and may be safely modified by the caller.
func Supported() []string {
	out := make([]string, len(supported))
	copy(out, supported)
	return out
}

// IsSupported reports whether version is one of MCPlexer's explicit protocol
// revisions. Empty, draft, malformed, and future revisions are unsupported.
func IsSupported(version string) bool {
	for _, candidate := range supported {
		if version == candidate {
			return true
		}
	}
	return false
}

// Select chooses the server-side revision for an initialize request. MCP
// lifecycle negotiation requires a server to echo a supported client proposal;
// otherwise it selects another revision it supports, preferably its latest.
func Select(requested string) string {
	if IsSupported(requested) {
		return requested
	}
	return Latest
}

// ValidateSelected checks the revision selected by an MCP server. A client
// must terminate initialization when it cannot support the selected revision.
func ValidateSelected(selected string) error {
	if IsSupported(selected) {
		return nil
	}
	if selected == "" {
		selected = "<empty>"
	}
	return fmt.Errorf(
		"%w %q (supported: %s)",
		ErrUnsupported,
		selected,
		strings.Join(supported, ", "),
	)
}
