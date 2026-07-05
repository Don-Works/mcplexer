package index

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// Service is the codebase indexer backing the builtin index__* tools. It
// enumerates a workspace's repo, extracts Go + TS/JS symbols and file-level
// import edges into a store.CodeIndexStore, and answers symbol / dep / test /
// summary / failure / context queries over that map. Builds are single-flighted
// per workspace; queries auto-build once when no build row exists (plan D3).
type Service struct {
	store  store.CodeIndexStore
	logger *slog.Logger

	// guard protects inflight; inflight[workspaceID] marks a build in progress
	// so a concurrent build/query waits for it (up to buildWait) rather than
	// racing (single-flight, P4).
	guard     sync.Mutex
	inflight  map[string]bool
	buildWait time.Duration
}

// NewService constructs a Service over the given store. A nil logger falls back
// to slog.Default().
func NewService(st store.CodeIndexStore, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{
		store: st, logger: logger,
		inflight: make(map[string]bool), buildWait: 8 * time.Second,
	}
}

// validateRoot enforces the D8 gate: refuse the empty / "/" root (the seeded
// global workspace).
func validateRoot(root string) error {
	if strings.TrimSpace(root) == "" || root == "/" {
		return ErrRootUnsafe
	}
	return nil
}

// requireDir additionally requires root to be an existing absolute directory —
// applied before a build reads the filesystem.
func requireDir(root string) error {
	if !filepath.IsAbs(root) {
		return ErrRootUnsafe
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return ErrRootUnsafe
	}
	return nil
}

// acquire claims the per-workspace build flight, waiting up to 8s for an
// in-flight build before giving up with ErrBuildInProgress (single-shot callers
// cannot retry, P4).
func (s *Service) acquire(ctx context.Context, workspaceID string) error {
	deadline := time.Now().Add(s.buildWait)
	for {
		s.guard.Lock()
		if !s.inflight[workspaceID] {
			s.inflight[workspaceID] = true
			s.guard.Unlock()
			return nil
		}
		s.guard.Unlock()
		if time.Now().After(deadline) {
			return ErrBuildInProgress
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// release clears the build flight.
func (s *Service) release(workspaceID string) {
	s.guard.Lock()
	delete(s.inflight, workspaceID)
	s.guard.Unlock()
}

// ensureBuilt is the auto-build gate every symbol/dep/test/summary/failure/
// context query funnels through (plan D3). A build row means "ready"; otherwise
// it runs a first incremental build under single-flight.
func (s *Service) ensureBuilt(ctx context.Context, workspaceID, root string) error {
	if err := validateRoot(root); err != nil {
		return err
	}
	if _, err := s.store.GetCodeIndexBuild(ctx, workspaceID); err == nil {
		return nil
	}
	_, err := s.Build(ctx, BuildRequest{WorkspaceID: workspaceID, Root: root})
	return err
}

// Build runs (or force-rebuilds) the incremental index under the per-workspace
// single-flight lock.
func (s *Service) Build(ctx context.Context, req BuildRequest) (*BuildResult, error) {
	if err := validateRoot(req.Root); err != nil {
		return nil, err
	}
	if err := requireDir(req.Root); err != nil {
		return nil, err
	}
	if err := s.acquire(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	defer s.release(req.WorkspaceID)
	return s.runBuild(ctx, req)
}

// filePathSet returns the set of indexed root-relative file paths for a
// workspace, used by test-ownership, failure-mapping, and context assembly.
func (s *Service) filePathSet(ctx context.Context, workspaceID string) (map[string]bool, error) {
	stats, err := s.store.ListCodeIndexFileStats(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	set := make(map[string]bool, len(stats))
	for _, st := range stats {
		set[st.Path] = true
	}
	return set, nil
}

// toSymbolHit projects a stored symbol into the wire-facing SymbolHit.
func toSymbolHit(sym store.CodeIndexSymbol, path string, score float64) SymbolHit {
	return SymbolHit{
		Name: sym.Name, Kind: sym.Kind, Receiver: sym.Receiver, Path: path,
		Line: sym.StartLine, EndLine: sym.EndLine, Signature: sym.Signature,
		Doc: sym.Doc, Exported: sym.Exported, Score: round3(score),
	}
}

// clampLimit returns def when limit <= 0, max when limit exceeds max, else limit.
func clampLimit(limit, def, max int) int {
	if limit <= 0 {
		return def
	}
	if limit > max {
		return max
	}
	return limit
}

// isNotFound reports whether err is the store's not-found sentinel.
func isNotFound(err error) bool {
	return errors.Is(err, store.ErrNotFound)
}
