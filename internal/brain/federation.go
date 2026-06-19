package brain

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/don-works/mcplexer/internal/store"
)

// RepoBrainDirName is the per-repo brain directory, discovered by walking a
// session's root_path ancestors exactly the way git discovers `.git`
// (docs/brain.md Appendix C.2). A project repo carrying this directory keeps
// its workspace's brain state in its OWN git history — the state travels
// with the code and no separate central brain commit is needed.
const RepoBrainDirName = ".mcplexer"

// RepoWorkspaceResult describes the workspace/project materialised from a
// repo-local .mcplexer/ marker.
type RepoWorkspaceResult struct {
	WorkspaceID string
	RootPath    string
	ParentID    string
	Created     bool
	Updated     bool
	WroteFile   bool
}

// DiscoverRepoBrain walks rootPath and its ancestors looking for a
// `.mcplexer/` directory (the per-repo brain). It returns the absolute path
// to the FIRST such directory found (nearest to rootPath wins, mirroring
// git's nearest-`.git` rule) and true, or "" and false when none is found.
//
// rootPath need not exist; a blank rootPath yields (no, false). The walk
// stops at the filesystem root. Symlinks are not followed (filepath.Dir
// climbs lexically) — repo discovery is a path-shape decision, not an
// inode one.
//
// excludeDirs are absolute candidate paths that must NEVER be returned even
// when they match by shape. The locked-down gateway data dir (~/.mcplexer,
// holding mcplexer.db, secrets/, p2p/, backups/) is the critical one: a
// session CWD under $HOME with no nearer .mcplexer would otherwise match
// HOME/.mcplexer and the watcher would recursively index the protected tree
// — breaching the data-dir lockdown (SPEC App. C.2 + binding decision 1).
// A matched-but-excluded candidate is skipped and the walk continues
// upward, so a legitimate repo brain higher in the tree is still found.
func DiscoverRepoBrain(rootPath string, excludeDirs ...string) (string, bool) {
	rootPath = strings.TrimSpace(rootPath)
	if rootPath == "" {
		return "", false
	}
	excluded := make(map[string]struct{}, len(excludeDirs))
	for _, e := range excludeDirs {
		if e = strings.TrimSpace(e); e != "" {
			excluded[filepath.Clean(e)] = struct{}{}
		}
	}
	dir := filepath.Clean(rootPath)
	for {
		candidate := filepath.Join(dir, RepoBrainDirName)
		if _, skip := excluded[candidate]; !skip {
			if info, err := os.Stat(candidate); err == nil && info.IsDir() {
				return candidate, true
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			// Reached the filesystem root without a match.
			return "", false
		}
		dir = parent
	}
}

// EnsureRepoWorkspace materialises a task-manager workspace for a repo-local
// .mcplexer/ directory. The folder containing .mcplexer/ is the project root:
// if that root is already the resolved parent workspace root, the parent is
// reused; otherwise a child workspace is created or updated with parent_id set
// to parentWorkspaceID. A workspace.md file, when present, supplies the
// workspace id/name/config; without one, the folder name becomes the project
// name and a collision-safe id.
func EnsureRepoWorkspace(
	ctx context.Context,
	st store.WorkspaceStore,
	repoBrainDir, parentWorkspaceID string,
) (RepoWorkspaceResult, error) {
	if st == nil {
		return RepoWorkspaceResult{}, errors.New("brain: EnsureRepoWorkspace: nil store")
	}
	repoBrainDir = filepath.Clean(strings.TrimSpace(repoBrainDir))
	if repoBrainDir == "." || repoBrainDir == "" {
		return RepoWorkspaceResult{}, errors.New("brain: EnsureRepoWorkspace: empty dir")
	}
	projectRoot := filepath.Clean(filepath.Dir(repoBrainDir))

	workspaces, err := st.ListWorkspaces(ctx)
	if err != nil {
		return RepoWorkspaceResult{}, fmt.Errorf("brain: list workspaces: %w", err)
	}
	parent := workspaceByID(workspaces, parentWorkspaceID)
	if parent != nil && sameCleanPath(parent.RootPath, projectRoot) {
		return RepoWorkspaceResult{
			WorkspaceID: parent.ID,
			RootPath:    projectRoot,
			ParentID:    parent.ParentID,
		}, nil
	}

	workspacePath := filepath.Join(repoBrainDir, workspaceFile)
	if data, err := os.ReadFile(workspacePath); err == nil {
		ws, err := workspaceFromRepoFile(data, workspacePath, projectRoot, parentWorkspaceID)
		if err != nil {
			return RepoWorkspaceResult{}, err
		}
		created, updated, err := upsertRepoWorkspace(ctx, st, workspaces, ws)
		if err != nil {
			return RepoWorkspaceResult{}, err
		}
		return RepoWorkspaceResult{
			WorkspaceID: ws.ID,
			RootPath:    ws.RootPath,
			ParentID:    ws.ParentID,
			Created:     created,
			Updated:     updated,
		}, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return RepoWorkspaceResult{}, fmt.Errorf("brain: read repo workspace.md: %w", err)
	}

	if existing := workspaceByRoot(workspaces, projectRoot); existing != nil {
		ws := *existing
		updated := false
		if ws.ParentID == "" && parentWorkspaceID != "" && ws.ID != parentWorkspaceID {
			ws.ParentID = parentWorkspaceID
			updated = true
		}
		if strings.TrimSpace(ws.Name) == "" {
			ws.Name = filepath.Base(projectRoot)
			updated = true
		}
		if ws.DefaultPolicy == "" && parent != nil && parent.DefaultPolicy != "" {
			ws.DefaultPolicy = parent.DefaultPolicy
			updated = true
		}
		if updated {
			if err := st.UpdateWorkspace(ctx, &ws); err != nil {
				return RepoWorkspaceResult{}, fmt.Errorf("brain: update repo workspace: %w", err)
			}
		}
		wrote, err := writeRepoWorkspaceFileIfMissing(workspacePath, &ws)
		if err != nil {
			return RepoWorkspaceResult{}, err
		}
		return RepoWorkspaceResult{
			WorkspaceID: ws.ID,
			RootPath:    ws.RootPath,
			ParentID:    ws.ParentID,
			Updated:     updated,
			WroteFile:   wrote,
		}, nil
	}

	ws := &store.Workspace{
		ID:            repoWorkspaceID(projectRoot, workspaces),
		Name:          filepath.Base(projectRoot),
		RootPath:      projectRoot,
		ParentID:      parentWorkspaceID,
		Source:        "brain",
		DefaultPolicy: inheritedDefaultPolicy(parent),
	}
	if err := st.CreateWorkspace(ctx, ws); err != nil {
		return RepoWorkspaceResult{}, fmt.Errorf("brain: create repo workspace: %w", err)
	}
	wrote, err := writeRepoWorkspaceFileIfMissing(workspacePath, ws)
	if err != nil {
		return RepoWorkspaceResult{}, err
	}
	return RepoWorkspaceResult{
		WorkspaceID: ws.ID,
		RootPath:    ws.RootPath,
		ParentID:    ws.ParentID,
		Created:     true,
		WroteFile:   wrote,
	}, nil
}

func workspaceFromRepoFile(
	data []byte,
	path, projectRoot, parentWorkspaceID string,
) (*store.Workspace, error) {
	fm, _, err := ParseWorkspace(data)
	if err != nil {
		return nil, fmt.Errorf("brain: parse repo workspace %s: %w", path, err)
	}
	if err := ValidateWorkspace(fm); err != nil {
		return nil, fmt.Errorf("brain: validate repo workspace %s: %w", path, err)
	}
	if strings.TrimSpace(fm.RootPath) == "" {
		fm.RootPath = projectRoot
	}
	if !sameCleanPath(fm.RootPath, projectRoot) {
		return nil, fmt.Errorf("brain: workspace.md root_path %q does not match repo root %q",
			fm.RootPath, projectRoot)
	}
	if strings.TrimSpace(fm.Parent) == "" && parentWorkspaceID != "" && fm.ID != parentWorkspaceID {
		fm.Parent = parentWorkspaceID
	}
	if fm.ID == fm.Parent {
		return nil, fmt.Errorf("brain: workspace.md parent %q must not equal id", fm.Parent)
	}
	if strings.TrimSpace(fm.Source) == "" {
		fm.Source = "brain"
	}
	ws, err := fm.ToWorkspace()
	if err != nil {
		return nil, fmt.Errorf("brain: convert repo workspace %s: %w", path, err)
	}
	ws.RootPath = filepath.Clean(ws.RootPath)
	return ws, nil
}

func upsertRepoWorkspace(
	ctx context.Context,
	st store.WorkspaceStore,
	workspaces []store.Workspace,
	ws *store.Workspace,
) (created, updated bool, err error) {
	existing := workspaceByID(workspaces, ws.ID)
	if existing == nil {
		if err := st.CreateWorkspace(ctx, ws); err != nil {
			return false, false, fmt.Errorf("brain: create repo workspace: %w", err)
		}
		return true, false, nil
	}
	if existing.RootPath != "" && !sameCleanPath(existing.RootPath, ws.RootPath) {
		return false, false, fmt.Errorf(
			"brain: workspace %q already has root_path %q, not %q",
			ws.ID, existing.RootPath, ws.RootPath,
		)
	}
	if ws.CreatedAt.IsZero() {
		ws.CreatedAt = existing.CreatedAt
	}
	if sameWorkspaceConfig(existing, ws) {
		*ws = *existing
		return false, false, nil
	}
	if err := st.UpdateWorkspace(ctx, ws); err != nil {
		return false, false, fmt.Errorf("brain: update repo workspace: %w", err)
	}
	return false, true, nil
}

func workspaceByID(workspaces []store.Workspace, id string) *store.Workspace {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil
	}
	for i := range workspaces {
		if workspaces[i].ID == id {
			return &workspaces[i]
		}
	}
	return nil
}

func workspaceByRoot(workspaces []store.Workspace, root string) *store.Workspace {
	root = filepath.Clean(strings.TrimSpace(root))
	if root == "" || root == "." {
		return nil
	}
	for i := range workspaces {
		if sameCleanPath(workspaces[i].RootPath, root) {
			return &workspaces[i]
		}
	}
	return nil
}

func sameCleanPath(a, b string) bool {
	a = strings.TrimSpace(a)
	b = strings.TrimSpace(b)
	if a == "" || b == "" {
		return false
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

func repoWorkspaceID(projectRoot string, workspaces []store.Workspace) string {
	base := slugify(filepath.Base(projectRoot))
	if base == "" {
		base = "project"
	}
	if workspaceByID(workspaces, base) == nil {
		return base
	}
	digest := hashBytes([]byte(filepath.Clean(projectRoot)))
	for _, n := range []int{8, 12, 16} {
		candidate := base + "-" + digest[:n]
		if workspaceByID(workspaces, candidate) == nil {
			return candidate
		}
	}
	for i := 2; ; i++ {
		candidate := fmt.Sprintf("%s-%s-%d", base, digest[:8], i)
		if workspaceByID(workspaces, candidate) == nil {
			return candidate
		}
	}
}

func inheritedDefaultPolicy(parent *store.Workspace) string {
	if parent != nil && parent.DefaultPolicy != "" {
		return parent.DefaultPolicy
	}
	return "allow"
}

func writeRepoWorkspaceFileIfMissing(path string, ws *store.Workspace) (bool, error) {
	if _, err := os.Stat(path); err == nil {
		return false, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return false, fmt.Errorf("brain: stat repo workspace.md: %w", err)
	}
	data, err := SerializeWorkspace(ws)
	if err != nil {
		return false, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, fmt.Errorf("brain: mkdir repo workspace dir: %w", err)
	}
	if err := atomicWrite(path, data); err != nil {
		return false, fmt.Errorf("brain: write repo workspace.md: %w", err)
	}
	return true, nil
}

func sameWorkspaceConfig(a *store.Workspace, b *store.Workspace) bool {
	if a == nil || b == nil {
		return false
	}
	return a.ID == b.ID &&
		a.Name == b.Name &&
		filepath.Clean(a.RootPath) == filepath.Clean(b.RootPath) &&
		a.ParentID == b.ParentID &&
		workspaceTagsEqual(a.Tags, b.Tags) &&
		a.DefaultPolicy == b.DefaultPolicy &&
		a.Source == b.Source
}

func workspaceTagsEqual(a, b []byte) bool {
	return normalWorkspaceTags(a) == normalWorkspaceTags(b)
}

func normalWorkspaceTags(v []byte) string {
	s := strings.TrimSpace(string(v))
	if s == "" || s == "null" {
		return "[]"
	}
	return s
}

// registeredDir records a dynamically-registered brain directory and the
// (workspace, source) it materialises. A repo-local `.mcplexer/` is one
// such dir; the central brain's workspaces/ tree is handled by the
// path-derived fallback and is NOT registered here.
type registeredDir struct {
	root        string // absolute path to the brain dir (e.g. /code/acme-api/.mcplexer)
	workspaceID string // the workspace slug/id this dir is canonical for
	source      string // store.IndexSourceRepo for repo-local dirs
}

// dirRegistry is the indexer's set of dynamically-registered brain dirs. It
// is consulted to resolve a path's owning (workspace, source) before
// falling back to the central path-derived rule. Safe for concurrent use:
// session resolution (registration) races with the watcher (lookup).
type dirRegistry struct {
	mu   sync.RWMutex
	dirs []registeredDir
}

func newDirRegistry() *dirRegistry { return &dirRegistry{} }

// add registers root as the canonical brain dir for workspaceID with the
// given source. Re-registering the same root is idempotent (the latest
// workspace/source wins). Returns true when the dir was newly added (so the
// caller can do one-time work like seeding the watch set + a reindex).
func (r *dirRegistry) add(root, workspaceID, source string) bool {
	root = filepath.Clean(root)
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.dirs {
		if r.dirs[i].root == root {
			r.dirs[i].workspaceID = workspaceID
			r.dirs[i].source = source
			return false
		}
	}
	r.dirs = append(r.dirs, registeredDir{root: root, workspaceID: workspaceID, source: source})
	return true
}

// resolve returns the registered dir owning path (the longest matching
// root prefix wins, so nested repos resolve to the nearest), or false when
// path lies under no registered dir (a central-brain path).
func (r *dirRegistry) resolve(path string) (registeredDir, bool) {
	path = filepath.Clean(path)
	r.mu.RLock()
	defer r.mu.RUnlock()
	var best registeredDir
	found := false
	for _, d := range r.dirs {
		if d.root == path || strings.HasPrefix(path, d.root+string(filepath.Separator)) {
			if !found || len(d.root) > len(best.root) {
				best = d
				found = true
			}
		}
	}
	return best, found
}

// canonicalWorkspaceDir resolves the on-disk root for a workspace's brain
// entities (the outbound Serializer's path resolver). When a repo-local
// .mcplexer/ dir is registered as canonical for the workspace (M6 —
// federation, SPEC App. C.2: "repo-local is canonical when present"), the
// repo dir IS the workspace root (its internal layout mirrors
// workspaces/<slug>/: workspace.md, tasks/, memory/). Otherwise the central
// brain's workspaces/<slug>/ folder is used. This is what makes a brand-new
// task__create/memory__save in a repo-backed workspace land in the repo, not
// the central brain — the divergence the parity findings flagged.
func (s *Serializer) canonicalWorkspaceDir(workspaceID string) (string, error) {
	if s.registry != nil {
		if d, ok := s.registry.resolveWorkspace(workspaceID); ok && d.root != "" {
			return d.root, nil
		}
	}
	return s.cfg.WorkspaceDir(workspaceID)
}

// resolveWorkspace returns the registered repo dir that is canonical for
// workspaceID, or false when no repo-local dir is registered for it (the
// workspace's canonical brain is the central tree). When multiple dirs map
// to the same workspace (should not happen), the last-registered wins.
func (r *dirRegistry) resolveWorkspace(workspaceID string) (registeredDir, bool) {
	if workspaceID == "" {
		return registeredDir{}, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for i := len(r.dirs) - 1; i >= 0; i-- {
		if r.dirs[i].workspaceID == workspaceID {
			return r.dirs[i], true
		}
	}
	return registeredDir{}, false
}

// snapshot returns a copy of the registered dirs (for the watcher's add
// sweep and tests).
//
//nolint:unused // test/watch helper kept for the next watcher wiring pass.
func (r *dirRegistry) snapshot() []registeredDir {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]registeredDir, len(r.dirs))
	copy(out, r.dirs)
	return out
}

// resolveSourceAndWorkspace classifies a path: registered repo dirs win
// (their recorded workspace + source), else it is a central path whose
// workspace is derived from the layout and source is "central".
func (ix *Indexer) resolveSourceAndWorkspace(path string) (workspaceID, source string) {
	if ix.registry != nil {
		if d, ok := ix.registry.resolve(path); ok {
			return d.workspaceID, d.source
		}
	}
	return workspaceFromPath(path), store.IndexSourceCentral
}

// resolveSource classifies the source label for an outbound Serializer write
// so the index_files row agrees with what the inbound Indexer would record
// (resolveSourceAndWorkspace, above). Without this an outbound write leaves
// Source="", which UpsertIndexFile coerces to IndexSourceCentral and — via
// ON CONFLICT DO UPDATE — clobbers any prior repo label, mislabelling a
// repo-backed record as central until the next inbound reindex reconciles it.
//
// Resolution is registry-first (a path under a registered repo dir, or a
// workspace with a registered repo dir, carries that dir's source), matching
// the indexer; otherwise the record is central.
func (s *Serializer) resolveSource(path, workspaceID string) string {
	if s.registry != nil {
		if d, ok := s.registry.resolve(path); ok {
			return d.source
		}
		if d, ok := s.registry.resolveWorkspace(workspaceID); ok && d.source != "" {
			return d.source
		}
	}
	return store.IndexSourceCentral
}

// RegisterDir dynamically registers a repo-local brain directory with the
// indexer, indexing its contents immediately so the workspace's repo-sourced
// state is live without waiting for the next full reindex (docs/brain.md
// Appendix C.2 — "register found dirs with the indexer/watcher dynamically").
//
// workspaceID is the workspace this dir is canonical for. source is normally
// store.IndexSourceRepo. Re-registering an already-known dir refreshes the
// mapping and re-indexes (cheap; the sha fast-path skips unchanged files).
// The optional onRegister callback (wired by the daemon to the Watcher's
// AddDir) is invoked once per newly-registered dir so the watcher starts
// observing it.
func (ix *Indexer) RegisterDir(ctx context.Context, dir, workspaceID, source string) error {
	dir = filepath.Clean(strings.TrimSpace(dir))
	if dir == "" {
		return errors.New("brain: RegisterDir: empty dir")
	}
	if ix.registry == nil {
		// Defensive: an Indexer built via NewIndexer always has a registry;
		// this guards a zero-value Indexer constructed in a test.
		ix.registry = newDirRegistry()
	}
	if source == "" {
		source = store.IndexSourceRepo
	}
	added := ix.registry.add(dir, workspaceID, source)
	if added && ix.onRegisterDir != nil {
		ix.onRegisterDir(dir)
	}
	// Index the dir's contents now so the repo-sourced rows are immediately
	// queryable. Errors per-file are logged inside reindexRepoDir.
	if err := ix.reindexRepoDir(ctx, dir); err != nil {
		return err
	}
	return nil
}

// SetRegisterDirNotify wires a callback fired once per newly-registered repo
// dir so the daemon can extend the watch set. Nil-safe.
func (ix *Indexer) SetRegisterDirNotify(fn func(dir string)) { ix.onRegisterDir = fn }

// reindexRepoDir indexes a repo-local brain dir whose internal layout
// mirrors a central workspaces/<slug>/ folder: workspace.md, tasks/,
// memory/, memory/facts/. A missing subdir is not an error.
func (ix *Indexer) reindexRepoDir(ctx context.Context, dir string) error {
	wsPath := filepath.Join(dir, workspaceFile)
	if _, err := os.Stat(wsPath); err == nil {
		if err := ix.IndexFile(ctx, wsPath); err != nil {
			ix.log.Warn("brain: index repo workspace.md", "path", wsPath, "error", err)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := ix.reindexDir(ctx, filepath.Join(dir, taskSubdir)); err != nil {
		ix.log.Warn("brain: reindex repo tasks", "dir", dir, "error", err)
	}
	memDir := filepath.Join(dir, memorySubdir)
	if err := ix.reindexDir(ctx, memDir, memoryFactsSubdir); err != nil {
		ix.log.Warn("brain: reindex repo memory", "dir", dir, "error", err)
	}
	if err := ix.reindexDir(ctx, filepath.Join(memDir, memoryFactsSubdir)); err != nil {
		ix.log.Warn("brain: reindex repo facts", "dir", dir, "error", err)
	}
	return nil
}
