//go:build !p2p

package p2p

import (
	"context"

	"github.com/don-works/mcplexer/internal/store"
)

func NewCollaborationInviteService(host *Host, collaborationStore store.CollaborationStore) *CollaborationInviteService {
	return &CollaborationInviteService{host: host, store: collaborationStore}
}

func (s *CollaborationInviteService) join(_ context.Context, _ CollaborationJoinOptions) (*CollaborationJoinResult, error) {
	return nil, ErrP2PNotBuiltIn
}
