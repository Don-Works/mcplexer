package telegram

import (
	"testing"
)

func TestGeneratePairingCode_Shape(t *testing.T) {
	seen := make(map[string]struct{})
	for i := 0; i < 200; i++ {
		code, err := generatePairingCode()
		if err != nil {
			t.Fatalf("generate: %v", err)
		}
		if len(code) != PairingCodeLength {
			t.Errorf("len: want %d got %d (%q)", PairingCodeLength, len(code), code)
		}
		for _, c := range code {
			if (c < 'A' || c > 'Z') && (c < '2' || c > '7') {
				t.Errorf("non-base32 char %q in %q", c, code)
			}
		}
		if _, dup := seen[code]; dup {
			t.Errorf("duplicate code %q on attempt %d", code, i)
		}
		seen[code] = struct{}{}
	}
}
