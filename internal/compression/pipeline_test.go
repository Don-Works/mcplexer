package compression

import (
	"encoding/json"
	"testing"
)

type fakeTransform struct {
	name     string
	lossless bool
	out      json.RawMessage
	changes  bool
	panics   bool
}

func (f fakeTransform) Name() string   { return f.name }
func (f fakeTransform) Lossless() bool { return f.lossless }
func (f fakeTransform) Apply(in json.RawMessage) (json.RawMessage, bool) {
	if f.panics {
		panic("boom")
	}
	if !f.changes {
		return in, false
	}
	return f.out, true
}

func identityEstimate(n int) int { return n }

func TestProcessOffReturnsUnchangedNoObs(t *testing.T) {
	p := New(identityEstimate, 0)
	p.Register(fakeTransform{name: "x", lossless: true, out: json.RawMessage(`{}`), changes: true})
	in := json.RawMessage(`{"content":[{"type":"text","text":"0123456789"}]}`)
	out, obs := p.Process(ModeOff, in)
	if string(out) != string(in) {
		t.Fatalf("off mode mutated result: %s", out)
	}
	if obs != nil {
		t.Fatalf("off mode produced observations: %+v", obs)
	}
}

func TestProcessShadowMeasuresButReturnsOriginal(t *testing.T) {
	p := New(identityEstimate, 0)
	shrunk := json.RawMessage(`{}`)
	p.Register(fakeTransform{name: "shrinker", lossless: true, out: shrunk, changes: true})
	in := json.RawMessage(`{"content":[{"type":"text","text":"0123456789"}]}`)
	out, obs := p.Process(ModeShadow, in)
	if string(out) != string(in) {
		t.Fatalf("shadow mode MUST return the original untouched, got: %s", out)
	}
	if len(obs) != 1 {
		t.Fatalf("want 1 observation, got %d", len(obs))
	}
	o := obs[0]
	if o.Applied {
		t.Errorf("shadow observation must not be Applied")
	}
	if !o.Changed {
		t.Errorf("expected Changed=true")
	}
	if o.SavedBytes != len(in)-len(shrunk) {
		t.Errorf("SavedBytes = %d, want %d", o.SavedBytes, len(in)-len(shrunk))
	}
	if o.SavedTokens <= 0 {
		t.Errorf("expected positive would-be token saving, got %d", o.SavedTokens)
	}
}

func TestProcessOnAppliesLosslessTransform(t *testing.T) {
	p := New(identityEstimate, 0)
	shrunk := json.RawMessage(`{"content":[{"type":"text","text":"x"}]}`)
	p.Register(fakeTransform{name: "shrinker", lossless: true, out: shrunk, changes: true})
	in := json.RawMessage(`{"content":[{"type":"text","text":"0123456789abcdef"}]}`)
	out, obs := p.Process(ModeOn, in)
	if string(out) != string(shrunk) {
		t.Fatalf("on mode should apply the transform, got: %s", out)
	}
	if !obs[0].Applied {
		t.Errorf("expected Applied=true in on mode")
	}
}

func TestProcessOnSkipsLossyTransform(t *testing.T) {
	p := New(identityEstimate, 0)
	shrunk := json.RawMessage(`{}`)
	p.Register(fakeTransform{name: "lossy", lossless: false, out: shrunk, changes: true})
	in := json.RawMessage(`{"content":[{"type":"text","text":"0123456789"}]}`)
	out, obs := p.Process(ModeOn, in)
	if string(out) != string(in) {
		t.Fatalf("on mode must NOT apply a lossy transform without a CCR backing, got: %s", out)
	}
	if obs[0].Applied {
		t.Errorf("lossy transform must not be Applied")
	}
}

func TestProcessSkipsBelowMinBytes(t *testing.T) {
	p := New(identityEstimate, 1024)
	p.Register(fakeTransform{name: "x", lossless: true, out: json.RawMessage(`{}`), changes: true})
	in := json.RawMessage(`{"content":[{"type":"text","text":"small"}]}`)
	out, obs := p.Process(ModeShadow, in)
	if string(out) != string(in) || obs != nil {
		t.Fatalf("payload below minBytes should be skipped entirely")
	}
}

func TestProcessRecoversFromPanickingTransform(t *testing.T) {
	p := New(identityEstimate, 0)
	p.Register(fakeTransform{name: "boom", lossless: true, panics: true})
	in := json.RawMessage(`{"content":[{"type":"text","text":"0123456789"}]}`)
	out, obs := p.Process(ModeOn, in)
	if string(out) != string(in) {
		t.Fatalf("a panicking transform must leave the result unchanged, got: %s", out)
	}
	if obs[0].Changed || obs[0].Applied {
		t.Errorf("panicking transform must record no change")
	}
}

func TestParseMode(t *testing.T) {
	cases := map[string]Mode{
		"":        ModeShadow,
		"off":     ModeOff,
		"OFF":     ModeOff,
		" on ":    ModeOn,
		"shadow":  ModeShadow,
		"garbage": ModeShadow,
	}
	for in, want := range cases {
		if got := ParseMode(in); got != want {
			t.Errorf("ParseMode(%q) = %q, want %q", in, got, want)
		}
	}
}
