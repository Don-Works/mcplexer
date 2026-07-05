package compression

import (
	"encoding/json"
	"regexp"
	"strings"
)

// base64Externalize replaces long inline base64 blobs (embedded images, data
// URIs, encoded file bodies) with a CCR marker. Base64 is token-dense noise a
// model cannot decode by reading; the exact blob stays one mcpx__retrieve
// away. Each blob is stashed individually so its marker addresses exactly the
// dropped bytes.

// base64MinBlobBytes is the smallest run worth externalizing — far above the
// marker cost, and long enough that prose or identifiers can't reach it.
const base64MinBlobBytes = 1024

// base64RunRE matches a standard-alphabet base64 run with optional data-URI
// prefix and padding. Go's regexp caps repeat counts at 1000, so the length
// floor here is a coarse pre-filter; the real base64MinBlobBytes threshold is
// enforced in code. URL-safe base64 (-_) is deliberately excluded: it
// overlaps with identifier/token charsets and risks false positives.
var base64RunRE = regexp.MustCompile(`(data:[a-zA-Z0-9/+.-]+;base64,)?[A-Za-z0-9+/]{256,}={0,2}`)

type base64Externalize struct{}

func (base64Externalize) Name() string   { return "base64_externalize" }
func (base64Externalize) Lossless() bool { return false }

func (base64Externalize) Apply(result json.RawMessage) (json.RawMessage, bool) {
	return result, false
}

func (base64Externalize) ApplyWithStash(result json.RawMessage) (json.RawMessage, bool, [][]byte) {
	var stash [][]byte
	out, changed := walkTextBlocks(result, func(text string) (string, bool) {
		if len(text) < base64MinBlobBytes {
			return text, false
		}
		replaced := false
		rewritten := base64RunRE.ReplaceAllStringFunc(text, func(run string) string {
			if len(run) < base64MinBlobBytes || !looksLikeBase64Payload(run) {
				return run
			}
			blob := []byte(run)
			stash = append(stash, blob)
			replaced = true
			return CCRMarker(CCRKey(blob), len(blob)) + " (inline base64 blob externalized)"
		})
		return rewritten, replaced
	})
	if !changed {
		return result, false, nil
	}
	return out, true, stash
}

// looksLikeBase64Payload guards against grabbing long runs that merely share
// base64's alphabet: a giant lowercase hex dump or an all-caps identifier
// wall. Real base64 payloads virtually always mix case or use +/ within the
// first kilobyte; a data: URI prefix is proof by construction.
func looksLikeBase64Payload(run string) bool {
	if strings.HasPrefix(run, "data:") {
		return true
	}
	sample := run
	if len(sample) > 1024 {
		sample = sample[:1024]
	}
	if strings.ContainsAny(sample, "+/") {
		return true
	}
	hasUpper := strings.IndexFunc(sample, func(r rune) bool { return r >= 'A' && r <= 'Z' }) >= 0
	hasLower := strings.IndexFunc(sample, func(r rune) bool { return r >= 'a' && r <= 'z' }) >= 0
	return hasUpper && hasLower
}
