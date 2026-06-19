package skills

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// TOML form of the W4 frontmatter fields embedded at the top level
// of the manifest. Mirrors the brief's YAML example one-for-one.
const extraManifestTOML = `manifest_version = 1
name = "demo-skill"
version = "1.0.0"
description = "demo skill carrying every W4 manifest_extra field"
produces = ["markdown", "json:reveal-deck-config"]
consumes = ["markdown", "screenshot"]
phases = ["discover", "draft", "publish"]
refinement = "enabled"

[[requires]]
binary = "ffmpeg"

[[requires]]
env = "ANTHROPIC_API_KEY"

[[requires]]
scope = "linear:read"

[capabilities]
[capabilities.network]
enabled = false
[capabilities.filesystem]
mode = "none"
`

func TestParse_ManifestExtra(t *testing.T) {
	m, err := Parse([]byte(extraManifestTOML))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got, want := len(m.Requires), 3; got != want {
		t.Fatalf("Requires len = %d, want %d", got, want)
	}
	wantKinds := []string{"binary", "env", "scope"}
	for i, k := range wantKinds {
		if got := m.Requires[i].Kind(); got != k {
			t.Errorf("Requires[%d].Kind() = %q, want %q", i, got, k)
		}
	}
	if got, want := m.Produces, []string{"markdown", "json:reveal-deck-config"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Produces = %v, want %v", got, want)
	}
	if got, want := m.Consumes, []string{"markdown", "screenshot"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Consumes = %v, want %v", got, want)
	}
	if got, want := m.Phases, []string{"discover", "draft", "publish"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Phases = %v, want %v", got, want)
	}
	if got, want := m.Refinement, "enabled"; got != want {
		t.Errorf("Refinement = %q, want %q", got, want)
	}
}

func TestValidate_ManifestExtra(t *testing.T) {
	m, err := Parse([]byte(extraManifestTOML))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if err := Validate(m); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestValidateExtra_HappyAndAbsent(t *testing.T) {
	cases := []struct {
		name string
		in   ManifestExtra
	}{
		{"absent", ManifestExtra{}},
		{
			"binary-only requires",
			ManifestExtra{Requires: []Requirement{{Binary: "git"}}},
		},
		{
			"env-only requires + produces + phases + refinement disabled",
			ManifestExtra{
				Requires:   []Requirement{{Env: "FOO"}},
				Produces:   []string{"markdown"},
				Phases:     []string{"a", "b-2"},
				Refinement: "disabled",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateExtra(tc.in); err != nil {
				t.Fatalf("ValidateExtra: %v", err)
			}
		})
	}
}

func TestValidateExtra_Failures(t *testing.T) {
	cases := []struct {
		name    string
		in      ManifestExtra
		wantErr error
	}{
		{
			"requires zero set",
			ManifestExtra{Requires: []Requirement{{}}},
			ErrInvalidRequirement,
		},
		{
			"requires multiple set",
			ManifestExtra{Requires: []Requirement{{Binary: "git", Env: "FOO"}}},
			ErrInvalidRequirement,
		},
		{
			"produces empty",
			ManifestExtra{Produces: []string{""}},
			ErrInvalidArtifact,
		},
		{
			"produces whitespace-only",
			ManifestExtra{Produces: []string{"  "}},
			ErrInvalidArtifact,
		},
		{
			"consumes empty",
			ManifestExtra{Consumes: []string{""}},
			ErrInvalidArtifact,
		},
		{
			"phase uppercase",
			ManifestExtra{Phases: []string{"Discover"}},
			ErrInvalidPhase,
		},
		{
			"phase empty",
			ManifestExtra{Phases: []string{""}},
			ErrInvalidPhase,
		},
		{
			"phase too long",
			ManifestExtra{Phases: []string{strings.Repeat("a", maxPhaseLen+1)}},
			ErrInvalidPhase,
		},
		{
			"phase has underscore",
			ManifestExtra{Phases: []string{"foo_bar"}},
			ErrInvalidPhase,
		},
		{
			"refinement unknown",
			ManifestExtra{Refinement: "maybe"},
			ErrInvalidRefinement,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateExtra(tc.in)
			if err == nil {
				t.Fatalf("ValidateExtra: expected error %v, got nil", tc.wantErr)
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("ValidateExtra: %v does not wrap %v", err, tc.wantErr)
			}
			if !errors.Is(err, ErrInvalidExtra) {
				t.Fatalf("ValidateExtra: %v does not wrap ErrInvalidExtra", err)
			}
		})
	}
}

func TestManifestExtra_EffectiveRefinement(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", RefinementEnabled},
		{RefinementEnabled, RefinementEnabled},
		{RefinementDisabled, RefinementDisabled},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			e := ManifestExtra{Refinement: tc.in}
			if got := e.EffectiveRefinement(); got != tc.want {
				t.Errorf("EffectiveRefinement() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestManifestExtra_IsZero(t *testing.T) {
	if !((ManifestExtra{}).IsZero()) {
		t.Fatal("expected zero value to report IsZero=true")
	}
	cases := []ManifestExtra{
		{Requires: []Requirement{{Binary: "x"}}},
		{Produces: []string{"x"}},
		{Consumes: []string{"x"}},
		{Phases: []string{"x"}},
		{Refinement: "enabled"},
	}
	for i, c := range cases {
		if c.IsZero() {
			t.Errorf("case %d expected IsZero=false, got true: %#v", i, c)
		}
	}
}

func TestMarshalExtra_Roundtrip(t *testing.T) {
	want := ManifestExtra{
		Requires: []Requirement{{Binary: "ffmpeg"}, {Env: "API"}, {Scope: "linear:read"}},
		Produces: []string{"markdown"},
		Consumes: []string{"png"},
		Phases:   []string{"discover", "draft"},
	}
	data, err := MarshalExtra(want)
	if err != nil {
		t.Fatalf("MarshalExtra: %v", err)
	}
	// Spot-check the wire shape includes the canonical keys.
	if !strings.Contains(string(data), `"requires"`) {
		t.Errorf("MarshalExtra missing requires key: %s", data)
	}
	got, err := UnmarshalExtra(data)
	if err != nil {
		t.Fatalf("UnmarshalExtra: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("round-trip mismatch:\ngot  %#v\nwant %#v", got, want)
	}
}

func TestMarshalExtra_ZeroRendersAsEmptyObject(t *testing.T) {
	data, err := MarshalExtra(ManifestExtra{})
	if err != nil {
		t.Fatalf("MarshalExtra: %v", err)
	}
	if got := string(data); got != "{}" {
		t.Errorf("zero MarshalExtra = %q, want %q", got, "{}")
	}
	// And UnmarshalExtra accepts empty/null/{} as zero.
	for _, in := range []string{"", "{}", "null", "  "} {
		got, err := UnmarshalExtra([]byte(in))
		if err != nil {
			t.Errorf("UnmarshalExtra(%q): %v", in, err)
		}
		if !got.IsZero() {
			t.Errorf("UnmarshalExtra(%q) = %#v, want zero", in, got)
		}
	}
}

func TestMarshalExtra_InvalidJSONInput(t *testing.T) {
	if _, err := UnmarshalExtra([]byte("not json")); err == nil {
		t.Fatal("UnmarshalExtra: expected error on bad input")
	}
}

func TestManifestExtra_JSONShapeMatchesBrief(t *testing.T) {
	// The brief calls out a precise wire shape; verify keys are
	// lowercase + match what the dashboard / API consumers parse.
	e := ManifestExtra{
		Requires:   []Requirement{{Binary: "ffmpeg"}},
		Produces:   []string{"markdown"},
		Consumes:   []string{"screenshot"},
		Phases:     []string{"draft"},
		Refinement: "enabled",
	}
	data, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	for _, key := range []string{
		`"requires"`, `"produces"`, `"consumes"`, `"phases"`, `"refinement"`, `"binary"`,
	} {
		if !strings.Contains(string(data), key) {
			t.Errorf("missing key %s in %s", key, data)
		}
	}
}
