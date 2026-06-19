package skillregistry

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"

	"github.com/don-works/mcplexer/internal/store"
)

const (
	maxFileContentBytes = 512 * 1024
	binarySampleBytes   = 512
)

type BundleFileRole string

const (
	RoleManifest BundleFileRole = "manifest"
	RoleScript   BundleFileRole = "script"
	RoleRef      BundleFileRole = "reference"
	RoleAsset    BundleFileRole = "asset"
	RoleOther    BundleFileRole = "other"
)

type BundleFileEntry struct {
	Path       string         `json:"path"`
	Normalized string         `json:"normalized"`
	Kind       string         `json:"kind"`
	Size       int64          `json:"size"`
	SHA256     string         `json:"sha256"`
	IsText     bool           `json:"is_text"`
	Role       BundleFileRole `json:"role"`
}

type BundleFileContent struct {
	Path       string `json:"path"`
	IsText     bool   `json:"is_text"`
	Content    string `json:"content,omitempty"`
	ContentB64 string `json:"content_b64,omitempty"`
	Size       int64  `json:"size"`
	Truncated  bool   `json:"truncated,omitempty"`
}

type FileDiffEntry struct {
	Path   string `json:"path"`
	OldSHA string `json:"old_sha,omitempty"`
	NewSHA string `json:"new_sha,omitempty"`
	Status string `json:"status"`
}

type VersionDiff struct {
	Name         string          `json:"name"`
	OldVersion   int             `json:"old_version"`
	NewVersion   int             `json:"new_version"`
	BodyDiff     string          `json:"body_diff,omitempty"`
	FrontDiff    string          `json:"frontmatter_diff,omitempty"`
	Tree         []FileDiffEntry `json:"tree,omitempty"`
	OldHasBundle bool            `json:"old_has_bundle"`
	NewHasBundle bool            `json:"new_has_bundle"`
}

func classifyRole(norm string) BundleFileRole {
	base := path.Base(strings.ToLower(norm))
	switch {
	case base == "skill.md":
		return RoleManifest
	case strings.HasSuffix(base, ".sh") || strings.HasSuffix(base, ".bash"),
		strings.HasSuffix(base, ".mjs") || strings.HasSuffix(base, ".js"),
		strings.HasSuffix(base, ".ts") || strings.HasSuffix(base, ".py"),
		strings.HasSuffix(base, ".go") || strings.HasSuffix(base, ".rb"),
		strings.HasSuffix(base, ".lua") || strings.HasSuffix(base, ".ps1"),
		strings.HasPrefix(base, "makefile"):
		return RoleScript
	case strings.HasPrefix(norm, "reference/") || strings.HasPrefix(norm, "docs/"),
		strings.HasSuffix(base, ".md") && base != "skill.md",
		strings.HasSuffix(base, ".txt") || strings.HasSuffix(base, ".rst"),
		strings.HasSuffix(base, ".adoc"):
		return RoleRef
	case strings.HasSuffix(base, ".png") || strings.HasSuffix(base, ".jpg"),
		strings.HasSuffix(base, ".jpeg") || strings.HasSuffix(base, ".gif"),
		strings.HasSuffix(base, ".svg") || strings.HasSuffix(base, ".ico"),
		strings.HasSuffix(base, ".woff") || strings.HasSuffix(base, ".woff2"),
		strings.HasSuffix(base, ".ttf") || strings.HasSuffix(base, ".eot"),
		strings.HasSuffix(base, ".zip") || strings.HasSuffix(base, ".tar"),
		strings.HasSuffix(base, ".gz") || strings.HasSuffix(base, ".pdf"):
		return RoleAsset
	default:
		return RoleOther
	}
}

func isText(data []byte) bool {
	sample := data
	if len(sample) > binarySampleBytes {
		sample = sample[:binarySampleBytes]
	}
	for _, b := range sample {
		if b < 0x20 && b != '\n' && b != '\r' && b != '\t' {
			return false
		}
	}
	return true
}

func fileSha256(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

type bundleWalker struct {
	topDir string
}

func (w *bundleWalker) normalize(name string) string {
	cleaned := path.Clean(name)
	if strings.HasPrefix(cleaned, "/") || strings.HasPrefix(cleaned, "..") {
		return ""
	}
	if w.topDir == "" {
		if idx := strings.Index(cleaned, "/"); idx > 0 {
			w.topDir = cleaned[:idx]
		}
	}
	if w.topDir != "" {
		cleaned = strings.TrimPrefix(cleaned, w.topDir+"/")
	}
	if cleaned == "" || cleaned == "." {
		return ""
	}
	return cleaned
}

func walkBundle(raw []byte, fn func(hdr *tar.Header, norm string, content []byte) error) error {
	gz, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("gzip: %w", err)
	}
	defer func() { _ = gz.Close() }()

	tr := tar.NewReader(gz)
	w := &bundleWalker{}
	entries := 0
	for {
		entries++
		if entries > 4096 {
			return fmt.Errorf("bundle has more than 4096 entries")
		}
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("tar next: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg && hdr.Typeflag != 0 {
			continue
		}
		norm := w.normalize(hdr.Name)
		if norm == "" {
			continue
		}
		if strings.HasPrefix(norm, "..") || strings.HasPrefix(norm, "/") {
			return fmt.Errorf("unsafe path %q after normalization", hdr.Name)
		}
		content, err := io.ReadAll(io.LimitReader(tr, MaxBundleBytes+1))
		if err != nil {
			return fmt.Errorf("read %s: %w", norm, err)
		}
		if err := fn(hdr, norm, content); err != nil {
			return err
		}
	}
	return nil
}

func (r *Registry) BundleFileIndex(ctx context.Context, scope store.SkillScope, name string, ref VersionRef) ([]BundleFileEntry, error) {
	if r == nil || r.store == nil {
		return nil, errors.New("skillregistry: not initialised")
	}
	raw, _, err := r.FetchBundle(ctx, scope, name, ref)
	if err != nil {
		return nil, err
	}
	var entries []BundleFileEntry
	err = walkBundle(raw, func(hdr *tar.Header, norm string, content []byte) error {
		text := isText(content)
		entries = append(entries, BundleFileEntry{
			Path:       norm,
			Normalized: path.Clean(norm),
			Kind:       "file",
			Size:       hdr.Size,
			SHA256:     fileSha256(content),
			IsText:     text,
			Role:       classifyRole(norm),
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return entries, nil
}

func (r *Registry) BundleFileContent(ctx context.Context, scope store.SkillScope, name string, ref VersionRef, filePath string) (*BundleFileContent, error) {
	if r == nil || r.store == nil {
		return nil, errors.New("skillregistry: not initialised")
	}
	cleaned := path.Clean(filePath)
	if strings.HasPrefix(cleaned, "..") || strings.HasPrefix(cleaned, "/") {
		return nil, fmt.Errorf("unsafe path %q", filePath)
	}
	raw, _, err := r.FetchBundle(ctx, scope, name, ref)
	if err != nil {
		return nil, err
	}
	var result *BundleFileContent
	err = walkBundle(raw, func(hdr *tar.Header, norm string, content []byte) error {
		if norm != cleaned {
			return nil
		}
		truncated := int64(len(content)) > maxFileContentBytes
		if truncated {
			content = content[:maxFileContentBytes]
		}
		text := isText(content)
		fc := &BundleFileContent{
			Path:      norm,
			IsText:    text,
			Size:      hdr.Size,
			Truncated: truncated,
		}
		if text {
			fc.Content = string(content)
		} else {
			fc.ContentB64 = base64.StdEncoding.EncodeToString(content)
		}
		result = fc
		return nil
	})
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, store.ErrNotFound
	}
	return result, nil
}

func (r *Registry) DiffVersions(ctx context.Context, scope store.SkillScope, name string, oldRef, newRef VersionRef) (*VersionDiff, error) {
	if r == nil || r.store == nil {
		return nil, errors.New("skillregistry: not initialised")
	}
	oldEntry, err := r.Get(ctx, scope, name, oldRef)
	if err != nil {
		return nil, fmt.Errorf("old version: %w", err)
	}
	newEntry, err := r.Get(ctx, scope, name, newRef)
	if err != nil {
		return nil, fmt.Errorf("new version: %w", err)
	}

	diff := &VersionDiff{
		Name:         name,
		OldVersion:   oldEntry.Version,
		NewVersion:   newEntry.Version,
		OldHasBundle: oldEntry.BundleSHA256 != "",
		NewHasBundle: newEntry.BundleSHA256 != "",
	}

	if oldEntry.Body != newEntry.Body {
		diff.BodyDiff = unifiedDiff(oldEntry.Body, newEntry.Body, "body")
	}

	oldFront := extractFrontmatter(oldEntry.Body)
	newFront := extractFrontmatter(newEntry.Body)
	if oldFront != newFront {
		diff.FrontDiff = unifiedDiff(oldFront, newFront, "frontmatter")
	}

	if diff.OldHasBundle && diff.NewHasBundle {
		tree, err := r.treeDiff(ctx, scope, name, oldEntry, newEntry)
		if err == nil {
			diff.Tree = tree
		}
	} else if diff.OldHasBundle && !diff.NewHasBundle {
		diff.Tree = []FileDiffEntry{{Status: "bundle_removed"}}
	} else if !diff.OldHasBundle && diff.NewHasBundle {
		diff.Tree = []FileDiffEntry{{Status: "bundle_added"}}
	}

	return diff, nil
}

func (r *Registry) treeDiff(ctx context.Context, scope store.SkillScope, name string, oldEntry, newEntry *store.SkillRegistryEntry) ([]FileDiffEntry, error) {
	oldRaw, _, err := r.FetchBundle(ctx, scope, name, VersionRef{Version: oldEntry.Version})
	if err != nil {
		return nil, err
	}
	newRaw, _, err := r.FetchBundle(ctx, scope, name, VersionRef{Version: newEntry.Version})
	if err != nil {
		return nil, err
	}

	oldFiles := map[string]string{}
	if err := walkBundle(oldRaw, func(_ *tar.Header, norm string, content []byte) error {
		oldFiles[norm] = fileSha256(content)
		return nil
	}); err != nil {
		return nil, err
	}

	newFiles := map[string]string{}
	if err := walkBundle(newRaw, func(_ *tar.Header, norm string, content []byte) error {
		newFiles[norm] = fileSha256(content)
		return nil
	}); err != nil {
		return nil, err
	}

	var tree []FileDiffEntry
	for p, sha := range oldFiles {
		if newSha, ok := newFiles[p]; ok {
			if sha != newSha {
				tree = append(tree, FileDiffEntry{Path: p, OldSHA: sha, NewSHA: newSha, Status: "modified"})
			}
		} else {
			tree = append(tree, FileDiffEntry{Path: p, OldSHA: sha, Status: "removed"})
		}
	}
	for p, sha := range newFiles {
		if _, ok := oldFiles[p]; !ok {
			tree = append(tree, FileDiffEntry{Path: p, NewSHA: sha, Status: "added"})
		}
	}
	return tree, nil
}

func extractFrontmatter(body string) string {
	if !strings.HasPrefix(body, "---") {
		return ""
	}
	end := strings.Index(body[3:], "---")
	if end < 0 {
		return ""
	}
	return strings.TrimSpace(body[3 : end+3])
}

func unifiedDiff(old, new, label string) string {
	oldLines := strings.Split(old, "\n")
	newLines := strings.Split(new, "\n")

	var buf strings.Builder
	buf.WriteString("--- " + label + " (old)\n")
	buf.WriteString("+++ " + label + " (new)\n")

	cs := longestCommonSubsequence(oldLines, newLines)
	oi, ni := 0, 0
	for _, c := range cs {
		for oi < len(oldLines) && oldLines[oi] != c {
			buf.WriteString("- " + oldLines[oi] + "\n")
			oi++
		}
		for ni < len(newLines) && newLines[ni] != c {
			buf.WriteString("+ " + newLines[ni] + "\n")
			ni++
		}
		buf.WriteString("  " + c + "\n")
		oi++
		ni++
	}
	for ; oi < len(oldLines); oi++ {
		buf.WriteString("- " + oldLines[oi] + "\n")
	}
	for ; ni < len(newLines); ni++ {
		buf.WriteString("+ " + newLines[ni] + "\n")
	}

	result := buf.String()
	if len(result) > 32768 {
		return result[:32768] + "\n... (truncated)\n"
	}
	return result
}

func longestCommonSubsequence(a, b []string) []string {
	m, n := len(a), len(b)
	if m > 5000 {
		m = 5000
	}
	if n > 5000 {
		n = 5000
	}
	dp := make([][]int, m+1)
	for i := range dp {
		dp[i] = make([]int, n+1)
	}
	for i := 1; i <= m; i++ {
		for j := 1; j <= n; j++ {
			if a[i-1] == b[j-1] {
				dp[i][j] = dp[i-1][j-1] + 1
			} else if dp[i-1][j] >= dp[i][j-1] {
				dp[i][j] = dp[i-1][j]
			} else {
				dp[i][j] = dp[i][j-1]
			}
		}
	}
	lcs := make([]string, 0, dp[m][n])
	i, j := m, n
	for i > 0 && j > 0 {
		if a[i-1] == b[j-1] {
			lcs = append(lcs, a[i-1])
			i--
			j--
		} else if dp[i-1][j] >= dp[i][j-1] {
			i--
		} else {
			j--
		}
	}
	for k := 0; k < len(lcs)/2; k++ {
		lcs[k], lcs[len(lcs)-1-k] = lcs[len(lcs)-1-k], lcs[k]
	}
	return lcs
}
