package skillregistry

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/don-works/mcplexer/internal/store"
)

const managedManifestName = ".mcplexer-managed.json"

type ManagedManifest struct {
	Entries map[string]ManagedEntry `json:"entries"`
}

type ManagedEntry struct {
	Version         int    `json:"version"`
	ContentHash     string `json:"content_hash"`
	RenderedHash    string `json:"rendered_hash,omitempty"`
	RendererVersion int    `json:"renderer_version,omitempty"`
}

type Materializer struct {
	reg *Registry

	mu sync.Mutex
}

func NewMaterializer(reg *Registry) *Materializer {
	return &Materializer{reg: reg}
}

type MaterializeScope struct {
	TargetRoot string
	Scope      store.SkillScope
}

type MaterializeResult struct {
	Created []string `json:"created,omitempty"`
	Updated []string `json:"updated,omitempty"`
	Pruned  []string `json:"pruned,omitempty"`
	Adopted []string `json:"adopted,omitempty"`
	Skipped []string `json:"skipped,omitempty"`
}

func (m *Materializer) Materialize(ctx context.Context, ms MaterializeScope) (*MaterializeResult, error) {
	if m == nil || m.reg == nil {
		return nil, errors.New("materializer: not initialised")
	}
	if ms.TargetRoot == "" {
		return nil, errors.New("materializer: target root is required")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if err := os.MkdirAll(ms.TargetRoot, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir target root: %w", err)
	}

	manifest, err := m.readManifest(ms.TargetRoot)
	if err != nil {
		return nil, fmt.Errorf("read manifest: %w", err)
	}

	heads, err := m.reg.ListHeads(ctx, ms.Scope, 0)
	if err != nil {
		return nil, fmt.Errorf("list heads: %w", err)
	}

	result := &MaterializeResult{}

	wantSet := make(map[string]store.SkillRegistryEntry, len(heads))
	for _, h := range heads {
		wantSet[h.Name] = h
	}

	for name, entry := range wantSet {
		managed, wasManaged := manifest.Entries[name]
		dir, err := managedSkillDir(ms.TargetRoot, name)
		if err != nil {
			return nil, err
		}
		rendered, err := m.reg.RenderEntry(ctx, &entry)
		if err != nil {
			return nil, fmt.Errorf("render %s: %w", name, err)
		}
		composed := len(rendered.Provenance) > 0
		desiredManaged := managedEntryForRender(entry, rendered, composed)
		managedRenderedHash := managed.RenderedHash
		if managedRenderedHash == "" {
			managedRenderedHash = managed.ContentHash
		}
		managedCurrent := managed.ContentHash == entry.ContentHash &&
			managedRenderedHash == rendered.SHA256 &&
			(!composed || managed.RendererVersion == CompositionRendererVersion)

		switch {
		case !wasManaged && dirExists(dir):
			onDiskHash, hasMD := contentHashOfFile(filepath.Join(dir, "SKILL.md"))
			if hasMD && onDiskHash == rendered.SHA256 {
				manifest.Entries[name] = desiredManaged
				result.Adopted = append(result.Adopted, name)
				continue
			}
			result.Skipped = append(result.Skipped, name)
			continue

		case wasManaged && managedCurrent:
			result.Skipped = append(result.Skipped, name)
			continue

		case wasManaged:
			if err := m.writeSkill(ctx, dir, &entry, rendered.Body); err != nil {
				return nil, fmt.Errorf("update %s: %w", name, err)
			}
			manifest.Entries[name] = desiredManaged
			result.Updated = append(result.Updated, name)

		default:
			if err := m.writeSkill(ctx, dir, &entry, rendered.Body); err != nil {
				return nil, fmt.Errorf("create %s: %w", name, err)
			}
			manifest.Entries[name] = desiredManaged
			result.Created = append(result.Created, name)
		}
	}

	for name := range manifest.Entries {
		if _, want := wantSet[name]; !want {
			dir, err := managedSkillDir(ms.TargetRoot, name)
			if err != nil {
				delete(manifest.Entries, name)
				result.Pruned = append(result.Pruned, name)
				continue
			}
			if err := os.RemoveAll(dir); err != nil {
				return nil, fmt.Errorf("prune %s: %w", name, err)
			}
			delete(manifest.Entries, name)
			result.Pruned = append(result.Pruned, name)
		}
	}

	if err := m.writeManifest(ms.TargetRoot, manifest); err != nil {
		return nil, fmt.Errorf("write manifest: %w", err)
	}

	return result, nil
}

func managedSkillDir(root, name string) (string, error) {
	name = strings.TrimSpace(name)
	if !nameRE.MatchString(name) {
		return "", fmt.Errorf("materializer: unsafe skill name %q", name)
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve target root: %w", err)
	}
	dirAbs := filepath.Join(rootAbs, name)
	rel, err := filepath.Rel(rootAbs, dirAbs)
	if err != nil {
		return "", fmt.Errorf("resolve target dir: %w", err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("materializer: target dir escapes root for %q", name)
	}
	return dirAbs, nil
}

func managedEntryForRender(entry store.SkillRegistryEntry, rendered *RenderedSkill, composed bool) ManagedEntry {
	managed := ManagedEntry{Version: entry.Version, ContentHash: entry.ContentHash}
	if composed {
		managed.RenderedHash = rendered.SHA256
		managed.RendererVersion = CompositionRendererVersion
	}
	return managed
}

func (m *Materializer) writeSkill(
	ctx context.Context,
	dir string,
	entry *store.SkillRegistryEntry,
	renderedBody string,
) error {
	staged, err := os.MkdirTemp(filepath.Dir(dir), ".staging-"+entry.Name+"-*")
	if err != nil {
		return fmt.Errorf("mkdir staging: %w", err)
	}

	if entry.BundleSHA256 != "" {
		bundle, _, fetchErr := m.reg.FetchBundleForEntry(ctx, entry)
		if fetchErr != nil && !errors.Is(fetchErr, ErrBundleNotPresent) {
			_ = os.RemoveAll(staged)
			return fmt.Errorf("fetch bundle for %s: %w", entry.Name, fetchErr)
		}
		if fetchErr == nil && len(bundle) > 0 {
			if err := extractBundleToDir(bundle, staged); err != nil {
				_ = os.RemoveAll(staged)
				return fmt.Errorf("extract bundle: %w", err)
			}
		}
	}

	// Bundle validation deliberately compares its SKILL.md to the raw
	// registry body. Runtime materialization overwrites that file only after
	// extraction so an archived raw placeholder can never win.
	if err := os.WriteFile(filepath.Join(staged, "SKILL.md"), []byte(renderedBody), 0o644); err != nil {
		_ = os.RemoveAll(staged)
		return fmt.Errorf("write rendered SKILL.md: %w", err)
	}

	if dirExists(dir) {
		if err := os.RemoveAll(dir); err != nil {
			_ = os.RemoveAll(staged)
			return fmt.Errorf("remove old dir: %w", err)
		}
	}

	if err := os.Rename(staged, dir); err != nil {
		if renameErr := fallbackCopyDir(staged, dir); renameErr != nil {
			_ = os.RemoveAll(staged)
			return fmt.Errorf("rename staging: %w; fallback: %v", err, renameErr)
		}
		_ = os.RemoveAll(staged)
	}

	return nil
}

func (m *Materializer) readManifest(root string) (*ManagedManifest, error) {
	path := filepath.Join(root, managedManifestName)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, fs.ErrNotExist) {
			return &ManagedManifest{Entries: map[string]ManagedEntry{}}, nil
		}
		return nil, err
	}
	var manifest ManagedManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}
	if manifest.Entries == nil {
		manifest.Entries = map[string]ManagedEntry{}
	}
	return &manifest, nil
}

func (m *Materializer) writeManifest(root string, manifest *ManagedManifest) error {
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal manifest: %w", err)
	}
	return os.WriteFile(filepath.Join(root, managedManifestName), data, 0o644)
}

func extractBundleToDir(bundle []byte, destDir string) error {
	if len(bundle) == 0 {
		return nil
	}
	gz, err := gzip.NewReader(bytes.NewReader(bundle))
	if err != nil {
		return fmt.Errorf("gzip: %w", err)
	}
	defer func() { _ = gz.Close() }()

	tr := tar.NewReader(gz)
	var entriesRead int
	for {
		if entriesRead++; entriesRead > 4096 {
			return fmt.Errorf("bundle has more than 4096 entries")
		}
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("tar next: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg && hdr.Typeflag != 0 && hdr.Typeflag != tar.TypeDir {
			continue
		}
		name := filepath.Clean(hdr.Name)
		if strings.HasPrefix(name, "../") || strings.Contains(name, "/../") || filepath.IsAbs(name) {
			continue
		}
		name = stripLeadingComponent(name)
		if name == "" || name == "." {
			continue
		}
		target := filepath.Join(destDir, name)
		switch {
		case hdr.Typeflag == tar.TypeDir || strings.HasSuffix(name, "/"):
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		default:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			mode := os.FileMode(hdr.Mode).Perm()
			if mode == 0 {
				mode = 0o644
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, io.LimitReader(tr, MaxBundleBytes)); err != nil {
				_ = f.Close()
				return err
			}
			_ = f.Close()
		}
	}
}

func stripLeadingComponent(p string) string {
	p = filepath.ToSlash(p)
	idx := strings.Index(p, "/")
	if idx < 0 {
		return p
	}
	return p[idx+1:]
}

func contentHashOfFile(path string) (string, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), true
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func fallbackCopyDir(src, dst string) error {
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	return filepath.Walk(src, func(path string, info fs.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer func() { _ = in.Close() }()
		out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode().Perm())
		if err != nil {
			return err
		}
		defer func() { _ = out.Close() }()
		_, err = io.Copy(out, in)
		return err
	})
}
