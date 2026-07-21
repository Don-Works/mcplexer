package downstream

// InstanceKey uniquely identifies a downstream instance.
//
// SessionID is empty for the default "shared" lifecycle: one process per
// (ServerID, AuthScopeID) multiplexed across every agent session. It is
// populated only for servers that opt into per-session isolation
// (ShouldIsolatePerSession — browser-automation downstreams), so each
// logical agent gets its own process. A browser process is stateful (one
// live page, cookies, navigation), so sharing it lets one agent navigate
// another agent's tab out from under it; the SessionID dimension gives each
// agent its own browser instead.
type InstanceKey struct {
	ServerID    string
	AuthScopeID string
	// SessionID isolates browser-automation instances per logical agent.
	// Empty = shared (the default for every non-browser downstream).
	SessionID string
}

// InstanceState represents the lifecycle state of a downstream process.
type InstanceState int

const (
	StateStopped InstanceState = iota
	StateStarting
	StateReady
	StateBusy
	StateIdle
	StateStopping
	StateRestarting
)

func (s InstanceState) String() string {
	switch s {
	case StateStopped:
		return "stopped"
	case StateStarting:
		return "starting"
	case StateReady:
		return "ready"
	case StateBusy:
		return "busy"
	case StateIdle:
		return "idle"
	case StateStopping:
		return "stopping"
	case StateRestarting:
		return "restarting"
	default:
		return "unknown"
	}
}

// InstanceInfo describes a running downstream instance for status reporting.
type InstanceInfo struct {
	Key   InstanceKey
	State InstanceState
}
