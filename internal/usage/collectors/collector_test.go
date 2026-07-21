package collectors

import (
	"context"
	"errors"
	"testing"

	"github.com/don-works/mcplexer/internal/secrets"
)

var _ SecretReader = (*secrets.Manager)(nil)

type recordingSecret struct {
	value []byte
	err   error
	scope string
	key   string
	calls int
}

func (s *recordingSecret) Get(_ context.Context, scopeID, key string) ([]byte, error) {
	s.calls++
	s.scope, s.key = scopeID, key
	return s.value, s.err
}

func requireNumber(t *testing.T, value *float64, expected float64) {
	t.Helper()
	if value == nil {
		t.Fatalf("number is nil; want %v", expected)
	}
	if *value != expected {
		t.Fatalf("number = %v; want %v", *value, expected)
	}
}

func TestRequireSecret(t *testing.T) {
	t.Run("nil reader is unconfigured", func(t *testing.T) {
		_, err := requireSecret(context.Background(), nil, "scope", "key")
		if err == nil || err.Error() != "no API key configured" {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("reader gets scope and key", func(t *testing.T) {
		reader := &recordingSecret{value: []byte("token")}
		value, err := requireSecret(context.Background(), reader, "scope-1", "api-key")
		if err != nil || value != "token" || reader.scope != "scope-1" || reader.key != "api-key" {
			t.Fatalf("value=%q err=%v scope=%q key=%q", value, err, reader.scope, reader.key)
		}
	})
	t.Run("read error is wrapped", func(t *testing.T) {
		reader := &recordingSecret{err: errors.New("locked")}
		_, err := requireSecret(context.Background(), reader, "scope", "key")
		if err == nil || err.Error() != "secret read: locked" {
			t.Fatalf("error = %v", err)
		}
	})
}
