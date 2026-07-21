package sanitize

// ProcessOptions configures a single sanitize pass over a tool result body.
//
// Callers should reuse a single *Denylist across calls — DefaultDenylist()
// returns a process-global, precompiled instance — so the regex compile
// cost is paid once.
type ProcessOptions struct {
	// Denylist is the rule set to scan against. If nil, DefaultDenylist()
	// is used. Callers that hot-path should pre-resolve this themselves
	// to skip the per-call nil check + indirection.
	Denylist *Denylist

	// Source labels the producer of the untrusted content, e.g.
	// "downstream:linear" or "tool:github__list_issues". Stamped into
	// the <untrusted-content source="…"> attribute when enveloped.
	Source string

	// Trust is the asserted trust level: "low" | "medium" | "high".
	// Anything else is normalised to "low" by Envelope.
	Trust string

	// Body is the raw tool-result text to sanitize.
	Body string

	// EnvelopeAlways forces every clean (no-hit) body to be enveloped
	// anyway. The Guards plan defaults this on at the policy layer; we
	// keep the flag here so dev/test can run with clean diffs.
	EnvelopeAlways bool
}

// Process runs the M1 sanitize pipeline against opts.Body:
//
//  1. Short-circuit if opts.Body is already enveloped (IsEnveloped). Pass
//     through verbatim — never double-wrap.
//  2. Scan the body against opts.Denylist (DefaultDenylist when nil).
//  3. If any hits found, return ActionEnveloped with the body wrapped in
//     <untrusted-content source=… trust=…>…</untrusted-content> and the
//     hit list in Matches (the caller emits one audit event per match).
//  4. No hits: envelope anyway when EnvelopeAlways is set, else pass through.
//
// Block / Redact / Quarantine dispositions are reserved for later
// milestones; M1 always envelopes on hit.
func Process(opts ProcessOptions) Result {
	if IsEnveloped(opts.Body) {
		return Result{
			Action: ActionPassThrough,
			Body:   opts.Body,
			Source: opts.Source,
			Trust:  opts.Trust,
		}
	}

	dl := opts.Denylist
	if dl == nil {
		dl = DefaultDenylist()
	}
	matches := dl.Scan(opts.Body)

	if len(matches) > 0 {
		return Result{
			Action:  ActionEnveloped,
			Body:    Envelope(opts.Source, opts.Trust, opts.Body),
			Matches: matches,
			Source:  opts.Source,
			Trust:   opts.Trust,
		}
	}

	if opts.EnvelopeAlways {
		return Result{
			Action: ActionEnveloped,
			Body:   Envelope(opts.Source, opts.Trust, opts.Body),
			Source: opts.Source,
			Trust:  opts.Trust,
		}
	}

	return Result{
		Action: ActionPassThrough,
		Body:   opts.Body,
		Source: opts.Source,
		Trust:  opts.Trust,
	}
}
