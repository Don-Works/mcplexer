package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/don-works/mcplexer/internal/store"
)

// whoamiResponse is the JSON shape of mcplexer__whoami. On a fresh install
// the self row may not exist yet (the boot path creates it on first run);
// we return a structured empty result with self_bootstrapped=false so an
// admin agent that calls whoami mid-setup can detect the gap and prompt
// the user, rather than receiving a generic ErrNotFound error envelope.
type whoamiResponse struct {
	User             *store.User `json:"user"`
	SelfBootstrapped bool        `json:"self_bootstrapped"`
}

// handleWhoami returns the bootstrap-self user row. Read-only; safe to
// call from a read-only admin session (CWD-gated like every mcplexer__*).
func handleWhoami(
	ctx context.Context, s store.Store, _ json.RawMessage,
) (json.RawMessage, error) {
	u, err := s.GetSelfUser(ctx)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return jsonResult(whoamiResponse{User: nil, SelfBootstrapped: false})
		}
		return nil, fmt.Errorf("get self user: %w", err)
	}
	return jsonResult(whoamiResponse{User: u, SelfBootstrapped: true})
}

// handleListUsers returns every user row, self first then ordered by
// display_name (the UserStore.ListUsers contract).
func handleListUsers(
	ctx context.Context, s store.Store, _ json.RawMessage,
) (json.RawMessage, error) {
	users, err := s.ListUsers(ctx)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	return jsonResult(users)
}

// handleGetUser returns one user by id. Unknown id surfaces as a
// structured error so the agent can distinguish "no such user" from a
// store failure (which would itself be unusual — UserStore is local).
func handleGetUser(
	ctx context.Context, s store.Store, args json.RawMessage,
) (json.RawMessage, error) {
	id, err := requireID(args)
	if err != nil {
		return nil, err
	}
	u, err := s.GetUser(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return errorResult("user not found"), nil
		}
		return nil, fmt.Errorf("get user: %w", err)
	}
	return jsonResult(u)
}

// handleListUserDevices returns the peers (devices) linked to one user.
// We pre-check the user row so an empty result can't be confused with a
// typo — a missing user surfaces as "user not found", whereas a known
// user with no linked peers surfaces as [].
func handleListUserDevices(
	ctx context.Context, s store.Store, args json.RawMessage,
) (json.RawMessage, error) {
	id, err := requireID(args)
	if err != nil {
		return nil, err
	}
	if _, err := s.GetUser(ctx, id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return errorResult("user not found"), nil
		}
		return nil, fmt.Errorf("get user: %w", err)
	}
	peers, err := s.ListPeersForUser(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("list peers for user: %w", err)
	}
	return jsonResult(peers)
}
