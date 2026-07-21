package skillregistry

import (
	"errors"
	"fmt"

	"github.com/don-works/mcplexer/internal/store"
)

// ErrCompositionNotPortable marks a composed root that cannot be transferred
// by the v1 registry sync protocols. Those protocols carry one root skill but
// not the exact dependency closure required to reproduce its includes.
var ErrCompositionNotPortable = errors.New("skillregistry: composition is not portable over protocol v1")

// CheckSyncPortableBody validates a raw SKILL.md and rejects composed roots.
// Callers must run this before producing a dry-run plan or mutating a registry.
func CheckSyncPortableBody(name, body string) error {
	parsed, err := Parse(body, name)
	if err != nil {
		return err
	}
	return checkSyncPortableParsed(parsed)
}

func checkSyncPortableEntry(entry *store.SkillRegistryEntry) error {
	if entry == nil {
		return errors.New("skillregistry: portability check entry is nil")
	}
	return CheckSyncPortableBody(entry.Name, entry.Body)
}

func checkSyncPortableParsed(parsed *Parsed) error {
	if parsed == nil || len(parsed.Extra.Includes) == 0 {
		return nil
	}
	return fmt.Errorf(
		"%w: %s declares %d include(s); dependency-closure transfer unsupported in protocol v1",
		ErrCompositionNotPortable, parsed.Name, len(parsed.Extra.Includes),
	)
}
