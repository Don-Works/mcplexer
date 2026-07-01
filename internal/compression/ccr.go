package compression

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
)

// CCR (Compress-Cache-Retrieve) makes a lossy drop transparently reversible: a
// transform stashes the original bytes, leaves a self-describing marker inline,
// and the model can call mcpx__retrieve to pull the exact original on demand.
// ONE hash function everywhere — mixing MD5/SHA is what produced headroom's
// dangling-marker bug (#816).

// CCRKey is the stable content-address of a payload: the first 24 hex chars of
// its SHA-256. Collisions at 24 hex chars (96 bits) are negligible for a
// short-TTL per-session cache.
func CCRKey(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])[:24]
}

// CCRMarker is the self-describing placeholder left inline where content was
// dropped. It names the key + original size and tells the model exactly how to
// recover the full bytes.
func CCRMarker(key string, origBytes int) string {
	return fmt.Sprintf(
		`[[ccr key=%s bytes=%d — call mcpx__retrieve({"key":"%s"}) to expand the omitted content]]`,
		key, origBytes, key,
	)
}

var ccrKeyRE = regexp.MustCompile(`key=([0-9a-f]{24})`)

// ParseCCRKeys returns the distinct CCR keys referenced in text, in order.
func ParseCCRKeys(text string) []string {
	matches := ccrKeyRE.FindAllStringSubmatch(text, -1)
	seen := make(map[string]bool, len(matches))
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		if !seen[m[1]] {
			seen[m[1]] = true
			out = append(out, m[1])
		}
	}
	return out
}
