package skillregistry

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/don-works/mcplexer/internal/store"
)

// ErrCompositionReferenced means a delete would break at least one active,
// exact composition pin.
var ErrCompositionReferenced = errors.New("skill version is referenced by active composition")

// guardCompositionReferences scans every active registry version, including
// shadowed and non-head entries. It rejects deletion of an exact version that
// an include pins. This is registered by New through the generic delete-guard
// seam so all Registry.SoftDelete callers get the same integrity protection.
func (r *Registry) guardCompositionReferences(
	ctx context.Context, workspaceID *string, name string, version int,
) error {
	targetVersions, err := r.activeDeleteTargets(ctx, workspaceID, name, version)
	if err != nil {
		return err
	}
	if len(targetVersions) == 0 {
		// Preserve the store's normal not-found behaviour. There is no active
		// target whose durability this guard can improve.
		return nil
	}

	heads, err := r.store.ListSkillRegistryScopeHeads(ctx, AdminScope(), 0)
	if err != nil {
		return fmt.Errorf("scan composition dependents: list names: %w", err)
	}
	names := make(map[string]struct{}, len(heads))
	for _, head := range heads {
		names[head.Name] = struct{}{}
	}
	orderedNames := make([]string, 0, len(names))
	for candidateName := range names {
		orderedNames = append(orderedNames, candidateName)
	}
	sort.Strings(orderedNames)

	var blockers []string
	for _, candidateName := range orderedNames {
		versions, listErr := r.store.ListSkillRegistryVersions(ctx, AdminScope(), candidateName, false)
		if listErr != nil {
			return fmt.Errorf("scan composition dependents: list %s versions: %w", candidateName, listErr)
		}
		for i := range versions {
			entry := &versions[i]
			if entryDeletedByRequest(entry, workspaceID, name, targetVersions) {
				continue
			}
			parsed, parseErr := Parse(entry.Body, entry.Name)
			if parseErr != nil {
				return fmt.Errorf("scan composition dependents: parse %s: %w",
					compositionEntryLabel(entry), parseErr)
			}
			for _, include := range parsed.Extra.Includes {
				if include.Skill != name {
					continue
				}
				if _, deleting := targetVersions[include.Version]; !deleting {
					continue
				}
				var includeWorkspaceID *string
				if include.Scope == "same" {
					includeWorkspaceID = entry.WorkspaceID
				}
				if !sameCompositionScope(includeWorkspaceID, workspaceID) {
					continue
				}
				blockers = append(blockers, fmt.Sprintf("%s include %q",
					compositionEntryLabel(entry), include.ID))
			}
		}
	}
	if len(blockers) == 0 {
		return nil
	}
	sort.Strings(blockers)
	target := compositionTargetLabel(workspaceID, name, version)
	return fmt.Errorf("%w: cannot delete %s; pinned by %s",
		ErrCompositionReferenced, target, strings.Join(blockers, ", "))
}

func (r *Registry) activeDeleteTargets(
	ctx context.Context, workspaceID *string, name string, version int,
) (map[int]struct{}, error) {
	versions, err := r.store.ListSkillRegistryVersions(ctx, AdminScope(), name, false)
	if err != nil {
		return nil, fmt.Errorf("scan composition target %s: %w", name, err)
	}
	targets := make(map[int]struct{})
	for i := range versions {
		entry := &versions[i]
		if !sameCompositionScope(entry.WorkspaceID, workspaceID) {
			continue
		}
		if version == 0 || entry.Version == version {
			targets[entry.Version] = struct{}{}
		}
	}
	return targets, nil
}

func entryDeletedByRequest(
	entry *store.SkillRegistryEntry,
	workspaceID *string,
	name string,
	targetVersions map[int]struct{},
) bool {
	if entry == nil || entry.Name != name || !sameCompositionScope(entry.WorkspaceID, workspaceID) {
		return false
	}
	_, deleting := targetVersions[entry.Version]
	return deleting
}

func sameCompositionScope(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func compositionEntryLabel(entry *store.SkillRegistryEntry) string {
	if entry == nil {
		return "<nil>"
	}
	return fmt.Sprintf("%s/%s@v%d", scopeLabel(entry.WorkspaceID), entry.Name, entry.Version)
}

func compositionTargetLabel(workspaceID *string, name string, version int) string {
	label := fmt.Sprintf("%s/%s", scopeLabel(workspaceID), name)
	if version == 0 {
		return label + " (all active versions)"
	}
	return fmt.Sprintf("%s@v%d", label, version)
}
