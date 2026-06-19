package store

import "testing"

func TestIsGlobalEntityKind(t *testing.T) {
	cases := []struct {
		kind string
		want bool
	}{
		// Globally identifiable — same id resolves to the same entity
		// across workspaces and machines.
		{"task", true},
		{"person", true},
		{"peer", true},
		{"agent", true},
		{"org", true},
		{"skill", true},
		{"artifact", true},

		// Casing / whitespace shouldn't change the answer.
		{"PERSON", true},
		{"  Task  ", true},

		// Peer-local or context-bound — keep workspace scope.
		{"place", false},
		{"event", false},
		{"workspace", false},

		// Unknown kinds default to scoped (conservative — opt-in not opt-out).
		{"", false},
		{"thingamajig", false},
	}
	for _, tc := range cases {
		if got := IsGlobalEntityKind(tc.kind); got != tc.want {
			t.Errorf("IsGlobalEntityKind(%q) = %v, want %v", tc.kind, got, tc.want)
		}
	}
}

func TestEntityRecallCanEscapeScope(t *testing.T) {
	cases := []struct {
		name string
		refs []EntityRef
		want bool
	}{
		{
			name: "empty refs never escape",
			refs: nil,
			want: false,
		},
		{
			name: "single global subject escapes",
			refs: []EntityRef{{Kind: "person", ID: "bob@x", Role: "subject"}},
			want: true,
		},
		{
			name: "empty role treated as subject — escapes",
			refs: []EntityRef{{Kind: "task", ID: "T1"}},
			want: true,
		},
		{
			name: "derived_from role escapes",
			refs: []EntityRef{{Kind: "skill", ID: "summarise", Role: "derived_from"}},
			want: true,
		},
		{
			name: "mentioned role does NOT escape (contextual reference)",
			refs: []EntityRef{{Kind: "person", ID: "bob@x", Role: "mentioned"}},
			want: false,
		},
		{
			name: "any local-kind ref vetoes escape",
			refs: []EntityRef{
				{Kind: "person", ID: "bob@x"},
				{Kind: "place", ID: "/Users/example/proj"},
			},
			want: false,
		},
		{
			name: "workspace kind never escapes (inherently scoped)",
			refs: []EntityRef{{Kind: "workspace", ID: "ws-1"}},
			want: false,
		},
		{
			name: "mixed roles — one mentioned vetoes the lot",
			refs: []EntityRef{
				{Kind: "task", ID: "T", Role: "subject"},
				{Kind: "person", ID: "alice", Role: "mentioned"},
			},
			want: false,
		},
		{
			name: "all global subjects escape",
			refs: []EntityRef{
				{Kind: "task", ID: "T"},
				{Kind: "person", ID: "alice", Role: "subject"},
				{Kind: "skill", ID: "summarise"},
			},
			want: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := EntityRecallCanEscapeScope(tc.refs); got != tc.want {
				t.Errorf("EntityRecallCanEscapeScope = %v, want %v", got, tc.want)
			}
		})
	}
}
