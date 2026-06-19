//go:build p2p

package p2p

import (
	"slices"
	"testing"
)

func TestDialableAddrsForPairing(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{
			name: "keeps direct ipv4 tcp/udp",
			in: []string{
				"/ip4/100.100.0.1/tcp/49527",
				"/ip4/192.168.8.106/udp/58906/quic-v1",
			},
			want: []string{
				"/ip4/100.100.0.1/tcp/49527",
				"/ip4/192.168.8.106/udp/58906/quic-v1",
			},
		},
		{
			name: "drops circuit relay addrs",
			in: []string{
				"/ip4/203.0.113.106/tcp/49527",
				"/dnsaddr/bootstrap.libp2p.io/p2p/QmNnoo/p2p-circuit",
				"/ip4/198.51.100.51/tcp/4001/p2p/QmQ/p2p-circuit",
			},
			want: []string{"/ip4/203.0.113.106/tcp/49527"},
		},
		{
			name: "drops webtransport + webrtc + certhash blobs",
			in: []string{
				"/ip4/100.100.0.1/tcp/49527",
				"/ip4/51.81.93.51/udp/4001/quic-v1/webtransport/certhash/uEi.../p2p/Qm.../p2p-circuit",
				"/ip6/::/udp/4001/webrtc-direct/certhash/uEi.../p2p/Qm.../p2p-circuit",
			},
			want: []string{"/ip4/100.64.0.1/tcp/49527"},
		},
		{
			name: "drops ipv6 link-local + loopback",
			in: []string{
				"/ip4/203.0.113.106/tcp/49527",
				"/ip6/fe80:e::14de:1dd9:f2f1:2306/tcp/49530",
				"/ip6/::1/tcp/49530",
				"/ip6/fd7a:115c:a1e0::f23b:c580/tcp/49530",
			},
			want: []string{
				"/ip4/203.0.113.106/tcp/49527",
				"/ip6/fd7a:115c:a1e0::f23b:c580/tcp/49530",
			},
		},
		{
			name: "drops empty entries",
			in:   []string{"", "/ip4/127.0.0.1/tcp/1234", ""},
			want: []string{"/ip4/127.0.0.1/tcp/1234"},
		},
		{
			name: "representative bloated list shrinks dramatically",
			in: []string{
				"/ip4/100.100.0.1/tcp/49527",
				"/ip4/100.100.0.1/udp/58906/quic-v1",
				"/ip4/127.0.0.1/tcp/49527",
				"/ip4/203.0.113.106/tcp/49527",
				"/ip6/::1/tcp/49530",
				"/ip6/fd7a:115c:a1e0::f23b:c580/tcp/49530",
				"/dnsaddr/bootstrap.libp2p.io/p2p/Qm/p2p-circuit",
				"/ip4/51.81.93.51/tcp/4001/p2p/Qm/p2p-circuit",
				"/ip4/51.81.93.51/udp/4001/quic-v1/webtransport/certhash/uEi/p2p/Qm/p2p-circuit",
				"/ip6/2604:2dc0:200:484::1/tcp/4001/p2p/Qm/p2p-circuit",
			},
			want: []string{
				"/ip4/100.100.0.1/tcp/49527",
				"/ip4/100.100.0.1/udp/58906/quic-v1",
				"/ip4/127.0.0.1/tcp/49527",
				"/ip4/203.0.113.106/tcp/49527",
				"/ip6/fd7a:115c:a1e0::f23b:c580/tcp/49530",
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := dialableAddrsForPairing(c.in)
			if !slices.Equal(got, c.want) {
				t.Errorf("dialableAddrsForPairing\nin   = %v\ngot  = %v\nwant = %v", c.in, got, c.want)
			}
		})
	}
}
