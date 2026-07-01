package compression

import "encoding/json"

// TokenEstimator maps a byte count to an estimated token count for the
// primary consuming context. Injected so this package stays free of a
// dependency on internal/models.
type TokenEstimator func(nBytes int) int

// Observation is the measured effect of one transform on one payload. In
// shadow mode Applied is always false (the output is discarded); the saving
// figures are the WOULD-BE saving. In on mode Applied is true when the output
// was actually used.
type Observation struct {
	Transform   string
	Lossless    bool
	OrigBytes   int
	OutBytes    int
	SavedBytes  int
	OrigTokens  int
	OutTokens   int
	SavedTokens int
	Changed     bool
	Applied     bool
}

// Pipeline evaluates registered transforms over tool-result payloads. It is
// the measure-first spine: in Shadow mode it measures each transform's
// would-be saving and returns the original untouched; in On mode it applies
// proven lossless transforms in sequence.
type Pipeline struct {
	transforms []Transform
	estimate   TokenEstimator
	minBytes   int
}

// New builds a Pipeline. estimate converts bytes→tokens for the savings
// figures (defaults to identity if nil). Payloads smaller than minBytes are
// skipped entirely — not worth the measurement cost.
func New(estimate TokenEstimator, minBytes int) *Pipeline {
	if estimate == nil {
		estimate = func(n int) int { return n }
	}
	return &Pipeline{estimate: estimate, minBytes: minBytes}
}

// Register appends transforms to the pipeline, evaluated in order.
func (p *Pipeline) Register(t ...Transform) {
	p.transforms = append(p.transforms, t...)
}

// Transforms returns the registered transform names in evaluation order.
func (p *Pipeline) Transforms() []string {
	if p == nil {
		return nil
	}
	names := make([]string, len(p.transforms))
	for i, t := range p.transforms {
		names[i] = t.Name()
	}
	return names
}

// Process runs the pipeline over a fully-assembled tool-result envelope for
// the given mode and returns the (possibly transformed) result plus one
// Observation per evaluated transform. In Off mode it returns the input
// unchanged with no observations.
func (p *Pipeline) Process(mode Mode, result json.RawMessage) (json.RawMessage, []Observation) {
	if p == nil || mode == ModeOff || len(p.transforms) == 0 {
		return result, nil
	}
	if p.minBytes > 0 && len(result) < p.minBytes {
		return result, nil
	}
	current := result
	obs := make([]Observation, 0, len(p.transforms))
	for _, t := range p.transforms {
		origBytes := len(current)
		out, changed := safeApply(t, current)
		o := Observation{
			Transform:  t.Name(),
			Lossless:   t.Lossless(),
			OrigBytes:  origBytes,
			OutBytes:   len(out),
			SavedBytes: origBytes - len(out),
			OrigTokens: p.estimate(origBytes),
			OutTokens:  p.estimate(len(out)),
			Changed:    changed,
		}
		o.SavedTokens = o.OrigTokens - o.OutTokens
		// Apply for real only in On mode, only when the transform is lossless
		// and actually shrank the payload. Shadow measures but never applies.
		if mode == ModeOn && changed && t.Lossless() && o.SavedBytes > 0 {
			current = out
			o.Applied = true
		}
		obs = append(obs, o)
	}
	return current, obs
}

// safeApply guards against a buggy transform panicking on the gateway hot
// path: on panic it returns the input unchanged. A first-line kill-switch;
// the full verify-after-compress breaker lands in a later task.
func safeApply(t Transform, in json.RawMessage) (out json.RawMessage, changed bool) {
	defer func() {
		if r := recover(); r != nil {
			out, changed = in, false
		}
	}()
	return t.Apply(in)
}
