package mesh

import "testing"

// TestStripReservedTags is the origin-spoofing regression: a paired peer must
// not be able to inject a "from:" or "p2p" tag that survives into the stored
// message, because sourcePeerID returns the FIRST "from:" tag and the
// dispatcher uses it as the cross-peer peer-scope trust anchor. Non-reserved
// tags (mesh-trigger tag_match targets) must be preserved.
func TestStripReservedTags(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"strips injected from", "from:12D3KooWVictim", ""},
		{"strips injected p2p marker", "p2p", ""},
		{"strips both reserved, keeps rest", "from:evil,p2p,alert,build", "alert,build"},
		{"keeps ordinary tags", "alert,team:secops", "alert,team:secops"},
		{"trims spaces", " alert , from:x , build ", "alert,build"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := stripReservedTags(tc.in); got != tc.want {
				t.Fatalf("stripReservedTags(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestInboundTagsTrustedMarkerFirst asserts the composed inbound tag string
// puts the trusted from:<sender> marker first even when the peer supplied its
// own from: tag, so sourcePeerID resolves the real sender.
func TestInboundTagsTrustedMarkerFirst(t *testing.T) {
	const sender = "12D3KooWRealSender"
	peerTags := "from:12D3KooWSpoofedSelf,alert"
	inbound := "p2p,from:" + sender
	if s := stripReservedTags(peerTags); s != "" {
		inbound = inbound + "," + s
	}
	want := "p2p,from:12D3KooWRealSender,alert"
	if inbound != want {
		t.Fatalf("inbound tags = %q, want %q", inbound, want)
	}
}
