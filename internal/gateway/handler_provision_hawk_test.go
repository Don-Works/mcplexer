package gateway

import (
	"testing"

	"github.com/don-works/mcplexer/internal/addon"
)

func TestParseHawkPromptSecret(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{
			name: "json",
			raw:  `{"id":"kid","key":"secret","algorithm":"sha1"}`,
		},
		{
			name: "key value lines",
			raw:  "HAWK_ID=kid\nHAWK_KEY=secret\nHAWK_ALGORITHM=sha256",
		},
		{
			name: "two lines",
			raw:  "kid\nsecret",
		},
		{
			name: "colon",
			raw:  "kid:secret",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseHawkPromptSecret(tt.raw)
			if err != nil {
				t.Fatalf("parseHawkPromptSecret: %v", err)
			}
			if got["HAWK_ID"] != "kid" {
				t.Fatalf("HAWK_ID = %q, want kid", got["HAWK_ID"])
			}
			if got["HAWK_KEY"] != "secret" {
				t.Fatalf("HAWK_KEY = %q, want secret", got["HAWK_KEY"])
			}
			if got["HAWK_ALGORITHM"] == "" {
				t.Fatalf("HAWK_ALGORITHM should be set")
			}
		})
	}
}

func TestParseHawkPromptSecretRequiresIDAndKey(t *testing.T) {
	_, err := parseHawkPromptSecret(`{"id":"kid"}`)
	if err == nil {
		t.Fatal("expected missing key error")
	}
}

func TestProvisionAuthScopeType_Hawk(t *testing.T) {
	if got := provisionAuthScopeType(addon.AuthHawk); got != "hawk" {
		t.Fatalf("provisionAuthScopeType(hawk) = %q, want hawk", got)
	}
	if got := provisionAuthScopeType(addon.AuthBearer); got != "generic" {
		t.Fatalf("provisionAuthScopeType(bearer) = %q, want generic", got)
	}
}
