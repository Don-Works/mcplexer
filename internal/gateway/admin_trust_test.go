package gateway

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestAdminTrustLevel proves the gate reports WHICH signal granted
// admin: the data dir wins over everything, a mcplexer source tree
// (via cwd or workspace root) classifies as the weaker source-repo
// escape, and anything else is none.
func TestAdminTrustLevel(t *testing.T) {
	dataDir := filepath.Clean("/Users/test/.mcplexer")
	g := NewAdminCWDGate(dataDir)

	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "go.mod"),
		[]byte("module github.com/don-works/mcplexer\n\ngo 1.25\n"), 0o644); err != nil {
		t.Fatalf("seed go.mod: %v", err)
	}
	project := t.TempDir() // ordinary project dir, no go.mod

	cases := []struct {
		name  string
		cwd   string
		roots []string
		want  AdminTrust
	}{
		{"data dir cwd", dataDir, nil, AdminTrustDataDir},
		{"data dir subpath", filepath.Join(dataDir, "backups"), nil, AdminTrustDataDir},
		{"source repo cwd", repo, nil, AdminTrustSourceRepo},
		{"source repo workspace root", "", []string{repo}, AdminTrustSourceRepo},
		{"data dir cwd wins over repo root", dataDir, []string{repo}, AdminTrustDataDir},
		{"plain project cwd", project, nil, AdminTrustNone},
		{"empty everything", "", nil, AdminTrustNone},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := g.AdminTrustLevel(c.cwd, c.roots); got != c.want {
				t.Errorf("AdminTrustLevel(%q, %v) = %q, want %q", c.cwd, c.roots, got, c.want)
			}
		})
	}

	t.Run("disabled gate is datadir", func(t *testing.T) {
		disabled := NewAdminCWDGate("")
		if got := disabled.AdminTrustLevel(project, nil); got != AdminTrustDataDir {
			t.Errorf("disabled gate = %q, want datadir", got)
		}
	})

	t.Run("trust level agrees with IsAdminContext", func(t *testing.T) {
		for _, c := range cases {
			granted := g.IsAdminContext(c.cwd, c.roots)
			level := g.AdminTrustLevel(c.cwd, c.roots)
			if granted != (level != AdminTrustNone) {
				t.Errorf("divergence for (%q, %v): IsAdminContext=%v but trust=%q",
					c.cwd, c.roots, granted, level)
			}
		}
	})

	// The MCPLEXER_ADMIN_ALLOW_ANY_CWD break-glass grants admin from any
	// dir, but must resolve to the WEAKER source-repo level so the
	// cross-workspace route guard still bites (2026-07-06 incident: the
	// env var was set on the daemon, forcing full data-dir trust and
	// making the guard inert). t.Setenv forbids t.Parallel — these run
	// serially.
	t.Run("env break-glass is source-repo not datadir", func(t *testing.T) {
		t.Setenv("MCPLEXER_ADMIN_ALLOW_ANY_CWD", "1")
		if got := g.AdminTrustLevel(project, nil); got != AdminTrustSourceRepo {
			t.Errorf("env-bypass plain dir = %q, want source-repo", got)
		}
		if got := g.AdminTrustLevel("", nil); got != AdminTrustSourceRepo {
			t.Errorf("env-bypass empty cwd = %q, want source-repo", got)
		}
		// A genuine data-dir CWD still wins even with the env var set.
		if got := g.AdminTrustLevel(dataDir, nil); got != AdminTrustDataDir {
			t.Errorf("env-bypass + data-dir cwd = %q, want datadir", got)
		}
		// A source-repo cwd is source-repo either way.
		if got := g.AdminTrustLevel(repo, nil); got != AdminTrustSourceRepo {
			t.Errorf("env-bypass + repo cwd = %q, want source-repo", got)
		}
		// Agreement invariant holds under the break-glass too: IsAdminContext
		// is true from anywhere, and the level is never None.
		if !g.IsAdminContext(project, nil) || g.AdminTrustLevel(project, nil) == AdminTrustNone {
			t.Error("env-bypass broke the IsAdminContext/None agreement invariant")
		}
	})
}

// TestAdminTrustContextRoundTrip covers the ctx plumbing between the
// gateway dispatch layer and the control backend.
func TestAdminTrustContextRoundTrip(t *testing.T) {
	ctx := context.Background()
	if got := AdminTrustFromContext(ctx); got != AdminTrustNone {
		t.Errorf("unstamped ctx = %q, want none", got)
	}
	ctx = WithAdminTrust(ctx, AdminTrustSourceRepo)
	if got := AdminTrustFromContext(ctx); got != AdminTrustSourceRepo {
		t.Errorf("stamped ctx = %q, want source-repo", got)
	}
}
