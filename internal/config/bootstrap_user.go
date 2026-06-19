package config

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/don-works/mcplexer/internal/store"
)

// SelfUserIDEnvVar is the env var that pins the local user_id at
// daemon-boot time. Lets the bulletproof e2e suite stand up multiple
// nodes with the same user identity (Tier 1 same-user pair) without
// post-hoc admin RPCs. When unset, BootstrapSelfUser falls back to the
// previous "fresh UUID on first run" behavior.
const SelfUserIDEnvVar = "MCPLEXER_SELF_USER_ID"

// SelfOrgEnvVar is the env var that pins the local org label. Tier 2
// resolution (same_org vs cross_org) compares this against the peer's
// org. Empty means "no org configured" — the tier resolver then treats
// every non-Tier-1 peer as cross_org (the most-restrictive default).
const SelfOrgEnvVar = "MCPLEXER_SELF_ORG"

// SelfDisplayNameEnvVar is an optional override for the local user's
// display_name. Used by docker-compose to pin Alice/Bob/Carol labels
// in the bulletproof test rig.
const SelfDisplayNameEnvVar = "MCPLEXER_SELF_USER_DISPLAY_NAME"

// BootstrapSelfUser ensures exactly one users.is_self=1 row exists. On a
// fresh install (no row), it creates one with a fresh UUID v4 and a
// display_name pulled from settings.display_name (falling back to the host
// name and finally "user"). Idempotent: a no-op when self already exists.
//
// Returns the resulting *store.User so callers can wire it to identity-aware
// services (pairing flow, mesh, etc.).
func BootstrapSelfUser(ctx context.Context, st store.UserStore, settings store.SettingsStore) (*store.User, error) {
	self, err := st.GetSelfUser(ctx)
	if err == nil {
		return self, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return nil, fmt.Errorf("get self user: %w", err)
	}

	display := resolveSelfDisplayName(ctx, settings)
	// MCPLEXER_SELF_USER_ID lets the operator pin the user_id at boot
	// time. Critical for the bulletproof e2e rig (two daemons with the
	// same SELF_USER_ID form a Tier 1 same-user pair). When unset we
	// keep the historical behavior — a fresh UUID v4 per fresh install.
	userID := strings.TrimSpace(os.Getenv(SelfUserIDEnvVar))
	if userID == "" {
		userID = uuid.NewString()
	}
	u := &store.User{
		UserID:      userID,
		DisplayName: display,
		CreatedAt:   time.Now().UTC(),
		IsSelf:      true,
	}
	if err := st.CreateUser(ctx, u); err != nil {
		return nil, fmt.Errorf("create self user: %w", err)
	}
	return u, nil
}

// resolveSelfDisplayName picks the best available human-friendly name for
// the local user: settings.display_name (if non-empty) → sanitised hostname
// (via defaultDisplayName, same logic Settings uses for first-boot defaults)
// → "user" as the ultimate fallback.
//
// Critical: never return raw os.Hostname() — on macOS+Tailscale the hostname
// is e.g. "example-host.invalid" with spaces, which downstream display_name
// validation rejects and which propagates onto paired peers as garbage. Use
// defaultDisplayName so the sanitised "mac" form is what reaches the wire.
func resolveSelfDisplayName(ctx context.Context, settings store.SettingsStore) string {
	// Env override wins so docker-compose can pin
	// "Alice (AcmeCo, machine 1)" etc. Trim so accidental whitespace in
	// the YAML doesn't propagate to peers.
	if env := strings.TrimSpace(os.Getenv(SelfDisplayNameEnvVar)); env != "" {
		return env
	}
	if settings != nil {
		if name := readSettingsDisplayName(ctx, settings); name != "" {
			return name
		}
	}
	if name := defaultDisplayName(); name != "" && name != "device" {
		return name
	}
	return "user"
}

// readSettingsDisplayName extracts the display_name field from the raw
// settings JSON without hard-coupling to the config.Settings struct shape
// (the field is owned by a sibling agent's branch and may not yet be in
// our struct definition).
func readSettingsDisplayName(ctx context.Context, settings store.SettingsStore) string {
	raw, err := settings.GetSettings(ctx)
	if err != nil || len(raw) == 0 {
		return ""
	}
	var probe struct {
		DisplayName string `json:"display_name"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return ""
	}
	return strings.TrimSpace(probe.DisplayName)
}

// SyntheticUserIDForPeer returns a deterministic UUID-shaped ID derived
// from a peer ID. Used in the backward-compat path when a remote peer
// pairs without sending its user_id (older binaries). The output is stable
// per peer so re-pairs don't create duplicate user rows for the same
// machine.
func SyntheticUserIDForPeer(peerID string) string {
	sum := sha256.Sum256([]byte("mcplexer.user.synthetic:" + peerID))
	hex := hex.EncodeToString(sum[:16])
	// Format as a v4-shaped UUID (8-4-4-4-12). The variant/version bits are
	// not RFC-4122-correct because the input is content-addressed, not
	// random; the goal is just a 36-char ID compatible with the same column.
	return fmt.Sprintf("%s-%s-%s-%s-%s",
		hex[0:8], hex[8:12], hex[12:16], hex[16:20], hex[20:32])
}
