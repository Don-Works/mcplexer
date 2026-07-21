package gateway

import (
	"context"

	"github.com/don-works/mcplexer/internal/store"
)

func (m *mockStore) ListSkillRegistryScopeHeads(
	context.Context, store.SkillScope, int,
) ([]store.SkillRegistryEntry, error) {
	return nil, nil
}
