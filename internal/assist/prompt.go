package assist

import "strings"

// completeSystemPrompt returns the terse system prompt for a ghost-text
// continuation. It is field-aware: a title continuation stays a fragment,
// a body continuation may extend a sentence. The model is told to emit ONLY
// the continuation text (no preamble, no quoting), so the result appends
// verbatim after the caret.
func completeSystemPrompt(field string) string {
	switch field {
	case "title", "name":
		return "You complete a short single-line label. Output ONLY the text that should follow the cursor, no quotes, no preamble, at most a few words. Do not repeat what is already written."
	default:
		return "You are an inline autocomplete for a technical note editor. Output ONLY the text that should follow the cursor — a natural continuation of at most one sentence. No preamble, no quotes, no markdown fences. Do not repeat text that is already present before the cursor."
	}
}

// completeUserPrompt frames the before/after-caret context for the model.
// The sentinel makes the fill-in-the-middle intent explicit without a
// provider-specific FIM token (which openai_compat backends may not honour).
func completeUserPrompt(req CompleteRequest) string {
	var b strings.Builder
	b.WriteString("Continue the text at the cursor (<CURSOR>). Output only the continuation.\n\n")
	b.WriteString(req.Context)
	b.WriteString("<CURSOR>")
	if req.Cursor != "" {
		b.WriteString(req.Cursor)
	}
	return b.String()
}

// cleanCompletion normalises a raw model completion into ghost text safe to
// append after the caret. It:
//   - strips surrounding quotes / code fences the model sometimes adds;
//   - drops a leading echo of the partial word already typed (so a ghost
//     continues a token instead of duplicating it);
//   - preserves a single joining space when the user is mid-sentence and the
//     model opened with whitespace, so appending the ghost reads naturally.
//
// An empty result (the model declined / restated) yields "" so the GUI shows
// nothing.
func cleanCompletion(raw, before string) string {
	out := strings.Trim(strings.TrimSpace(raw), "`")
	out = strings.TrimSpace(out)
	if len(out) >= 2 && out[0] == '"' && out[len(out)-1] == '"' {
		out = strings.TrimSpace(out[1 : len(out)-1])
	}
	if out == "" {
		return ""
	}

	beforeEndsSpace := before != "" && isSpace(before[len(before)-1])
	rawLeadsSpace := raw != "" && isSpace(raw[0])

	// Echo trim: if the user is mid-token (no trailing space) and the model
	// repeated the partial word, drop the echoed prefix so the ghost extends
	// the same token (e.g. "recom" + "recompute" -> "pute").
	if !beforeEndsSpace {
		if w := lastWord(before); w != "" && strings.HasPrefix(strings.ToLower(out), strings.ToLower(w)) {
			out = out[len(w):]
		}
	}
	if out == "" {
		return ""
	}

	// Re-attach a single joining space when the user is mid-sentence (a word
	// already typed, no trailing space) and the model's reply began with
	// whitespace — TrimSpace above ate it, but the join needs it.
	if !beforeEndsSpace && rawLeadsSpace && before != "" && !isSpace(out[0]) {
		out = " " + out
	}
	return out
}

// isSpace reports whether b is an ASCII whitespace byte.
func isSpace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r'
}

// lastWord returns the final whitespace-delimited token of s, or "".
func lastWord(s string) string {
	s = strings.TrimRight(s, " \t")
	i := strings.LastIndexAny(s, " \t\n")
	if i < 0 {
		return s
	}
	return s[i+1:]
}
