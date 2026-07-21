package api

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// sseFrame is one parsed `event:`/`data:` SSE frame.
type sseFrame struct {
	event string
	data  string
}

// parseSSEFrames splits an SSE body into frames on the blank-line delimiter,
// pulling the `event:` and (possibly multi-line) `data:` fields out of each.
// Heartbeat comment lines (":\n") and empty frames are skipped.
func parseSSEFrames(body string) []sseFrame {
	var frames []sseFrame
	for _, block := range strings.Split(body, "\n\n") {
		block = strings.TrimRight(block, "\n")
		if block == "" {
			continue
		}
		var f sseFrame
		var dataLines []string
		for _, line := range strings.Split(block, "\n") {
			switch {
			case strings.HasPrefix(line, "event: "):
				f.event = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				dataLines = append(dataLines, strings.TrimPrefix(line, "data: "))
			}
		}
		if f.event == "" && len(dataLines) == 0 {
			continue // comment / heartbeat only
		}
		f.data = strings.Join(dataLines, "\n")
		frames = append(frames, f)
	}
	return frames
}

// TestStreamCompletion asserts the SSE wire contract from streamCompletion:
// one `event: token` frame per word whose data reassembles losslessly to the
// completion, followed by a single terminal `event: done` frame carrying the
// resolving profile. A regression here would ship silently — this is the
// handler's core deliverable.
func TestStreamCompletion(t *testing.T) {
	tests := []struct {
		name       string
		text       string
		profile    string
		wantTokens int // number of `event: token` frames expected
	}{
		{name: "two words", text: "hello world", profile: "p1", wantTokens: 2},
		{name: "single word", text: "hello", profile: "fast", wantTokens: 1},
		{name: "newline preserved", text: "line1\nline2", profile: "p2", wantTokens: 2},
		{name: "empty completion still terminates", text: "", profile: "p3", wantTokens: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder() // *httptest.ResponseRecorder implements http.Flusher

			streamCompletion(w, w, tt.text, tt.profile)

			frames := parseSSEFrames(w.Body.String())
			if len(frames) == 0 {
				t.Fatalf("no frames emitted; body = %q", w.Body.String())
			}

			// All-but-last frames must be `event: token`; the last must be `done`.
			done := frames[len(frames)-1]
			tokens := frames[:len(frames)-1]

			if done.event != "done" {
				t.Fatalf("terminal frame event = %q, want done; body = %q", done.event, w.Body.String())
			}
			wantDone := `{"profile":"` + tt.profile + `"}`
			if done.data != wantDone {
				t.Errorf("done data = %q, want %q", done.data, wantDone)
			}

			if len(tokens) != tt.wantTokens {
				t.Fatalf("token frames = %d, want %d; body = %q", len(tokens), tt.wantTokens, w.Body.String())
			}

			// Lossless reassembly: concatenating token data (decoding the \n
			// escape sseData applies) reproduces the original completion.
			var sb strings.Builder
			for _, f := range tokens {
				if f.event != "token" {
					t.Fatalf("expected event: token, got %q", f.event)
				}
				sb.WriteString(strings.ReplaceAll(f.data, "\\n", "\n"))
			}
			if got := sb.String(); got != tt.text {
				t.Errorf("reassembled tokens = %q, want %q", got, tt.text)
			}
		})
	}
}
