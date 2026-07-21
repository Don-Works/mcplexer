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
	out, obs := p.Process(ModeOff, nil, in)
	if string(out) != string(in) {
		t.Fatalf("off mode mutated result: %s", out)
	}
	if obs != nil {
		t.Fatalf("off mode produced observations: %+v", obs)
	}
}

// TestShadowSavingsSumToChainedSaving (F2): shadow per-transform savings are
// marginal, so their sum must equal the end-to-end saving On mode achieves on
// the same payload — no double-counting of overlapping wins (minify and dedup
// both claiming the same text block was the original accounting bug).
func TestShadowSavingsSumToChainedSaving(t *testing.T) {
	// structured-dup payload: jsonMinify would shrink the text, then
	// structuredDedup would drop it entirely — maximal overlap.
	payload := structuredDupFixture()
	p := New(nil, 0)
	p.Register(DefaultTransforms()...)

	_, shadowObs := p.Process(ModeShadow, nil, payload)
	shadowSum := 0
	for _, o := range shadowObs {
		if o.Applied {
			t.Fatalf("shadow must never mark Applied: %+v", o)
		}
		if len(o.Stash) != 0 {
			t.Fatalf("shadow must never hand back stashes to persist: %+v", o)
		}
		if o.Changed && o.SavedBytes > 0 {
			shadowSum += o.SavedBytes
		}
	}

	onOut, onObs := p.Process(ModeOn, nil, payload)
	onSum := 0
	for _, o := range onObs {
		if o.Applied {
			onSum += o.SavedBytes
		}
	}
	endToEnd := len(payload) - len(onOut)
	if onSum != endToEnd {
		t.Fatalf("On-mode applied savings %d != end-to-end delta %d", onSum, endToEnd)
	}
	if shadowSum != endToEnd {
		t.Fatalf("shadow savings sum %d != chained end-to-end saving %d (double-counting?)", shadowSum, endToEnd)
	}
}

func TestProcessShadowMeasuresButReturnsOriginal(t *testing.T) {
	p := New(identityEstimate, 0)
	shrunk := json.RawMessage(`{}`)
	p.Register(fakeTransform{name: "shrinker", lossless: true, out: shrunk, changes: true})
	in := json.RawMessage(`{"content":[{"type":"text","text":"0123456789"}]}`)
	out, obs := p.Process(ModeShadow, nil, in)
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
	out, obs := p.Process(ModeOn, nil, in)
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
	out, obs := p.Process(ModeOn, nil, in)
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
	out, obs := p.Process(ModeShadow, nil, in)
	if string(out) != string(in) || obs != nil {
		t.Fatalf("payload below minBytes should be skipped entirely")
	}
}

func TestProcessRecoversFromPanickingTransform(t *testing.T) {
	p := New(identityEstimate, 0)
	p.Register(fakeTransform{name: "boom", lossless: true, panics: true})
	in := json.RawMessage(`{"content":[{"type":"text","text":"0123456789"}]}`)
	out, obs := p.Process(ModeOn, nil, in)
	if string(out) != string(in) {
		t.Fatalf("a panicking transform must leave the result unchanged, got: %s", out)
	}
	if obs[0].Changed || obs[0].Applied {
		t.Errorf("panicking transform must record no change")
	}
}

func TestProcessSkipsDisabledTransform(t *testing.T) {
	p := New(identityEstimate, 0)
	p.Register(fakeTransform{name: "shrinker", lossless: true, out: json.RawMessage(`{}`), changes: true})
	in := json.RawMessage(`{"content":[{"type":"text","text":"0123456789"}]}`)
	out, obs := p.Process(ModeOn, map[string]bool{"shrinker": true}, in)
	if string(out) != string(in) {
		t.Fatalf("disabled transform must not be applied, got: %s", out)
	}
	if len(obs) != 0 {
		t.Fatalf("disabled transform must produce no observations, got %d", len(obs))
	}
}

// badLossless claims to be lossless but corrupts the payload — the Verifier
// backstop must catch it and refuse to apply.
type badLossless struct{}

func (badLossless) Name() string                     { return "bad_lossless" }
func (badLossless) Lossless() bool                   { return true }
func (badLossless) Verify(_, _ json.RawMessage) bool { return false }
func (badLossless) Apply(json.RawMessage) (json.RawMessage, bool) {
	return json.RawMessage(`{"content":[{"type":"text","text":"CORRUPTED"}]}`), true
}

func TestVerifierBackstopBlocksApply(t *testing.T) {
	p := New(identityEstimate, 0)
	p.Register(badLossless{})
	in := json.RawMessage(`{"content":[{"type":"text","text":"original content here, padded to beat minBytes"}]}`)
	out, obs := p.Process(ModeOn, nil, in)
	if string(out) != string(in) {
		t.Fatalf("verifier backstop must block a lossless transform whose Verify fails; got: %s", out)
	}
	if obs[0].Applied {
		t.Error("a transform failing Verify must not be marked Applied")
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
