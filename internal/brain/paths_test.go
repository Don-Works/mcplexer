package brain

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/don-works/mcplexer/internal/store"
)

func TestSafeSlug(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		want    string
		wantErr error
	}{
		{"normal", "my-workspace", "my-workspace", nil},
		{"underscore", "my_workspace", "my_workspace", nil},
		{"alphanumeric", "ws123", "ws123", nil},
		{"with spaces trim", "  acme  ", "acme", nil},
		{"single char", "a", "a", nil},
		{"empty", "", "", ErrSlugEmpty},
		{"blank", "   ", "", ErrSlugEmpty},
		{"dot", ".", "", ErrSlugTraversal},
		{"dotdot", "..", "", ErrSlugTraversal},
		{"dot prefix", ".hidden", "", ErrSlugDotPrefix},
		{"slash", "a/b", "", ErrSlugTraversal},
		{"backslash", `a\b`, "", ErrSlugTraversal},
		{"parent traversal", "../escape", "", ErrSlugDotPrefix},
		{"absolute path", "/etc/passwd", "", ErrSlugTraversal},
		{"nested parent", "a/../../etc", "", ErrSlugTraversal},
		{"embedded dots", "foo..bar", "", ErrSlugTraversal},
		{"consecutive dots", "a..b", "", ErrSlugTraversal},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := safeSlug(tc.input)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("safeSlug(%q) error = %v, want %v", tc.input, err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("safeSlug(%q) unexpected error: %v", tc.input, err)
			}
			if got != tc.want {
				t.Fatalf("safeSlug(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestWorkspaceDirRejectsTraversal(t *testing.T) {
	c := Config{Dir: "/root/brain"}
	hostile := []string{
		"../../../etc",
		"..%2Fetc",
		"/absolute",
		`back\slash`,
		"..",
		".",
	}
	for _, slug := range hostile {
		dir, err := c.WorkspaceDir(slug)
		if err == nil {
			t.Fatalf("WorkspaceDir(%q) = %q, expected error for traversal slug", slug, dir)
		}
	}
}

func TestClientDirRejectsTraversal(t *testing.T) {
	c := Config{Dir: "/root/brain"}
	dir, err := c.ClientDir("../../etc")
	if err == nil {
		t.Fatalf("ClientDir(../../etc) = %q, expected error", dir)
	}
}

func TestSafePersonWorkspace(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"", "crm"},
		{"global", "crm"},
		{"  ", "crm"},
		{"crm", "crm"},
		{"my-workspace", "my-workspace"},
		{"ws123", "ws123"},
		{"../escape", "crm"},
		{"/absolute", "crm"},
		{`back\slash`, "crm"},
		{".", "crm"},
		{"..", "crm"},
		{".hidden", "crm"},
		{"foo..bar", "crm"},
		{"a/../../etc", "crm"},
	}
	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			got := safePersonWorkspace(tc.input)
			if got != tc.want {
				t.Fatalf("safePersonWorkspace(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestPersonPath_HostileWorkspace(t *testing.T) {
	brainDir := "/safe/brain"
	hostile := []struct {
		workspaceID string
		desc        string
	}{
		{"../escape", "parent traversal"},
		{"/etc", "absolute path"},
		{`back\slash`, "backslash"},
		{".", "dot"},
		{"..", "dotdot"},
		{".hidden", "dot prefix"},
		{"foo..bar", "embedded dots"},
		{"a/../../etc", "nested traversal"},
	}
	for _, tc := range hostile {
		t.Run(tc.desc, func(t *testing.T) {
			ser := &Serializer{cfg: Config{Dir: brainDir}}
			p := &store.PersonEntry{
				Name:        "Alice",
				ID:          "01PERSON01",
				WorkspaceID: tc.workspaceID,
			}
			got := ser.personPath(p)
			wantSuffix := filepath.Join("workspaces", "crm", "crm", "people", "alice.md")
			if !strings.HasSuffix(got, wantSuffix) {
				t.Fatalf("personPath with hostile workspace %q = %q, want suffix %q", tc.workspaceID, got, wantSuffix)
			}
		})
	}
}

func TestPersonPath_ValidWorkspace(t *testing.T) {
	brainDir := "/safe/brain"
	ser := &Serializer{cfg: Config{Dir: brainDir}}
	p := &store.PersonEntry{
		Name:        "Bob",
		ID:          "01PERSON02",
		WorkspaceID: "acme-corp",
	}
	got := ser.personPath(p)
	want := filepath.Join(brainDir, "workspaces", "acme-corp", "crm", "people", "bob.md")
	if got != want {
		t.Fatalf("personPath = %q, want %q", got, want)
	}
}
