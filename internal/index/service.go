package index

import (
	"context"
	"log/slog"
	"strings"
	"sync"

	"github.com/don-works/mcplexer/internal/store"
)

// Service is the codebase indexer. Stage 0 ships the type, constructor, and
// full public method set as compiling stubs; every method currently reports
// ErrNotBuilt (or a root-safety error). Stage 1 (Agent A) replaces the stub
// bodies with the real walk / extract / rank pipeline described in plan §7,
// keeping these signatures frozen.
type Service struct {
	store  store.CodeIndexStore
	logger *slog.Logger

	// Per-workspace single-flight. guard protects the two maps; locks holds
	// one mutex per workspace so builds serialize; inflight marks a build in
	// progress so a concurrent query can wait or fail fast with
	// ErrBuildInProgress. Stage 0 wires the plumbing (buildLock + ensureBuilt
	// read/write these); Stage 1 fills the real build-and-wait bodies.
	guard    sync.Mutex
	locks    map[string]*sync.Mutex
	inflight map[string]bool
}

// NewService constructs a Service over the given store. A nil logger falls
// back to slog.Default().
func NewService(st store.CodeIndexStore, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{
		store:    st,
		logger:   logger,
		locks:    make(map[string]*sync.Mutex),
		inflight: make(map[string]bool),
	}
}

// validateRoot enforces the D8 gate: refuse the empty / "/" root (the seeded
// global workspace). Stage 1 additionally requires the root to be an existing
// absolute directory.
func validateRoot(root string) error {
	if strings.TrimSpace(root) == "" || root == "/" {
		return ErrRootUnsafe
	}
	return nil
}

// buildLock returns (creating on first use) the per-workspace build mutex.
// Stage 1's Build/ensureBuilt lock it for single-flight; stage 0 keeps the
// map plumbing live.
func (s *Service) buildLock(workspaceID string) *sync.Mutex {
	s.guard.Lock()
	defer s.guard.Unlock()
	m, ok := s.locks[workspaceID]
	if !ok {
		m = &sync.Mutex{}
		s.locks[workspaceID] = m
	}
	return m
}

// ensureBuilt is the auto-build gate every query method funnels through
// (plan D3). Stage 0 stub: it validates the root, fails fast with
// ErrBuildInProgress when a build is already flagged, and otherwise reports
// ErrNotBuilt. Stage 1 replaces the "report not-built" branch with an
// incremental Build under buildLock.
func (s *Service) ensureBuilt(ctx context.Context, workspaceID, root string) error {
	if err := validateRoot(root); err != nil {
		return err
	}
	s.guard.Lock()
	building := s.inflight[workspaceID]
	s.guard.Unlock()
	if building {
		return ErrBuildInProgress
	}
	if _, err := s.store.GetCodeIndexBuild(ctx, workspaceID); err != nil {
		s.logger.Debug("code index not built", "workspace", workspaceID)
	}
	return ErrNotBuilt
}

// Build is a stage-0 stub. Stage 1 runs the incremental build under the
// per-workspace lock + inflight single-flight.
func (s *Service) Build(ctx context.Context, req BuildRequest) (*BuildResult, error) {
	if err := validateRoot(req.Root); err != nil {
		return nil, err
	}
	_ = s.buildLock(req.WorkspaceID)
	return nil, ErrNotBuilt
}

// Symbols is a stage-0 stub. Stage 1 searches the symbol FTS index.
func (s *Service) Symbols(ctx context.Context, req SymbolsRequest) ([]SymbolHit, error) {
	return nil, s.ensureBuilt(ctx, req.WorkspaceID, req.Root)
}

// Deps is a stage-0 stub. Stage 1 walks the file-level import graph.
func (s *Service) Deps(ctx context.Context, req DepsRequest) (*DepsResult, error) {
	return nil, s.ensureBuilt(ctx, req.WorkspaceID, req.Root)
}

// TestsFor is a stage-0 stub. Stage 1 applies the test-ownership heuristics.
func (s *Service) TestsFor(ctx context.Context, workspaceID, root, file string) (*TestsForResult, error) {
	return nil, s.ensureBuilt(ctx, workspaceID, root)
}

// Summary is a stage-0 stub. Stage 1 assembles the heuristic file card.
func (s *Service) Summary(ctx context.Context, workspaceID, root, file string) (*FileSummary, error) {
	return nil, s.ensureBuilt(ctx, workspaceID, root)
}

// RecentChanges is a stage-0 stub. Stage 1 reads git log for churn.
func (s *Service) RecentChanges(ctx context.Context, req RecentChangesRequest) (*RecentChangesResult, error) {
	return nil, s.ensureBuilt(ctx, req.WorkspaceID, req.Root)
}

// MapFailure is a stage-0 stub. Stage 1 parses + ranks the failure text.
func (s *Service) MapFailure(ctx context.Context, workspaceID, root, text string, limit int) ([]FailureCandidate, error) {
	return nil, s.ensureBuilt(ctx, workspaceID, root)
}

// ContextPack is a stage-0 stub. Stage 1 builds the token-budgeted pack.
func (s *Service) ContextPack(ctx context.Context, req ContextRequest) (*ContextPack, error) {
	return nil, s.ensureBuilt(ctx, req.WorkspaceID, req.Root)
}

// Status is a stage-0 stub reporting not-built. Stage 1 returns the real
// freshness verdict (git HEAD compare, dirty count, counters).
func (s *Service) Status(ctx context.Context, workspaceID, root string) (*Status, error) {
	return nil, s.ensureBuilt(ctx, workspaceID, root)
}
