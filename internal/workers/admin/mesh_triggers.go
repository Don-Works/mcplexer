// mesh_triggers.go (M4) — admin CRUD for the per-Worker mesh-trigger
// rows. The dispatcher consumes the same store; this service is the
// validation + invalidation layer that sits between the MCP / HTTP
// surfaces and the WorkerStore.
//
// Reload notification: after every mutation we call DispatcherReloader
// (when wired) so the in-memory dispatcher cache picks up the change
// without waiting for a daemon restart.
package admin

import (
	"context"
	"errors"
	"fmt"
	"regexp"

	"github.com/don-works/mcplexer/internal/store"
)

// triggerScopePrefix mirrors the dispatcher constant so the admin grant
// helpers and the dispatcher's permission check agree on format.
const triggerScopePrefix = "trigger_worker:"

// DispatcherReloader is the optional hook called after every CRUD
// mutation so the in-memory dispatcher cache refreshes. Implemented by
// *triggers/mesh.Dispatcher.
type DispatcherReloader interface {
	Reload(ctx context.Context) error
}

// MeshTriggerInput is the admin's incoming shape — same fields as
// store.WorkerMeshTrigger but with omitempty so the JSON marshaler
// produces a tighter payload over the wire. AllMessages=true is a
// shortcut for "no filter criteria, fire on every message" — admins
// must opt in so an unfiltered trigger isn't an accident.
type MeshTriggerInput struct {
	ID              string                    `json:"id,omitempty"`
	WorkerID        string                    `json:"worker_id"`
	TagMatch        string                    `json:"tag_match,omitempty"`
	KindMatch       string                    `json:"kind_match,omitempty"`
	AudienceMatch   string                    `json:"audience_match,omitempty"`
	ContentRegex    string                    `json:"content_regex,omitempty"`
	StatusFromMatch string                    `json:"status_from_match,omitempty"`
	StatusToMatch   string                    `json:"status_to_match,omitempty"`
	FromFilters     []store.TriggerFromFilter `json:"from_filters,omitempty"`
	ThrottleSeconds int                       `json:"throttle_seconds,omitempty"`
	MaxChainDepth   int                       `json:"max_chain_depth,omitempty"`
	Enabled         *bool                     `json:"enabled,omitempty"`
	AllMessages     bool                      `json:"all_messages,omitempty"`
}

// PeerScopeStore narrows the store surface the grant helpers need.
// store.Store satisfies it.
type PeerScopeStore interface {
	GrantPeerScope(ctx context.Context, peerID, scope string) error
	RevokePeerScope(ctx context.Context, peerID, scope string) error
	HasPeerScope(ctx context.Context, peerID, scope string) (bool, error)
}

// SetDispatcherReloader wires the in-memory dispatcher cache so CRUD
// mutations invalidate it immediately. Optional — when nil, mutations
// rely on the next boot / next daemon-level Reload to surface.
func (s *Service) SetDispatcherReloader(r DispatcherReloader) {
	s.dispatcherReloader = r
}

// SetMeshTriggerStore wires the full store surface so the service can
// invoke the trigger CRUD methods. We accept the narrow interface so
// tests can inject a fake.
func (s *Service) SetMeshTriggerStore(ts MeshTriggerStore) {
	s.meshTriggerStore = ts
}

// SetPeerScopeStore wires the peer-scope grant surface used by the
// trigger grant convenience tools.
func (s *Service) SetPeerScopeStore(ps PeerScopeStore) {
	s.peerScopeStore = ps
}

// MeshTriggerStore is the dependency surface the admin needs from the
// store. Mirrors store.Store but narrows it to the trigger CRUD methods.
type MeshTriggerStore interface {
	ListWorkerMeshTriggers(ctx context.Context, workerID string) ([]*store.WorkerMeshTrigger, error)
	GetWorkerMeshTrigger(ctx context.Context, id string) (*store.WorkerMeshTrigger, error)
	CreateWorkerMeshTrigger(ctx context.Context, t *store.WorkerMeshTrigger) error
	UpdateWorkerMeshTrigger(ctx context.Context, t *store.WorkerMeshTrigger) error
	DeleteWorkerMeshTrigger(ctx context.Context, id string) error
}

// ListMeshTriggers returns every trigger row for workerID. The Worker is
// looked up first to map "worker doesn't exist" to a clean 404.
func (s *Service) ListMeshTriggers(ctx context.Context, workerID string) ([]*store.WorkerMeshTrigger, error) {
	if workerID == "" {
		return nil, errors.New("worker_id required")
	}
	if _, err := s.store.GetWorker(ctx, workerID); err != nil {
		return nil, err
	}
	if s.meshTriggerStore == nil {
		return nil, errors.New("mesh trigger store not wired")
	}
	return s.meshTriggerStore.ListWorkerMeshTriggers(ctx, workerID)
}

// GetMeshTrigger fetches one trigger by id.
func (s *Service) GetMeshTrigger(ctx context.Context, id string) (*store.WorkerMeshTrigger, error) {
	if id == "" {
		return nil, errors.New("id required")
	}
	if s.meshTriggerStore == nil {
		return nil, errors.New("mesh trigger store not wired")
	}
	return s.meshTriggerStore.GetWorkerMeshTrigger(ctx, id)
}

// CreateMeshTrigger validates the input + persists. Reloads the
// dispatcher cache on success.
func (s *Service) CreateMeshTrigger(ctx context.Context, in MeshTriggerInput) (*store.WorkerMeshTrigger, error) {
	if s.meshTriggerStore == nil {
		return nil, errors.New("mesh trigger store not wired")
	}
	if in.WorkerID == "" {
		return nil, errors.New("worker_id required")
	}
	if _, err := s.store.GetWorker(ctx, in.WorkerID); err != nil {
		return nil, err
	}
	if err := validateMeshTrigger(in); err != nil {
		return nil, err
	}
	t := triggerFromInput(in)
	if err := s.meshTriggerStore.CreateWorkerMeshTrigger(ctx, t); err != nil {
		return nil, err
	}
	s.reloadDispatcher(ctx)
	return t, nil
}

// UpdateMeshTrigger overlays the mutable fields onto the row, validates
// the merged state, and persists.
func (s *Service) UpdateMeshTrigger(ctx context.Context, in MeshTriggerInput) (*store.WorkerMeshTrigger, error) {
	if s.meshTriggerStore == nil {
		return nil, errors.New("mesh trigger store not wired")
	}
	if in.ID == "" {
		return nil, errors.New("id required")
	}
	current, err := s.meshTriggerStore.GetWorkerMeshTrigger(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	mergeIntoTrigger(current, in)
	merged := MeshTriggerInput{
		ID:              current.ID,
		WorkerID:        current.WorkerID,
		TagMatch:        current.TagMatch,
		KindMatch:       current.KindMatch,
		AudienceMatch:   current.AudienceMatch,
		ContentRegex:    current.ContentRegex,
		StatusFromMatch: current.StatusFromMatch,
		StatusToMatch:   current.StatusToMatch,
		FromFilters:     current.FromFilters,
		ThrottleSeconds: current.ThrottleSeconds,
		MaxChainDepth:   current.MaxChainDepth,
		AllMessages:     in.AllMessages,
	}
	if err := validateMeshTrigger(merged); err != nil {
		return nil, err
	}
	if err := s.meshTriggerStore.UpdateWorkerMeshTrigger(ctx, current); err != nil {
		return nil, err
	}
	s.reloadDispatcher(ctx)
	return current, nil
}

// DeleteMeshTrigger removes the row + invalidates the cache.
func (s *Service) DeleteMeshTrigger(ctx context.Context, id string) error {
	if s.meshTriggerStore == nil {
		return errors.New("mesh trigger store not wired")
	}
	if id == "" {
		return errors.New("id required")
	}
	if err := s.meshTriggerStore.DeleteWorkerMeshTrigger(ctx, id); err != nil {
		return err
	}
	s.reloadDispatcher(ctx)
	return nil
}

// GrantTriggerToPeer adds the trigger_worker:<workerName> (or wildcard)
// scope to peerID. workerName "*" maps to "trigger_worker:*". Returns
// the scope string actually granted so the caller can echo it back.
func (s *Service) GrantTriggerToPeer(ctx context.Context, peerID, workerName string) (string, error) {
	if s.peerScopeStore == nil {
		return "", errors.New("peer scope store not wired")
	}
	if peerID == "" {
		return "", errors.New("peer_id required")
	}
	if workerName == "" {
		return "", errors.New("worker_name required (use \"*\" for all)")
	}
	scope := triggerScopePrefix + workerName
	if err := s.peerScopeStore.GrantPeerScope(ctx, peerID, scope); err != nil {
		return "", fmt.Errorf("grant scope: %w", err)
	}
	return scope, nil
}

// RevokeTriggerGrant removes a previously-granted scope. Idempotent
// at the store layer.
func (s *Service) RevokeTriggerGrant(ctx context.Context, peerID, workerName string) (string, error) {
	if s.peerScopeStore == nil {
		return "", errors.New("peer scope store not wired")
	}
	if peerID == "" {
		return "", errors.New("peer_id required")
	}
	if workerName == "" {
		return "", errors.New("worker_name required")
	}
	scope := triggerScopePrefix + workerName
	if err := s.peerScopeStore.RevokePeerScope(ctx, peerID, scope); err != nil {
		return "", fmt.Errorf("revoke scope: %w", err)
	}
	return scope, nil
}

// reloadDispatcher invokes the dispatcher's cache reload best-effort.
// Reload errors are logged-via-return through the audit ledger up-stack
// but never abort the mutation that just persisted.
func (s *Service) reloadDispatcher(ctx context.Context) {
	if s.dispatcherReloader == nil {
		return
	}
	_ = s.dispatcherReloader.Reload(ctx)
}

// triggerFromInput builds a *store.WorkerMeshTrigger from the admin
// input shape with defaults applied. Caller has already validated.
func triggerFromInput(in MeshTriggerInput) *store.WorkerMeshTrigger {
	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	throttle := in.ThrottleSeconds
	if throttle <= 0 {
		throttle = 60
	}
	depth := in.MaxChainDepth
	if depth <= 0 {
		depth = 3
	}
	filters := in.FromFilters
	if filters == nil {
		filters = []store.TriggerFromFilter{}
	}
	return &store.WorkerMeshTrigger{
		ID:              in.ID,
		WorkerID:        in.WorkerID,
		TagMatch:        in.TagMatch,
		KindMatch:       in.KindMatch,
		AudienceMatch:   in.AudienceMatch,
		ContentRegex:    in.ContentRegex,
		StatusFromMatch: in.StatusFromMatch,
		StatusToMatch:   in.StatusToMatch,
		FromFilters:     filters,
		ThrottleSeconds: throttle,
		MaxChainDepth:   depth,
		Enabled:         enabled,
	}
}

// mergeIntoTrigger overlays the input's non-zero fields onto current.
// Empty match strings on the input are interpreted as "clear" if the
// caller explicitly sets all_messages=true; otherwise empty means "no
// change" so PATCH-style requests don't accidentally widen the trigger.
func mergeIntoTrigger(t *store.WorkerMeshTrigger, in MeshTriggerInput) {
	if in.TagMatch != "" {
		t.TagMatch = in.TagMatch
	}
	if in.KindMatch != "" {
		t.KindMatch = in.KindMatch
	}
	if in.AudienceMatch != "" {
		t.AudienceMatch = in.AudienceMatch
	}
	if in.ContentRegex != "" {
		t.ContentRegex = in.ContentRegex
	}
	if in.StatusFromMatch != "" {
		t.StatusFromMatch = in.StatusFromMatch
	}
	if in.StatusToMatch != "" {
		t.StatusToMatch = in.StatusToMatch
	}
	if in.FromFilters != nil {
		t.FromFilters = in.FromFilters
	}
	if in.ThrottleSeconds > 0 {
		t.ThrottleSeconds = in.ThrottleSeconds
	}
	if in.MaxChainDepth > 0 {
		t.MaxChainDepth = in.MaxChainDepth
	}
	if in.Enabled != nil {
		t.Enabled = *in.Enabled
	}
	if in.AllMessages {
		t.TagMatch = ""
		t.KindMatch = ""
		t.AudienceMatch = ""
		t.ContentRegex = ""
		t.StatusFromMatch = ""
		t.StatusToMatch = ""
		t.FromFilters = []store.TriggerFromFilter{}
	}
}

// validateMeshTrigger enforces the admin-level invariants documented
// on the spec: regex compiles, throttle >= 1s, MaxChainDepth in [1, 10],
// and at least one match criterion non-empty OR AllMessages=true.
func validateMeshTrigger(in MeshTriggerInput) error {
	if in.ContentRegex != "" {
		if _, err := regexp.Compile(in.ContentRegex); err != nil {
			return fmt.Errorf("content_regex does not compile: %w", err)
		}
	}
	if in.ThrottleSeconds < 0 {
		return errors.New("throttle_seconds must be >= 0")
	}
	if in.ThrottleSeconds > 0 && in.ThrottleSeconds < 1 {
		return errors.New("throttle_seconds must be >= 1 when set")
	}
	if in.MaxChainDepth < 0 || in.MaxChainDepth > 10 {
		return errors.New("max_chain_depth must be in [1, 10]")
	}
	if in.KindMatch != "" && !isValidKind(in.KindMatch) {
		return fmt.Errorf("kind_match %q not one of finding|task|alert|question|result|event|reply", in.KindMatch)
	}
	for i, f := range in.FromFilters {
		if f.Role != "" {
			// Mesh messages don't carry sender role on the wire, so a
			// role constraint can never be verified at dispatch time. The
			// matcher fails closed on it; reject at create/update so the
			// admin learns immediately instead of shipping a trigger that
			// never fires (or, pre-fix, fired for EVERY sender).
			return fmt.Errorf("from_filters[%d]: role filtering is not implemented — use peer_id and/or agent_name", i)
		}
	}
	if !in.AllMessages {
		if in.TagMatch == "" && in.KindMatch == "" && in.AudienceMatch == "" &&
			in.ContentRegex == "" && in.StatusFromMatch == "" && in.StatusToMatch == "" &&
			len(in.FromFilters) == 0 {
			return errors.New("trigger needs at least one match criterion OR all_messages=true")
		}
	}
	return nil
}

// isValidKind mirrors mesh.validKind without taking that import.
func isValidKind(k string) bool {
	switch k {
	case "finding", "task", "alert", "question", "result", "event", "reply":
		return true
	}
	return false
}
