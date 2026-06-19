package hammerspoon

import (
	"encoding/base64"
	"strings"
	"testing"
)

// TestDecodeScreenshotPNG_RejectsNonPNG is the regression net for the
// zero-buffer capture bug: a blank capture (daemon without Screen Recording
// permission) yields a raw zeroed buffer, not a PNG. decodeScreenshotPNG must
// reject it so spillScreenshot never writes a multi-MB junk .png.
func TestDecodeScreenshotPNG_RejectsNonPNG(t *testing.T) {
	// 1024*768*3 zero bytes — the exact shape of the junk files that
	// accumulated in ~/.mcplexer/hammerspoon-screenshots.
	junk := base64.StdEncoding.EncodeToString(make([]byte, 1024*768*3))
	if _, err := decodeScreenshotPNG(junk); err == nil {
		t.Fatal("decodeScreenshotPNG accepted an all-zero non-PNG payload; should reject")
	} else if !strings.Contains(err.Error(), "not a PNG") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestDecodeScreenshotPNG_RejectsBadBase64(t *testing.T) {
	if _, err := decodeScreenshotPNG("not-valid-base64!!!"); err == nil {
		t.Fatal("decodeScreenshotPNG accepted invalid base64; should reject")
	}
}

func TestDecodeScreenshotPNG_AcceptsRealPNG(t *testing.T) {
	// PNG signature + a few trailing bytes is enough to satisfy the magic check.
	raw := append(append([]byte{}, pngMagic...), 'I', 'H', 'D', 'R')
	b64 := base64.StdEncoding.EncodeToString(raw)
	got, err := decodeScreenshotPNG(b64)
	if err != nil {
		t.Fatalf("decodeScreenshotPNG rejected a PNG-signed payload: %v", err)
	}
	if len(got) != len(raw) {
		t.Errorf("decoded length = %d, want %d", len(got), len(raw))
	}
}
