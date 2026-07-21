package harnesssync

import (
	"context"

	"github.com/don-works/mcplexer/internal/skillregistry"
)

const usingMcplexerSkillName = "using-mcplexer"

// DefaultUsingMcplexerVersion is used when the skill registry is
// unavailable or the using-mcplexer skill has not been seeded yet.
const DefaultUsingMcplexerVersion = 1

// UsingMcplexerRegistryVersion resolves the current head version of the
// using-mcplexer skill from the registry. Falls back to
// DefaultUsingMcplexerVersion when reg is nil or the skill is missing.
func UsingMcplexerRegistryVersion(ctx context.Context, reg *skillregistry.Registry) int {
	if reg == nil {
		return DefaultUsingMcplexerVersion
	}
	entry, err := reg.Get(ctx, skillregistry.AdminScope(), usingMcplexerSkillName, skillregistry.VersionRef{Latest: true})
	if err != nil || entry == nil || entry.Version <= 0 {
		return DefaultUsingMcplexerVersion
	}
	return entry.Version
}
