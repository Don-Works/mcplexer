package tasks

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

var ErrVisibilityApprovalRequired = errors.New("task visibility: widening requires human approval")

// SetVisibility lets an agent choose the audience only inside the workspace's
// publication policy. Narrowing is always allowed. Widening an existing task
// fails closed when the policy requires a human; the dashboard remains the
// explicit approval/override surface.
func (s *Service) SetVisibility(
	ctx context.Context,
	workspaceID string,
	taskID string,
	visibility string,
	audience []string,
) (*store.TaskAccess, error) {
	if s == nil || s.collaborationStore == nil {
		return nil, fmt.Errorf("collaboration is not enabled")
	}
	task, err := s.Get(ctx, workspaceID, taskID)
	if err != nil {
		return nil, err
	}
	if err := s.authorizeSharedTaskAction(ctx, task, store.CapabilityTasksShare); err != nil {
		return nil, err
	}
	if !store.ValidTaskVisibility(visibility) {
		return nil, fmt.Errorf("visibility must be private, restricted, or workspace")
	}
	owner, err := s.localCollaborationOwner(ctx)
	if err != nil {
		return nil, err
	}
	current, err := s.collaborationStore.GetTaskAccess(ctx, task.ID)
	if err != nil {
		return nil, err
	}
	if visibility != store.TaskVisibilityPrivate {
		if current.ShareID == "" {
			return nil, fmt.Errorf("task workspace is not collaboration-enabled")
		}
		policy, err := s.collaborationStore.GetWorkspacePublicationPolicy(ctx, current.ShareID)
		if err != nil {
			return nil, err
		}
		if visibilityRank(visibility) > visibilityRank(policy.AgentVisibilityCeiling) {
			return nil, fmt.Errorf("requested visibility %q exceeds agent ceiling %q", visibility, policy.AgentVisibilityCeiling)
		}
		if policy.WideningRequiresApproval && visibilityRank(visibility) > visibilityRank(current.Visibility) {
			return nil, ErrVisibilityApprovalRequired
		}
	}
	resolvedAudience, err := s.resolveVisibilityAudience(ctx, audience)
	if err != nil {
		return nil, err
	}
	return s.collaborationStore.SetTaskVisibility(ctx, store.TaskVisibilityChange{
		TaskID: task.ID, Visibility: visibility,
		AudiencePrincipalIDs: resolvedAudience,
		ActorPrincipalID:     owner.ID, At: time.Now().UTC(),
	})
}

// PublishSystemTask widens a SYSTEM-created task to workspace visibility so it
// replicates to mirrored peers. It deliberately bypasses the agent publication
// ceiling and the widening-approval gate that SetVisibility enforces: a
// monitoring incident task is minted by the daemon, not an agent, and must reach
// the peer dashboard without a human approving each one. A freshly created task
// is forced private (see sqlite task.go), and the replication send-path denies a
// private task to every non-owner principal — so a system task that is never
// widened silently never mirrors.
//
// It is a no-op (never an error) when collaboration is off, the workspace has no
// ACTIVE share (there is no peer to replicate to), or the task is already
// non-private (idempotent under the incident ensurer's convergence). It consults
// no model.
func (s *Service) PublishSystemTask(ctx context.Context, taskID string) error {
	if s == nil || s.collaborationStore == nil {
		return nil
	}
	access, err := s.collaborationStore.GetTaskAccess(ctx, taskID)
	if err != nil {
		return err
	}
	// Guard: widen only where there is a peer to replicate to. A non-collab
	// workspace has no active share; widening there changes visibility semantics
	// for no benefit.
	if access.ShareID == "" || access.Visibility != store.TaskVisibilityPrivate {
		return nil
	}
	owner, err := s.localCollaborationOwner(ctx)
	if err != nil {
		return err
	}
	_, err = s.collaborationStore.SetTaskVisibility(ctx, store.TaskVisibilityChange{
		TaskID: taskID, Visibility: store.TaskVisibilityWorkspace,
		ActorPrincipalID: owner.ID, At: time.Now().UTC(),
	})
	return err
}

func (s *Service) localCollaborationOwner(ctx context.Context) (*store.Principal, error) {
	principals, err := s.collaborationStore.ListPrincipals(ctx)
	if err != nil {
		return nil, err
	}
	for i := range principals {
		if principals[i].IsLocalOwner && principals[i].Status == store.PrincipalStatusActive {
			return &principals[i], nil
		}
	}
	return nil, fmt.Errorf("local collaboration owner: %w", store.ErrNotFound)
}

// Audience entries may be stable principal IDs or exact display names. Names
// are ergonomic for models but must resolve uniquely; ambiguity fails closed.
func (s *Service) resolveVisibilityAudience(ctx context.Context, audience []string) ([]string, error) {
	if len(audience) == 0 {
		return nil, nil
	}
	principals, err := s.collaborationStore.ListPrincipals(ctx)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]store.Principal, len(principals))
	byName := make(map[string][]store.Principal, len(principals))
	for _, principal := range principals {
		byID[principal.ID] = principal
		name := strings.ToLower(strings.TrimSpace(principal.DisplayName))
		byName[name] = append(byName[name], principal)
	}
	resolved := make([]string, 0, len(audience))
	seen := make(map[string]struct{}, len(audience))
	for _, raw := range audience {
		value := strings.TrimSpace(raw)
		principal, ok := byID[value]
		if !ok {
			matches := byName[strings.ToLower(value)]
			if len(matches) != 1 {
				return nil, fmt.Errorf("audience %q does not resolve to one principal", value)
			}
			principal = matches[0]
		}
		if principal.IsLocalOwner || principal.Status != store.PrincipalStatusActive {
			return nil, fmt.Errorf("audience %q is not an active remote principal", value)
		}
		if _, duplicate := seen[principal.ID]; duplicate {
			continue
		}
		seen[principal.ID] = struct{}{}
		resolved = append(resolved, principal.ID)
	}
	return resolved, nil
}
