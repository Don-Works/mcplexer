package telegram

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

// CreatePairing generates a new pairing code bound to a workspace and
// platform, persists it, and returns the code. Caller builds the platform
// deep-link (e.g. https://t.me/<bot>?start=<code>).
func (m *Manager) CreatePairing(
	ctx context.Context,
	platform, workspaceID, createdBySessionID string,
	ttl time.Duration,
) (*store.TelegramPairing, error) {
	if platform == "" || workspaceID == "" {
		return nil, fmt.Errorf("platform and workspace_id are required")
	}
	if ttl <= 0 {
		ttl = DefaultPairingTTL
	}

	code, err := generatePairingCode()
	if err != nil {
		return nil, fmt.Errorf("generate code: %w", err)
	}
	now := time.Now().UTC()
	p := &store.TelegramPairing{
		Code:               code,
		Platform:           platform,
		WorkspaceID:        workspaceID,
		CreatedBySessionID: createdBySessionID,
		ExpiresAt:          now.Add(ttl),
		CreatedAt:          now,
	}
	if err := m.store.CreateTelegramPairing(ctx, p); err != nil {
		return nil, fmt.Errorf("persist pairing: %w", err)
	}
	return p, nil
}

// ConsumePairing validates a code, deletes it on success, and returns the
// (platform, workspaceID) it was bound to. Returns ErrPairingInvalid if the
// code is unknown or expired — bridge rejects unknown chats, so the caller
// should surface this to the user as "invalid code".
func (m *Manager) ConsumePairing(
	ctx context.Context,
	platform, code string,
) (workspaceID string, err error) {
	code = strings.TrimSpace(code)
	if code == "" {
		return "", ErrPairingInvalid
	}
	p, err := m.store.GetTelegramPairing(ctx, code)
	if err != nil || p == nil {
		return "", ErrPairingInvalid
	}
	if p.Platform != platform {
		return "", ErrPairingInvalid
	}
	if time.Now().UTC().After(p.ExpiresAt) {
		_ = m.store.DeleteTelegramPairing(ctx, code)
		return "", ErrPairingInvalid
	}
	if err := m.store.DeleteTelegramPairing(ctx, code); err != nil {
		// Race: another adapter already consumed. Treat as invalid.
		return "", ErrPairingInvalid
	}
	return p.WorkspaceID, nil
}

// sweepPairings periodically purges expired codes. Called in Manager.Run.
func (m *Manager) sweepPairings(ctx context.Context) {
	n, err := m.store.SweepExpiredTelegramPairings(ctx, time.Now().UTC())
	if err != nil {
		slog.Warn("bridge: sweep pairings", "error", err)
		return
	}
	if n > 0 {
		slog.Info("bridge: swept expired pairings", "count", n)
	}
}

func generatePairingCode() (string, error) {
	// 5 random bytes base32-encoded → 8 chars.
	var raw [5]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw[:]), nil
}
