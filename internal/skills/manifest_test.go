package skills

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

const happyManifestTOML = `manifest_version = 1
name = "blog-post"
version = "1.2.3"
description = "Turn an idea into a blog post"
author = "Example Maintainer <maintainer@example.com>"
license = "AGPL-3.0-or-later"
homepage = "https://github.com/don-works/mcplexer-skills"
tags = ["writing", "marketing"]
entry_point = "skill.md"
readme = "README.md"
mcplexer_min_version = "0.3.0"

[dependencies]
cmux-browser = { version = "^1.0.0" }
"@example/internal" = { version = ">=2.1.0, <3.0.0" }

[capabilities]

[[capabilities.mcp_servers]]
name = "github"
version = "^1.0.0"

[[capabilities.mcp_servers]]
name = "linear"
optional = true

[capabilities.network]
enabled = true
allowed_hosts = ["api.github.com", "linear.app"]

[capabilities.filesystem]
mode = "read_only"
paths = ["~/notes/...", "~/.config/mcplexer"]
`

func TestParse_HappyPath(t *testing.T) {
	m, err := Parse([]byte(happyManifestTOML))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if m.Name != "blog-post" {
		t.Errorf("Name = %q, want %q", m.Name, "blog-post")
	}
	if m.Version != "1.2.3" {
		t.Errorf("Version = %q, want %q", m.Version, "1.2.3")
	}
	if m.ManifestVersion != 1 {
		t.Errorf("ManifestVersion = %d, want 1", m.ManifestVersion)
	}
	if len(m.Capabilities.MCPServers) != 2 {
		t.Errorf("MCPServers len = %d, want 2", len(m.Capabilities.MCPServers))
	}
	if !m.Capabilities.MCPServers[1].Optional {
		t.Errorf("expected linear server to be optional")
	}
	if m.Capabilities.Filesystem.Mode != FilesystemModeReadOnly {
		t.Errorf("Filesystem.Mode = %q, want read_only", m.Capabilities.Filesystem.Mode)
	}
	if got := m.Dependencies["cmux-browser"].Version; got != "^1.0.0" {
		t.Errorf("Dependencies[cmux-browser].Version = %q, want ^1.0.0", got)
	}
}

func TestValidate_HappyPath(t *testing.T) {
	m, err := Parse([]byte(happyManifestTOML))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if err := Validate(m); err != nil {
		t.Fatalf("Validate: unexpected error: %v", err)
	}
}

func TestValidate_Invalid(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(m *Manifest)
		wantErr error
	}{
		{
			name:    "missing manifest_version",
			mutate:  func(m *Manifest) { m.ManifestVersion = 0 },
			wantErr: ErrMissingField,
		},
		{
			name:    "unsupported manifest_version",
			mutate:  func(m *Manifest) { m.ManifestVersion = 999 },
			wantErr: ErrUnsupportedManifestVersion,
		},
		{
			name:    "missing name",
			mutate:  func(m *Manifest) { m.Name = "" },
			wantErr: ErrMissingField,
		},
		{
			name:    "invalid name (uppercase)",
			mutate:  func(m *Manifest) { m.Name = "Blog-Post" },
			wantErr: ErrInvalidName,
		},
		{
			name:    "invalid name (leading hyphen)",
			mutate:  func(m *Manifest) { m.Name = "-foo" },
			wantErr: ErrInvalidName,
		},
		{
			name:    "missing version",
			mutate:  func(m *Manifest) { m.Version = "" },
			wantErr: ErrMissingField,
		},
		{
			name:    "invalid version (not semver)",
			mutate:  func(m *Manifest) { m.Version = "1.2" },
			wantErr: ErrInvalidVersion,
		},
		{
			name:    "missing description",
			mutate:  func(m *Manifest) { m.Description = "" },
			wantErr: ErrMissingField,
		},
		{
			name:    "invalid mcplexer_min_version",
			mutate:  func(m *Manifest) { m.MCPlexerMinVersion = "v1" },
			wantErr: ErrInvalidVersion,
		},
		{
			name: "dependency missing version",
			mutate: func(m *Manifest) {
				m.Dependencies = map[string]Dependency{"foo": {}}
			},
			wantErr: ErrMissingField,
		},
		{
			name: "dependency invalid range",
			mutate: func(m *Manifest) {
				m.Dependencies = map[string]Dependency{"foo": {Version: ">>1.0"}}
			},
			wantErr: ErrInvalidVersion,
		},
		{
			name: "mcp_server missing name",
			mutate: func(m *Manifest) {
				m.Capabilities.MCPServers = []MCPServer{{Name: ""}}
			},
			wantErr: ErrMissingField,
		},
		{
			name: "mcp_server invalid version range",
			mutate: func(m *Manifest) {
				m.Capabilities.MCPServers = []MCPServer{{Name: "x", Version: "abc"}}
			},
			wantErr: ErrInvalidVersion,
		},
		{
			name: "filesystem unknown mode",
			mutate: func(m *Manifest) {
				m.Capabilities.Filesystem = FilesystemCapability{Mode: "anything"}
			},
			wantErr: ErrInvalidCapability,
		},
		{
			name: "filesystem read_only without paths",
			mutate: func(m *Manifest) {
				m.Capabilities.Filesystem = FilesystemCapability{
					Mode: FilesystemModeReadOnly,
				}
			},
			wantErr: ErrInvalidCapability,
		},
		{
			name: "filesystem none with paths",
			mutate: func(m *Manifest) {
				m.Capabilities.Filesystem = FilesystemCapability{
					Mode:  FilesystemModeNone,
					Paths: []string{"~/x"},
				}
			},
			wantErr: ErrInvalidCapability,
		},
		{
			name: "network disabled with allowed_hosts",
			mutate: func(m *Manifest) {
				m.Capabilities.Network = NetworkCapability{
					Enabled:      false,
					AllowedHosts: []string{"example.com"},
				}
			},
			wantErr: ErrInvalidCapability,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, err := Parse([]byte(happyManifestTOML))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			tt.mutate(m)
			err = Validate(m)
			if err == nil {
				t.Fatalf("Validate: expected error %v, got nil", tt.wantErr)
			}
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Validate: error %v does not wrap %v", err, tt.wantErr)
			}
			if !errors.Is(err, ErrInvalidManifest) {
				t.Fatalf("Validate: error %v does not wrap ErrInvalidManifest", err)
			}
		})
	}
}

func TestValidate_NilManifest(t *testing.T) {
	err := Validate(nil)
	if err == nil {
		t.Fatal("Validate(nil): want error, got nil")
	}
	if !errors.Is(err, ErrInvalidManifest) {
		t.Errorf("Validate(nil): error %v does not wrap ErrInvalidManifest", err)
	}
}

func TestParse_UnknownField(t *testing.T) {
	bad := happyManifestTOML + "\nunknown_field = true\n"
	if _, err := Parse([]byte(bad)); err == nil {
		t.Fatal("Parse: expected error on unknown field, got nil")
	}
}

func TestRoundTrip_ParseMarshalParse(t *testing.T) {
	first, err := Parse([]byte(happyManifestTOML))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	out, err := Marshal(first)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !strings.Contains(string(out), `name = 'blog-post'`) &&
		!strings.Contains(string(out), `name = "blog-post"`) {
		t.Errorf("marshalled output missing name: %s", out)
	}
	second, err := Parse(out)
	if err != nil {
		t.Fatalf("Parse(round-trip): %v\n%s", err, out)
	}
	if !reflect.DeepEqual(first, second) {
		t.Errorf("round-trip mismatch:\nfirst=%#v\nsecond=%#v", first, second)
	}
	if err := Validate(second); err != nil {
		t.Errorf("Validate after round-trip: %v", err)
	}
}

func TestMarshal_NilManifest(t *testing.T) {
	if _, err := Marshal(nil); err == nil {
		t.Fatal("Marshal(nil): want error, got nil")
	}
}

func TestIsXRange(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"1.2.3", true}, // accepted as numeric (validateVersionRange will treat as exact)
		{"1.2.x", true},
		{"1.x", true},
		{"1", true},
		{"*", true},
		{"x.y.z", false},
		{"1.2.3.4", false},
		{"abc", false},
	}
	for _, tt := range tests {
		if got := isXRange(tt.in); got != tt.want {
			t.Errorf("isXRange(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}
