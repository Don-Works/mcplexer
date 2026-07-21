package approval

import (
	"context"
	"errors"

	"github.com/don-works/mcplexer/internal/store"
)

var (
	ErrNoPeerTargets = errors.New("no peer targets")
	ErrPeerTimeout   = errors.New("peer approval timed out")
)

type PeerApprover interface {
	Ask(ctx context.Context, a *store.ToolApproval, targets []string) (approved bool, reason string, err error)
}
