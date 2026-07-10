package store

import "testing"

func TestIsLocalAuthRef(t *testing.T) {
	cases := []struct {
		scope string
		key   string
		want  bool
	}{
		{LocalAuthScopeOpenCode, LocalAuthKeyMiniMax, true},
		{LocalAuthScopeOpenCode, LocalAuthKeyZAI, true},
		{LocalAuthScopeOpenCode, LocalAuthKeyOpenRouter, true},
		{LocalAuthScopeMiMo, LocalAuthKeyMiMoXiaomi, true},
		{LocalAuthScopeOpenCode, "custom-key", false},
		{"workspace-scope", LocalAuthKeyMiniMax, false},
		{"local:other", LocalAuthKeyMiniMax, false},
	}
	for _, tc := range cases {
		if got := IsLocalAuthRef(tc.scope, tc.key); got != tc.want {
			t.Fatalf("IsLocalAuthRef(%q,%q) = %v, want %v", tc.scope, tc.key, got, tc.want)
		}
	}
}