package compression

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
)

// Terminal-output transforms: ANSI escape stripping, carriage-return
// progress-frame collapse, and run-length collapse of identical lines. All
// are StashingTransforms — the exact original text block is stashed in CCR
// before anything is dropped, so every change is recoverable via
// mcpx__retrieve. Each fires only past a minimum saving so the marker never
// costs more than it saves.

// terminalMinSavedBytes is the smallest per-block saving worth a CCR marker.
const terminalMinSavedBytes = 256

var (
	// CSI (colors/cursor), OSC (titles/hyperlinks, BEL- or ST-terminated),
	// and two-char ESC sequences. Bare ESC is left alone — stripping it
	// without its sequence would change bytes for no gain.
	ansiRE = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(\x07|\x1b\\)|\x1b[@-Z\\^_]`)
)

// stashingTextTransform factors the shared shape of the terminal transforms:
// rewrite each text block with fn, and when the rewrite saves enough bytes,
// stash the exact original and append a CCR marker plus a short reason.
func stashingTextTransform(result json.RawMessage, reason string, fn func(string) string) (json.RawMessage, bool, [][]byte) {
	var stash [][]byte
	out, changed := walkTextBlocks(result, func(text string) (string, bool) {
		rewritten := fn(text)
		if len(text)-len(rewritten) < terminalMinSavedBytes {
			return text, false
		}
		original := []byte(text)
		stash = append(stash, original)
		return rewritten + "\n" + CCRMarker(CCRKey(original), len(original)) + " (" + reason + ")", true
	})
	if !changed {
		return result, false, nil
	}
	return out, true, stash
}

// --- ansiStrip: remove ANSI/CSI/OSC escape sequences (recoverable) ---

// ANSI escapes are terminal rendering noise the model pays tokens for; colors
// can occasionally encode meaning (red=fail), so the original stays a
// retrieve away rather than being trusted as pure noise.
type ansiStrip struct{}

func (ansiStrip) Name() string   { return "ansi_strip" }
func (ansiStrip) Lossless() bool { return false }

func (ansiStrip) Apply(result json.RawMessage) (json.RawMessage, bool) {
	return result, false
}

func (ansiStrip) ApplyWithStash(result json.RawMessage) (json.RawMessage, bool, [][]byte) {
	return stashingTextTransform(result, "ANSI terminal escapes stripped", func(text string) string {
		return ansiRE.ReplaceAllString(text, "")
	})
}

// --- crCollapse: keep only the final frame of \r-overwritten lines ---

// Progress bars and spinners emit many frames separated by bare \r; a
// terminal displays only the last frame per line, so keeping just that is
// faithful to what a human operator would have seen.
type crCollapse struct{}

func (crCollapse) Name() string   { return "cr_progress_collapse" }
func (crCollapse) Lossless() bool { return false }

func (crCollapse) Apply(result json.RawMessage) (json.RawMessage, bool) {
	return result, false
}

func (crCollapse) ApplyWithStash(result json.RawMessage) (json.RawMessage, bool, [][]byte) {
	return stashingTextTransform(result, "overwritten progress frames collapsed", collapseCRFrames)
}

func collapseCRFrames(text string) string {
	if !strings.Contains(text, "\r") {
		return text
	}
	lines := strings.Split(text, "\n")
	for i, ln := range lines {
		// CRLF endings: the trailing \r belongs to the line break, not a frame.
		crlf := strings.HasSuffix(ln, "\r")
		body := strings.TrimSuffix(ln, "\r")
		if j := strings.LastIndexByte(body, '\r'); j >= 0 {
			body = body[j+1:]
		}
		if crlf {
			body += "\r"
		}
		lines[i] = body
	}
	return strings.Join(lines, "\n")
}

// --- repeatCollapse: run-length collapse of identical consecutive lines ---

// repeatMinRun is the shortest run worth collapsing. The exact count is kept
// inline, so no information is lost even before the CCR fallback.
const repeatMinRun = 5

type repeatCollapse struct{}

func (repeatCollapse) Name() string   { return "repeat_collapse" }
func (repeatCollapse) Lossless() bool { return false }

func (repeatCollapse) Apply(result json.RawMessage) (json.RawMessage, bool) {
	return result, false
}

func (repeatCollapse) ApplyWithStash(result json.RawMessage) (json.RawMessage, bool, [][]byte) {
	return stashingTextTransform(result, "repeated lines collapsed, exact counts inline", collapseRepeatedLines)
}

func collapseRepeatedLines(text string) string {
	lines := strings.Split(text, "\n")
	out := make([]string, 0, len(lines))
	for i := 0; i < len(lines); {
		run := 1
		for i+run < len(lines) && lines[i+run] == lines[i] {
			run++
		}
		if run >= repeatMinRun && strings.TrimSpace(lines[i]) != "" {
			out = append(out, lines[i],
				"[previous line repeated "+strconv.Itoa(run-1)+" more times]")
		} else {
			out = append(out, lines[i:i+run]...)
		}
		i += run
	}
	return strings.Join(out, "\n")
}
