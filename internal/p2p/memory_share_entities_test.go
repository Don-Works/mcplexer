// memory_share_entities_test.go — verifies the peer-local strip rule
// for memory entity links. Builds under both -tags p2p and the slim
// stub mode since IsEntityKindPeerLocal / FilterEntitiesForMesh are
// declared in both build paths.
package p2p

import "testing"

func TestIsEntityKindPeerLocal(t *testing.T) {
	cases := []struct {
		kind string
		want bool
	}{
		{"place", true},
		{"PLACE", true},
		{" Place ", true},
		{"event", true},
		{"task", false},
		{"person", false},
		{"peer", false},
		{"agent", false},
		{"org", false},
		{"skill", false},
		{"artifact", false},
		{"workspace", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := IsEntityKindPeerLocal(tc.kind); got != tc.want {
			t.Errorf("IsEntityKindPeerLocal(%q) = %v, want %v",
				tc.kind, got, tc.want)
		}
	}
}

func TestFilterEntitiesForMesh(t *testing.T) {
	in := []EntityLink{
		{Kind: "task", ID: "T1"},
		{Kind: "place", ID: "/Users/example/foo"}, // peer-local — drop
		{Kind: "person", ID: "alice@x"},
		{Kind: "event", ID: "01KSG-EVT"}, // peer-local — drop
		{Kind: "PLACE", ID: "/abs"},      // case-insensitive — drop
		{Kind: "artifact", ID: "https://example.com/x"},
	}
	got := FilterEntitiesForMesh(in)
	if len(got) != 3 {
		t.Fatalf("expected 3 entries after filter, got %d: %+v", len(got), got)
	}
	for _, e := range got {
		if IsEntityKindPeerLocal(e.Kind) {
			t.Errorf("peer-local link leaked: %+v", e)
		}
	}
}

func TestFilterEntitiesForMeshEmpty(t *testing.T) {
	if got := FilterEntitiesForMesh(nil); got != nil {
		t.Errorf("nil input should return nil, got %+v", got)
	}
	if got := FilterEntitiesForMesh([]EntityLink{}); got != nil {
		t.Errorf("empty input should return nil, got %+v", got)
	}
}
