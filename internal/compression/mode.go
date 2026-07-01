// Package compression is the measure-first token-compression pipeline that
// sits at the mcplexer gateway seam. It compresses downstream MCP tool-result
// payloads before they reach the model's context. Its defining property is
// three-state operation: transforms can be measured in shadow (dry-run) with
// zero risk before ever being applied for real.
package compression

import "strings"

// Mode is the three-state compression rollout switch.
//
//   - Off    skips the transform entirely.
//   - Shadow (dry-run) runs the transform only to MEASURE the would-be saving
//     and always returns the ORIGINAL untouched — zero accuracy/latency risk
//     to the answer. This is the safe, auto-wired default.
//   - On     applies the transform's output for real (lossless transforms
//     only, until a CCR backing lands for reversible-lossy ones).
type Mode string

const (
	ModeOff    Mode = "off"
	ModeShadow Mode = "shadow"
	ModeOn     Mode = "on"
)

// ParseMode normalizes a settings/env string into a Mode. Empty or unknown
// input resolves to Shadow — the measure-first default, so a fresh or legacy
// install auto-wires into dry-run measurement rather than silently off or on.
func ParseMode(s string) Mode {
	switch Mode(strings.ToLower(strings.TrimSpace(s))) {
	case ModeOff:
		return ModeOff
	case ModeOn:
		return ModeOn
	default:
		return ModeShadow
	}
}

// Valid reports whether m is one of the three known modes.
func (m Mode) Valid() bool {
	switch m {
	case ModeOff, ModeShadow, ModeOn:
		return true
	default:
		return false
	}
}
