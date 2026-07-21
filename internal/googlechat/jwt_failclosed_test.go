package googlechat

import (
	"context"
	"strings"
	"testing"
)

// TestVerifyFailsClosedWithoutAudience asserts that a verifier constructed
// without a project number (audience) rejects every token instead of
// accepting any well-signed Google Chat JWT. Regression guard for the
// aud-bypass: previously audience=="" disabled the aud check entirely.
func TestVerifyFailsClosedWithoutAudience(t *testing.T) {
	v := NewJWTVerifier("")
	_, err := v.Verify(context.Background(), "any.token.value")
	if err == nil {
		t.Fatal("expected error when audience is unset, got nil")
	}
	if !strings.Contains(err.Error(), "no audience configured") {
		t.Fatalf("expected no-audience error, got %q", err.Error())
	}
}
