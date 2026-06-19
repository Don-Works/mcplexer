// Package skills defines the .mcskill bundle manifest schema and validation.
//
// A .mcskill bundle is a gzip'd tar archive containing a manifest.toml at its
// root, the skill prompt (skill.md), and optional README, scripts, and assets.
// This package only handles parsing and validating the manifest — bundle I/O,
// signing, and installation live in sibling packages (out of scope for M2.1).
//
// The TOML library used is github.com/pelletier/go-toml/v2 — see docs/skill-format.md
// for the rationale.
package skills

import (
	"bytes"
	"fmt"

	"github.com/pelletier/go-toml/v2"
)

// ManifestVersion is the current manifest schema version. Bundles must declare
// a manifest_version that this implementation knows how to parse.
const ManifestVersion = 1

// FilesystemMode controls how an installed skill's scripts may touch the host
// filesystem. Modes are mutually exclusive — a skill picks exactly one.
type FilesystemMode string

const (
	// FilesystemModeNone forbids any filesystem access from skill scripts.
	FilesystemModeNone FilesystemMode = "none"

	// FilesystemModeReadOnly permits read-only access to the paths in
	// FilesystemCapability.Paths.
	FilesystemModeReadOnly FilesystemMode = "read_only"

	// FilesystemModeReadWrite permits read+write access to the paths in
	// FilesystemCapability.Paths.
	FilesystemModeReadWrite FilesystemMode = "read_write"
)

// Manifest is the top-level structure of a .mcskill bundle's manifest.toml.
//
// Field grouping:
//   - identity:      Name, Version, ManifestVersion
//   - metadata:      Description, Author, License, Homepage, Tags
//   - skill body:    EntryPoint, ReadmePath
//   - dependencies:  Dependencies, MCPlexerMinVersion
//   - capabilities:  MCPServers, Network, Filesystem
//   - provenance:    Signature (free-form, M2.4 will define encoding)
type Manifest struct {
	// ManifestVersion is the schema version of this manifest. Required.
	// Bundles produced by this codebase always set ManifestVersion = 1.
	ManifestVersion int `toml:"manifest_version"`

	// Name is the skill's machine-readable identifier (e.g. "blog-post").
	// Lowercase letters, digits, and hyphens only. Required.
	Name string `toml:"name"`

	// Version is the skill's semantic version (e.g. "1.2.3"). Required.
	Version string `toml:"version"`

	// Description is a short single-line summary shown in skill listings.
	// Required.
	Description string `toml:"description"`

	// Author is the skill author or maintainer, free-form (e.g. "Example
	// Maintainer <maintainer@example.com>"). Optional.
	Author string `toml:"author,omitempty"`

	// License is the SPDX identifier (e.g. "MIT", "Apache-2.0",
	// "AGPL-3.0-or-later").
	// Optional but strongly recommended.
	License string `toml:"license,omitempty"`

	// Homepage is an optional URL pointing to the skill's docs or source.
	Homepage string `toml:"homepage,omitempty"`

	// Tags is a free-form list of search/discovery keywords. Optional.
	Tags []string `toml:"tags,omitempty"`

	// EntryPoint is the path inside the bundle to the markdown skill body.
	// Defaults to "skill.md" when empty.
	EntryPoint string `toml:"entry_point,omitempty"`

	// ReadmePath is the optional path inside the bundle to a human-readable
	// README. Defaults to "README.md" when empty (and absent file is fine).
	ReadmePath string `toml:"readme,omitempty"`

	// MCPlexerMinVersion is the minimum mcplexer version required to install
	// the bundle (semver, e.g. "0.3.0"). Optional. When unset, any version
	// is accepted.
	MCPlexerMinVersion string `toml:"mcplexer_min_version,omitempty"`

	// Dependencies declares other skills this skill depends on. Each entry
	// is keyed by skill name and contains a semver range constraint.
	Dependencies map[string]Dependency `toml:"dependencies,omitempty"`

	// Capabilities declares the runtime capabilities the skill needs.
	// Installation will surface these to the user for explicit consent
	// (M2.3 defines the consent model — out of scope here).
	Capabilities Capabilities `toml:"capabilities"`

	// Signature is the canonical minisign signature blob (untrusted comment
	// + base64 sig + trusted comment + base64 comment-sig, four lines, as
	// produced by minisign(1) / aead.dev/minisign). The trusted comment
	// binds the signature to the bundle digest — see internal/skills/sign.go
	// and ADR 0002. Empty when the bundle is unsigned.
	//
	// Validate enforces only the on-the-wire format; full cryptographic
	// verification against a trust store happens at install time via
	// skills.Verify.
	Signature string `toml:"signature,omitempty"`

	// ManifestExtra is embedded so its fields (requires, produces,
	// consumes, phases, refinement) live at the top level of the TOML
	// manifest — the brief's wire shape uses bare `requires = [...]`,
	// `produces = [...]`, etc. with no enclosing [extra] table. All
	// five sub-fields are optional; a manifest that doesn't declare
	// them parses to the zero ManifestExtra. See manifest_extra.go for
	// the type definitions and ValidateExtra for the schema rules.
	ManifestExtra
}

// Dependency expresses a single skill→skill dependency by version range.
//
// Examples in TOML:
//
//	[dependencies]
//	cmux-browser = { version = "^1.0.0" }
//	"@org/internal-skill" = { version = ">=2.1.0 <3.0.0" }
type Dependency struct {
	// Version is a semver range string. Supported operators follow the
	// standard npm/cargo grammar: =, !=, >, >=, <, <=, ~, ^, and the "x.y.*"
	// wildcard form. Required when Dependency is present.
	Version string `toml:"version"`
}

// Capabilities declares everything the skill might do at runtime.
type Capabilities struct {
	// MCPServers lists downstream MCP servers the skill expects to call.
	// Each entry references a server by name (matching the gateway's
	// catalogue) and may pin a semver range — see MCPServer.
	MCPServers []MCPServer `toml:"mcp_servers,omitempty"`

	// Network controls whether scripts may reach the network.
	Network NetworkCapability `toml:"network"`

	// Filesystem controls scripts' filesystem access.
	Filesystem FilesystemCapability `toml:"filesystem"`
}

// MCPServer references a downstream MCP server the skill expects to use.
//
// Encoded in TOML as an inline table:
//
//	mcp_servers = [
//	  { name = "github" },
//	  { name = "linear", version = "^1.0.0" },
//	]
//
// We deliberately use a struct (not a bare string) so future fields like
// `optional`, `min_tools`, etc. can be added without breaking the schema.
type MCPServer struct {
	// Name is the server's namespace (e.g. "github", "linear"). Required.
	Name string `toml:"name"`

	// Version is an optional semver range constraint. Empty means any.
	Version string `toml:"version,omitempty"`

	// Optional, when true, allows the skill to install even if the server
	// is not present. Scripts must guard their calls themselves.
	Optional bool `toml:"optional,omitempty"`
}

// NetworkCapability declares whether and where a skill may reach the network.
type NetworkCapability struct {
	// Enabled, when false, blocks all outbound network access from the
	// skill's scripts. Default false.
	Enabled bool `toml:"enabled"`

	// AllowedHosts is an optional allow-list of hostnames or host:port.
	// When non-empty and Enabled is true, only these hosts may be reached.
	// When empty and Enabled is true, all hosts are allowed.
	AllowedHosts []string `toml:"allowed_hosts,omitempty"`
}

// FilesystemCapability declares filesystem access for skill scripts.
type FilesystemCapability struct {
	// Mode is one of FilesystemModeNone, FilesystemModeReadOnly,
	// FilesystemModeReadWrite. Required (defaults to "none" when omitted).
	Mode FilesystemMode `toml:"mode"`

	// Paths is the list of allowed paths when Mode != none. Paths may
	// contain `~` (home expansion) and a single trailing `/...` to match
	// any descendant. Empty list with a non-none mode is invalid.
	Paths []string `toml:"paths,omitempty"`
}

// Parse decodes a manifest.toml byte slice into a Manifest. It does not
// validate semantic correctness — call Validate after Parse.
func Parse(data []byte) (*Manifest, error) {
	var m Manifest
	dec := toml.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("decode manifest: %w", err)
	}
	return &m, nil
}

// Marshal encodes a Manifest to canonical TOML bytes. Field ordering matches
// the struct definition order, which is the canonical wire form.
func Marshal(m *Manifest) ([]byte, error) {
	if m == nil {
		return nil, fmt.Errorf("marshal manifest: nil manifest")
	}
	var buf bytes.Buffer
	enc := toml.NewEncoder(&buf)
	enc.SetIndentTables(true)
	if err := enc.Encode(m); err != nil {
		return nil, fmt.Errorf("encode manifest: %w", err)
	}
	return buf.Bytes(), nil
}
