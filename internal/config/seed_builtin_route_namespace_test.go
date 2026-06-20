package config

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestDefaultBuiltinRoutesMatchServerNamespace enforces the invariant the
// data__/kv__ "no matching route" bug violated: every default allow rule that
// targets an internal builtin server whose ToolNamespace is set must only match
// tools prefixed with that namespace. The routing namespace guard
// (routing.matchRoute) silently skips a rule whose downstream namespace doesn't
// prefix the tool, so a rule like data__* -> mcpx-builtin (ns "mcpx") would seed
// fine yet never dispatch. Asserting it here, against the real seed tables,
// catches the mismatch at build time instead of from a live session.
func TestDefaultBuiltinRoutesMatchServerNamespace(t *testing.T) {
	serverNS := make(map[string]string, len(defaultDownstreamServers))
	internal := make(map[string]bool, len(defaultDownstreamServers))
	for _, s := range defaultDownstreamServers {
		serverNS[s.ID] = s.ToolNamespace
		internal[s.ID] = s.Transport == "internal"
	}

	for _, r := range defaultRouteRules {
		if r.Policy != "allow" || r.DownstreamServerID == "" {
			continue
		}
		ns, ok := serverNS[r.DownstreamServerID]
		if !ok {
			t.Errorf("rule %q targets unknown downstream server %q", r.ID, r.DownstreamServerID)
			continue
		}
		// Only internal builtins carry the dispatch-by-namespace contract; the
		// guard only fires when the server has a namespace set.
		if !internal[r.DownstreamServerID] || ns == "" {
			continue
		}
		var patterns []string
		if err := json.Unmarshal(r.ToolMatch, &patterns); err != nil {
			t.Errorf("rule %q has unparseable ToolMatch: %v", r.ID, err)
			continue
		}
		for _, p := range patterns {
			if !strings.HasPrefix(p, ns+"__") {
				t.Errorf("rule %q routes pattern %q to server %q (ns %q), but the "+
					"routing namespace guard requires the pattern to start with %q__ "+
					"— it would never dispatch", r.ID, p, r.DownstreamServerID, ns, ns)
			}
		}
	}
}

// TestCodeModeStateToolsAreRoutable is the targeted guard for the regression:
// the data__ and kv__ code-mode state surfaces must each have a default allow
// route to a default internal server whose namespace matches.
func TestCodeModeStateToolsAreRoutable(t *testing.T) {
	serverByID := make(map[string]string, len(defaultDownstreamServers))
	for _, s := range defaultDownstreamServers {
		if s.Transport == "internal" {
			serverByID[s.ID] = s.ToolNamespace
		}
	}

	for _, ns := range []string{"data", "kv"} {
		found := false
		for _, r := range defaultRouteRules {
			if r.Policy != "allow" {
				continue
			}
			var patterns []string
			_ = json.Unmarshal(r.ToolMatch, &patterns)
			for _, p := range patterns {
				if p != ns+"__*" {
					continue
				}
				if serverByID[r.DownstreamServerID] != ns {
					t.Fatalf("%s__* routes to %q (ns %q), want an internal server with ns %q",
						ns, r.DownstreamServerID, serverByID[r.DownstreamServerID], ns)
				}
				found = true
			}
		}
		if !found {
			t.Errorf("no default allow route found for %s__* — code-mode %s tools are unroutable", ns, ns)
		}
	}
}
