//go:build p2p

package p2p

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Wire helpers for the pairing protocol. Split out of pairing_p2p.go to
// keep that file under the 300-line limit.
//
// The pairing handshake supports two wire shapes for forward compat:
//
//	v0 (legacy): "<code>\n"               → reply "ok\n" or "no\n"
//	v1 (new):    `{"code":"…","display_name":"…"}\n` → "ok\n" or "no\n"
//
// New initiators emit v1 when their DisplayNameProvider yields a name;
// otherwise they fall back to v0. New responders accept both shapes.

// parsePairRequest accepts either the v0 plain-code line or the v1 JSON
// envelope. Returns the parsed code + remote display_name (display_name is
// "" for v0). Errors only when both shapes fail.
func parsePairRequest(line string) (code, displayName string, err error) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return "", "", errors.New("empty pair request")
	}
	// JSON form must start with '{' — cheap detect avoids unmarshalling
	// the very common all-digit code path.
	if strings.HasPrefix(trimmed, "{") {
		var req struct {
			Code        string `json:"code"`
			DisplayName string `json:"display_name"`
		}
		if jerr := json.Unmarshal([]byte(trimmed), &req); jerr != nil {
			return "", "", fmt.Errorf("decode pair request: %w", jerr)
		}
		return strings.TrimSpace(req.Code), strings.TrimSpace(req.DisplayName), nil
	}
	return trimmed, "", nil
}
