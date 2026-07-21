package tasks

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/don-works/mcplexer/internal/store"
)

// ErrWorkspaceMembershipDenied is returned by local mirror mutations when
// the accepted invitation does not advertise the needed capability. This is
// a UX guard, not the trust boundary: the workspace home always re-checks its
// live grant and access epoch before accepting a publish.
var ErrWorkspaceMembershipDenied = errors.New("shared workspace permission denied")

func (s *Service) workspaceMembership(
	ctx context.Context, workspaceID string,
) (*store.WorkspaceMembership, bool, error) {
	if s == nil || s.membershipStore == nil || strings.TrimSpace(workspaceID) == "" {
		return nil, false, nil
	}
	membership, err := s.membershipStore.GetWorkspaceMembershipByLocalWorkspaceID(ctx, workspaceID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("resolve shared workspace permissions: %w", err)
	}
	if membership.Status != store.WorkspaceShareStatusActive {
		return membership, true, fmt.Errorf("%w: workspace membership is revoked", ErrWorkspaceMembershipDenied)
	}
	return membership, true, nil
}

func membershipHas(capabilities []string, wanted string) bool {
	for _, capability := range capabilities {
		if capability == wanted {
			return true
		}
	}
	return false
}

func requireMembershipCapabilities(membership *store.WorkspaceMembership, all, any []string) error {
	if membership == nil {
		return nil
	}
	for _, capability := range all {
		if !membershipHas(membership.Capabilities, capability) {
			return fmt.Errorf("%w: %s is required", ErrWorkspaceMembershipDenied, capability)
		}
	}
	if len(any) > 0 {
		for _, capability := range any {
			if membershipHas(membership.Capabilities, capability) {
				return nil
			}
		}
		return fmt.Errorf("%w: one of %s is required", ErrWorkspaceMembershipDenied, strings.Join(any, ", "))
	}
	return nil
}

func (s *Service) authorizeSharedWorkspaceDraft(ctx context.Context, workspaceID string) error {
	membership, shared, err := s.workspaceMembership(ctx, workspaceID)
	if err != nil || !shared {
		return err
	}
	return requireMembershipCapabilities(membership, nil, []string{
		store.CapabilityTasksCreate,
		store.CapabilityTasksPublish,
	})
}

func (s *Service) authorizeSharedTaskEdit(
	ctx context.Context, task *store.Task, extra ...string,
) error {
	if task == nil {
		return store.ErrNotFound
	}
	membership, shared, err := s.workspaceMembership(ctx, task.WorkspaceID)
	if err != nil || !shared {
		return err
	}
	if task.OriginPeerID == membership.HomePeerID || task.SourceKind == store.TaskSourcePeerImport {
		all := append([]string{store.CapabilityTasksEdit}, extra...)
		return requireMembershipCapabilities(membership, all, nil)
	}
	if err := requireMembershipCapabilities(membership, extra, nil); err != nil {
		return err
	}
	return requireMembershipCapabilities(membership, nil, []string{
		store.CapabilityTasksCreate,
		store.CapabilityTasksPublish,
	})
}

func (s *Service) authorizeSharedTaskAction(
	ctx context.Context, task *store.Task, required ...string,
) error {
	if task == nil {
		return store.ErrNotFound
	}
	membership, shared, err := s.workspaceMembership(ctx, task.WorkspaceID)
	if err != nil || !shared {
		return err
	}
	if task.OriginPeerID == membership.HomePeerID || task.SourceKind == store.TaskSourcePeerImport {
		return requireMembershipCapabilities(membership, required, nil)
	}
	return requireMembershipCapabilities(membership, nil, []string{
		store.CapabilityTasksCreate,
		store.CapabilityTasksPublish,
	})
}

func patchChangesAssignee(p UpdatePatch) bool {
	if p.Assignee != nil {
		return true
	}
	for _, field := range p.Clear {
		if strings.EqualFold(strings.TrimSpace(field), "assignee") {
			return true
		}
	}
	return false
}
