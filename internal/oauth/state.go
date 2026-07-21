package oauth

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"
)

// StateEntry holds the CSRF state for an in-flight OAuth authorization.
type StateEntry struct {
	AuthScopeID  string
	CodeVerifier string // PKCE verifier (empty if not using PKCE)
	RedirectURI  string // bound at authorization time; reused for token exchange
	CreatedAt    time.Time
}

// StateStore is an in-memory CSRF state store with automatic TTL cleanup.
type StateStore struct {
	mu      sync.Mutex
	entries map[string]StateEntry
	ttl     time.Duration
}

// NewStateStore creates a StateStore with a 10-minute TTL.
func NewStateStore() *StateStore {
	return &StateStore{
		entries: make(map[string]StateEntry),
		ttl:     10 * time.Minute,
	}
}

// Create generates a new state token and stores it with the given auth scope ID,
// optional PKCE code verifier, and the redirect_uri bound at authorization time.
// Returns the state token.
func (s *StateStore) Create(authScopeID, codeVerifier, redirectURI string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.cleanup()

	token, err := generateStateToken()
	if err != nil {
		return "", err
	}
	s.entries[token] = StateEntry{
		AuthScopeID:  authScopeID,
		CodeVerifier: codeVerifier,
		RedirectURI:  redirectURI,
		CreatedAt:    time.Now(),
	}
	return token, nil
}

// Validate checks a state token and returns the associated entry.
// The token is consumed (deleted) on successful validation.
func (s *StateStore) Validate(state string) (*StateEntry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	entry, ok := s.entries[state]
	if !ok {
		return nil, false
	}
	delete(s.entries, state)

	if time.Since(entry.CreatedAt) > s.ttl {
		return nil, false
	}
	return &entry, true
}

// cleanup removes expired entries. Must be called with mu held.
func (s *StateStore) cleanup() {
	now := time.Now()
	for k, v := range s.entries {
		if now.Sub(v.CreatedAt) > s.ttl {
			delete(s.entries, k)
		}
	}
}

func generateStateToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("crypto/rand: %w", err)
	}
	return hex.EncodeToString(b), nil
}
