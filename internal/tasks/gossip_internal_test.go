package tasks

import "testing"

// TestCompareHLCWithTiebreak_TableDriven pins the comparator in
// isolation so the LWW logic is verifiable without a sqlite round
// trip. Edge cases: equal HLC + equal peer (exact dup), equal HLC +
// different peer, unequal HLC.
func TestCompareHLCWithTiebreak_TableDriven(t *testing.T) {
	tests := []struct {
		name        string
		aHLC, aPeer string
		bHLC, bPeer string
		wantSign    int // -1 / 0 / +1
	}{
		{"newer-hlc-wins", "0002", "peer-z", "0001", "peer-a", +1},
		{"older-hlc-loses", "0001", "peer-a", "0002", "peer-z", -1},
		{"same-hlc-smaller-peer-wins", "0001", "peer-a", "0001", "peer-z", +1},
		{"same-hlc-larger-peer-loses", "0001", "peer-z", "0001", "peer-a", -1},
		{"identical-is-zero", "0001", "peer-a", "0001", "peer-a", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := compareHLCWithTiebreak(tt.aHLC, tt.aPeer, tt.bHLC, tt.bPeer)
			gotSign := 0
			switch {
			case got > 0:
				gotSign = 1
			case got < 0:
				gotSign = -1
			}
			if gotSign != tt.wantSign {
				t.Fatalf("compareHLC(%q,%q vs %q,%q) = %d (sign %d), want %d",
					tt.aHLC, tt.aPeer, tt.bHLC, tt.bPeer, got, gotSign, tt.wantSign)
			}
		})
	}
}
