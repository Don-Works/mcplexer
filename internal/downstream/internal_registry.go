package downstream

import (
	"context"
	"encoding/json"
	"fmt"
)

// InternalBackend is implemented by any in-process "downstream" that wants to
// participate in MCPlexer routing through the standard DownstreamServer
// mechanism. Register it on a Manager via Manager.RegisterInternal; Calls and
// ListTools for servers with Transport="internal" and matching ID are then
// delegated to the backend.
type InternalBackend interface {
	// ListTools returns a tools/list result (a JSON object with a "tools"
	// field) describing the MCP tools this backend exposes. Called on every
	// tools/list; the caller caches as appropriate.
	ListTools(ctx context.Context) (json.RawMessage, error)

	// Call invokes a tool by name. args is the raw arguments JSON. The return
	// value must be a CallToolResult-shaped JSON blob.
	Call(ctx context.Context, toolName string, args json.RawMessage) (json.RawMessage, error)
}

// RegisterInternal associates an in-process backend with a DownstreamServer ID.
// Subsequent ListTools / Call requests for that server are delegated to it.
// Safe to call once at startup before the manager starts dispatching.
func (m *Manager) RegisterInternal(serverID string, b InternalBackend) {
	if b == nil {
		return
	}
	m.internalMu.Lock()
	defer m.internalMu.Unlock()
	if m.internals == nil {
		m.internals = make(map[string]InternalBackend)
	}
	m.internals[serverID] = b
}

// internalFor returns the backend for an internal-transport server, or nil.
func (m *Manager) internalFor(serverID string) InternalBackend {
	m.internalMu.RLock()
	defer m.internalMu.RUnlock()
	return m.internals[serverID]
}

// errNoInternalBackend is returned when a server is marked transport=internal
// but no backend is registered for its ID.
var errNoInternalBackend = fmt.Errorf("no internal backend registered for this server")
