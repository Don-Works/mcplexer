package skills

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// ManifestExtra holds the W4 frontmatter additions: structured fields that
// downstream features (W2 telemetry, W3 refinement, W6 composition graph)
// can read without inspecting the freeform metadata blob.
//
// Every field is optional. Empty values mean "not declared". A skill
// without an extras block parses to the zero ManifestExtra.
//
// JSON shape (canonical wire form, matches the dedicated sqlite column
// `skill_registry_entries.manifest_extra`):
//
//	{
//	  "requires":  [{"binary":"ffmpeg"}, {"env":"ANTHROPIC_API_KEY"}],
//	  "produces":  ["markdown", "json:reveal-deck-config"],
//	  "consumes":  ["markdown", "screenshot"],
//	  "phases":    ["discover", "draft", "publish"],
//	  "refinement":"enabled"
//	}
//
// YAML/TOML frontmatter examples:
//
//	# SKILL.md
//	---
//	name: blog-post
//	description: …
//	requires:
//	  - { binary: "ffmpeg" }
//	  - { env: "ANTHROPIC_API_KEY" }
//	  - { scope: "linear:read" }
//	produces:
//	  - "markdown"
//	consumes:
//	  - "markdown"
//	phases: ["discover", "draft", "publish"]
//	refinement: "enabled"
//	---
//
//	# manifest.toml (.mcskill bundle)
//	[[requires]]
//	binary = "ffmpeg"
//	[[requires]]
//	env = "ANTHROPIC_API_KEY"
//	produces = ["markdown"]
//	consumes = ["markdown"]
//	phases = ["discover", "draft", "publish"]
//	refinement = "enabled"
type ManifestExtra struct {
	// Requires lists prerequisites the runner must check before
	// invoking the skill. Each entry sets exactly one of Binary, Env,
	// or Scope.
	Requires []Requirement `json:"requires,omitempty" toml:"requires,omitempty" yaml:"requires,omitempty"`

	// Produces lists opaque artifact-type strings the skill emits
	// (e.g. "markdown", "json:reveal-deck-config", "screenshot").
	// Free namespace — no validation on shape beyond non-empty.
	Produces []string `json:"produces,omitempty" toml:"produces,omitempty" yaml:"produces,omitempty"`

	// Consumes lists artifact types the skill expects as input. Same
	// namespace + shape rules as Produces.
	Consumes []string `json:"consumes,omitempty" toml:"consumes,omitempty" yaml:"consumes,omitempty"`

	// Phases lists declared phases for W2's task-tree construction.
	// Short kebab-case identifiers (e.g. "discover", "draft", "publish").
	Phases []string `json:"phases,omitempty" toml:"phases,omitempty" yaml:"phases,omitempty"`

	// Refinement is the W3 self-improvement toggle. Allowed values are
	// "", "enabled", "disabled". Empty defaults to "enabled" downstream.
	Refinement string `json:"refinement,omitempty" toml:"refinement,omitempty" yaml:"refinement,omitempty"`
}

// Requirement is one prerequisite entry under `requires:`. Exactly one
// of Binary / Env / Scope must be set per item — Validate enforces this.
//
// Forward-compat: new requirement kinds (port, file, …) can be added as
// fields here without breaking existing skills (they declare the kinds
// they know).
type Requirement struct {
	// Binary is the name of an executable that must be on $PATH (or
	// fully-qualified if the skill cares about pinning). Mutually
	// exclusive with Env and Scope.
	Binary string `json:"binary,omitempty" toml:"binary,omitempty" yaml:"binary,omitempty"`

	// Env is the name of an environment variable that must be set
	// (non-empty) at runtime. Mutually exclusive with Binary and Scope.
	Env string `json:"env,omitempty" toml:"env,omitempty" yaml:"env,omitempty"`

	// Scope is a permission scope the agent must hold (e.g.
	// "linear:read", "github:repo:write"). Free-form; the agent /
	// runner decides what counts. Mutually exclusive with Binary and Env.
	Scope string `json:"scope,omitempty" toml:"scope,omitempty" yaml:"scope,omitempty"`
}

// RefinementEnabled is the canonical "on" value for Refinement.
const RefinementEnabled = "enabled"

// RefinementDisabled is the canonical "off" value for Refinement.
const RefinementDisabled = "disabled"

// EffectiveRefinement returns the runtime-visible refinement mode. An
// empty Refinement defaults to "enabled" — that's the W3 contract.
func (e ManifestExtra) EffectiveRefinement() string {
	if e.Refinement == "" {
		return RefinementEnabled
	}
	return e.Refinement
}

// IsZero reports whether the extras carry any declared content. Useful
// for skipping the sqlite write or trimming the JSON envelope.
func (e ManifestExtra) IsZero() bool {
	return len(e.Requires) == 0 && len(e.Produces) == 0 &&
		len(e.Consumes) == 0 && len(e.Phases) == 0 && e.Refinement == ""
}

// Kind returns the requirement's discriminator ("binary"/"env"/"scope").
// Returns "" when the entry is malformed (zero or multiple fields set).
func (r Requirement) Kind() string {
	set := 0
	kind := ""
	if r.Binary != "" {
		set++
		kind = "binary"
	}
	if r.Env != "" {
		set++
		kind = "env"
	}
	if r.Scope != "" {
		set++
		kind = "scope"
	}
	if set != 1 {
		return ""
	}
	return kind
}

// Validation sentinels for ManifestExtra. Joined into ErrInvalidManifest
// by ValidateExtra so existing callers' errors.Is chains keep working.
var (
	// ErrInvalidExtra is the umbrella error for the W4 fields.
	ErrInvalidExtra = errors.New("invalid manifest extras")

	// ErrInvalidRequirement indicates a Requires[i] entry sets zero or
	// multiple of binary/env/scope.
	ErrInvalidRequirement = errors.New("invalid requirement")

	// ErrInvalidArtifact indicates a Produces/Consumes entry is empty
	// after trimming.
	ErrInvalidArtifact = errors.New("invalid artifact type")

	// ErrInvalidPhase indicates a Phases entry violates the kebab-case
	// rule or the length bound.
	ErrInvalidPhase = errors.New("invalid phase")

	// ErrInvalidRefinement indicates Refinement is set to something
	// other than "", "enabled", "disabled".
	ErrInvalidRefinement = errors.New("invalid refinement")
)

// phaseRE is the kebab-case shape: [a-z0-9-]+ with length 1–32 (the
// length check is enforced separately so the error message can name
// the offending value).
var phaseRE = regexp.MustCompile(`^[a-z0-9-]+$`)

const maxPhaseLen = 32

// ValidateExtra runs the W4 schema rules. Returns nil on success; on
// failure returns an error wrapping ErrInvalidExtra plus the joined
// per-field sentinels — callers can errors.Is against any of them.
func ValidateExtra(e ManifestExtra) error {
	var errs []error
	for i, req := range e.Requires {
		if req.Kind() == "" {
			errs = append(errs,
				fmt.Errorf("%w: requires[%d] must set exactly one of binary/env/scope",
					ErrInvalidRequirement, i))
		}
	}
	for i, a := range e.Produces {
		if strings.TrimSpace(a) == "" {
			errs = append(errs,
				fmt.Errorf("%w: produces[%d] is empty", ErrInvalidArtifact, i))
		}
	}
	for i, a := range e.Consumes {
		if strings.TrimSpace(a) == "" {
			errs = append(errs,
				fmt.Errorf("%w: consumes[%d] is empty", ErrInvalidArtifact, i))
		}
	}
	for i, p := range e.Phases {
		if p == "" {
			errs = append(errs,
				fmt.Errorf("%w: phases[%d] is empty", ErrInvalidPhase, i))
			continue
		}
		if len(p) > maxPhaseLen {
			errs = append(errs,
				fmt.Errorf("%w: phases[%d]=%q exceeds %d chars",
					ErrInvalidPhase, i, p, maxPhaseLen))
			continue
		}
		if !phaseRE.MatchString(p) {
			errs = append(errs,
				fmt.Errorf("%w: phases[%d]=%q must match [a-z0-9-]+",
					ErrInvalidPhase, i, p))
		}
	}
	switch e.Refinement {
	case "", RefinementEnabled, RefinementDisabled:
		// ok
	default:
		errs = append(errs,
			fmt.Errorf("%w: refinement=%q (want one of: enabled, disabled)",
				ErrInvalidRefinement, e.Refinement))
	}
	if len(errs) == 0 {
		return nil
	}
	return fmt.Errorf("%w: %w", ErrInvalidExtra, errors.Join(errs...))
}

// MarshalExtra encodes a ManifestExtra into its canonical JSON bytes
// (the wire shape stored in the sqlite column). A zero ManifestExtra
// renders as "{}" so downstream readers can rely on a parseable JSON
// envelope.
func MarshalExtra(e ManifestExtra) ([]byte, error) {
	if e.IsZero() {
		return []byte("{}"), nil
	}
	out, err := json.Marshal(e)
	if err != nil {
		return nil, fmt.Errorf("marshal manifest extras: %w", err)
	}
	return out, nil
}

// UnmarshalExtra decodes the canonical JSON bytes back into a
// ManifestExtra. Empty / "null" / "{}" inputs yield the zero value
// without an error.
func UnmarshalExtra(data []byte) (ManifestExtra, error) {
	if len(data) == 0 {
		return ManifestExtra{}, nil
	}
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" || trimmed == "null" || trimmed == "{}" {
		return ManifestExtra{}, nil
	}
	var e ManifestExtra
	if err := json.Unmarshal(data, &e); err != nil {
		return ManifestExtra{}, fmt.Errorf("unmarshal manifest extras: %w", err)
	}
	return e, nil
}
