package googlechat

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// DefaultPairingTTL is how long a generated pairing code remains valid.
const DefaultPairingTTL = 10 * time.Minute

// PairingCodeLength is the base32-encoded length of pairing codes.
// 8 chars = 40 bits of entropy — enough that guessing in 10 min is impractical.
const PairingCodeLength = 8

// ErrPairingInvalid is returned when a pairing code is unknown or expired.
var ErrPairingInvalid = errors.New("pairing code invalid or expired")

// CreatePairing generates a new pairing code bound to a workspace, persists it,
// and returns the code. Caller builds the deep-link / "say /pair <code> to the
// bot" instructions.
func (m *Manager) CreatePairing(
	ctx context.Context,
	workspaceID, createdBySessionID string,
	ttl time.Duration,
) (*store.GoogleChatPairing, error) {
	if workspaceID == "" {
		return nil, fmt.Errorf("workspace_id is required")
	}
	if ttl <= 0 {
		ttl = DefaultPairingTTL
	}
	code, err := generatePairingCode()
	if err != nil {
		return nil, fmt.Errorf("generate code: %w", err)
	}
	now := time.Now().UTC()
	p := &store.GoogleChatPairing{
		Code:               code,
		WorkspaceID:        workspaceID,
		CreatedBySessionID: createdBySessionID,
		ExpiresAt:          now.Add(ttl),
		CreatedAt:          now,
	}
	if err := m.store.CreateGoogleChatPairing(ctx, p); err != nil {
		return nil, fmt.Errorf("persist pairing: %w", err)
	}
	return p, nil
}

// ConsumePairing validates a code, deletes it on success, and returns the
// workspaceID it was bound to. Returns ErrPairingInvalid if the code is
// unknown or expired.
func (m *Manager) ConsumePairing(ctx context.Context, code string) (string, error) {
	code = strings.TrimSpace(code)
	if code == "" {
		return "", ErrPairingInvalid
	}
	p, err := m.store.GetGoogleChatPairing(ctx, code)
	if err != nil || p == nil {
		return "", ErrPairingInvalid
	}
	if time.Now().UTC().After(p.ExpiresAt) {
		_ = m.store.DeleteGoogleChatPairing(ctx, code)
		return "", ErrPairingInvalid
	}
	if err := m.store.DeleteGoogleChatPairing(ctx, code); err != nil {
		return "", ErrPairingInvalid
	}
	return p.WorkspaceID, nil
}

// sweepPairings periodically purges expired codes.
func (m *Manager) sweepPairings(ctx context.Context) {
	n, err := m.store.SweepExpiredGoogleChatPairings(ctx, time.Now().UTC())
	if err != nil {
		slog.Warn("googlechat: sweep pairings", "error", err)
		return
	}
	if n > 0 {
		slog.Info("googlechat: swept expired pairings", "count", n)
	}
}

func generatePairingCode() (string, error) {
	var raw [5]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw[:]), nil
}
