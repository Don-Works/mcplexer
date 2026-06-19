// Package harnesscontext discovers small, allowlisted harness context files
// and normalizes them into data-workbench documents.
package harnesscontext

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

type Harness string

const (
	HarnessCodex  Harness = "codex"
	HarnessCursor Harness = "cursor"
	HarnessAll    Harness = "all"
)

const (
	DefaultCollectionName = "harness-context"
	DefaultMaxFiles       = 200
	DefaultMaxFileBytes   = 256 << 10
	DefaultMaxTotalBytes  = 4 << 20
)

type Options struct {
	Harnesses      []Harness
	HomeDir        string
	WorkDir        string
	MaxFiles       int
	MaxFileBytes   int
	MaxTotalBytes  int
	HarvestBatchID string
	CollectionName string
}

type Document struct {
	Harness        string `json:"harness"`
	SourcePath     string `json:"source_path"`
	SourceKind     string `json:"source_kind"`
	Title          string `json:"title"`
	Content        string `json:"content"`
	SourceHash     string `json:"source_hash"`
	SizeBytes      int64  `json:"size_bytes"`
	ModifiedAt     string `json:"modified_at,omitempty"`
	HarvestBatchID string `json:"harvest_batch_id"`
}

type FileManifest struct {
	Harness    string `json:"harness"`
	Path       string `json:"path"`
	SourceKind string `json:"source_kind"`
	Status     string `json:"status"`
	Reason     string `json:"reason,omitempty"`
	SizeBytes  int64  `json:"size_bytes,omitempty"`
	SourceHash string `json:"source_hash,omitempty"`
}

type Result struct {
	Documents []Document     `json:"documents"`
	Files     []FileManifest `json:"files"`
	Found     int            `json:"found"`
	Ingested  int            `json:"ingested"`
	Skipped   int            `json:"skipped"`
	Excluded  int            `json:"excluded"`
	Errors    []string       `json:"errors,omitempty"`
}

type candidate struct {
	path       string
	harness    Harness
	sourceKind string
}

func Harvest(opts Options) (*Result, error) {
	opts = opts.withDefaults()
	if err := opts.Validate(); err != nil {
		return nil, err
	}
	candidates := discoverCandidates(opts)
	res := &Result{Files: make([]FileManifest, 0, len(candidates))}
	seen := map[string]bool{}
	totalBytes := int64(0)
	for _, c := range candidates {
		path := cleanPath(c.path)
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		res.Found++
		if len(res.Documents) >= opts.MaxFiles {
			res.Skipped++
			res.Files = append(res.Files, manifest(c, path, "skipped", "max_files reached", 0, ""))
			continue
		}
		doc, mf, err := readCandidate(c, path, opts.HarvestBatchID, opts.MaxFileBytes)
		if err != nil {
			if mf.Status == "skipped" {
				res.Skipped++
			} else {
				res.Excluded++
			}
			res.Files = append(res.Files, mf)
			continue
		}
		if totalBytes+doc.SizeBytes > int64(opts.MaxTotalBytes) {
			res.Skipped++
			res.Files = append(res.Files, manifest(c, path, "skipped", "max_total_bytes reached", doc.SizeBytes, doc.SourceHash))
			continue
		}
		totalBytes += doc.SizeBytes
		res.Documents = append(res.Documents, doc)
		res.Files = append(res.Files, mf)
	}
	res.Ingested = len(res.Documents)
	return res, nil
}

func BuildDataItems(docs []Document) ([]store.DataItem, error) {
	items := make([]store.DataItem, 0, len(docs))
	for _, doc := range docs {
		raw, err := json.Marshal(doc)
		if err != nil {
			return nil, fmt.Errorf("marshal document %q: %w", doc.SourcePath, err)
		}
		items = append(items, store.DataItem{
			Kind:        store.DataWorkbenchKindDocs,
			PayloadJSON: raw,
			Text:        doc.Content,
		})
	}
	return items, nil
}

func (o Options) Validate() error {
	if o.MaxFiles < 0 || o.MaxFileBytes < 0 || o.MaxTotalBytes < 0 {
		return errors.New("max_files, max_file_bytes, and max_total_bytes must be non-negative")
	}
	for _, h := range o.Harnesses {
		switch h {
		case HarnessCodex, HarnessCursor:
		default:
			return fmt.Errorf("unknown harness %q", h)
		}
	}
	return nil
}

func (o Options) withDefaults() Options {
	if len(o.Harnesses) == 0 {
		o.Harnesses = []Harness{HarnessCodex, HarnessCursor}
	}
	o.Harnesses = expandHarnesses(o.Harnesses)
	if o.MaxFiles == 0 {
		o.MaxFiles = DefaultMaxFiles
	}
	if o.MaxFileBytes == 0 {
		o.MaxFileBytes = DefaultMaxFileBytes
	}
	if o.MaxTotalBytes == 0 {
		o.MaxTotalBytes = DefaultMaxTotalBytes
	}
	if strings.TrimSpace(o.CollectionName) == "" {
		o.CollectionName = DefaultCollectionName
	}
	if strings.TrimSpace(o.HarvestBatchID) == "" {
		o.HarvestBatchID = "harvest-" + time.Now().UTC().Format("20060102T150405Z")
	}
	if strings.TrimSpace(o.HomeDir) == "" {
		if home, err := os.UserHomeDir(); err == nil {
			o.HomeDir = home
		}
	}
	if strings.TrimSpace(o.WorkDir) == "" {
		if wd, err := os.Getwd(); err == nil {
			o.WorkDir = wd
		}
	}
	return o
}

func expandHarnesses(in []Harness) []Harness {
	out := make([]Harness, 0, len(in))
	seen := map[Harness]bool{}
	for _, h := range in {
		if h == HarnessAll {
			for _, expanded := range []Harness{HarnessCodex, HarnessCursor} {
				if !seen[expanded] {
					out = append(out, expanded)
					seen[expanded] = true
				}
			}
			continue
		}
		if !seen[h] {
			out = append(out, h)
			seen[h] = true
		}
	}
	return out
}

func discoverCandidates(opts Options) []candidate {
	var out []candidate
	for _, h := range opts.Harnesses {
		switch h {
		case HarnessCodex:
			out = append(out, codexCandidates(opts.HomeDir, opts.WorkDir)...)
		case HarnessCursor:
			out = append(out, cursorCandidates(opts.HomeDir, opts.WorkDir)...)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].harness != out[j].harness {
			return out[i].harness < out[j].harness
		}
		return out[i].path < out[j].path
	})
	return out
}

func codexCandidates(homeDir, workDir string) []candidate {
	var out []candidate
	add := func(path, kind string) {
		if info, err := os.Lstat(path); err == nil && info.Mode().IsRegular() {
			out = append(out, candidate{path: path, harness: HarnessCodex, sourceKind: kind})
		}
	}
	if homeDir != "" {
		add(filepath.Join(homeDir, ".codex", "AGENTS.md"), "codex_user")
		add(filepath.Join(homeDir, ".codex", "instructions.md"), "codex_user")
	}
	if workDir != "" {
		add(filepath.Join(workDir, "AGENTS.md"), "codex_workspace")
		add(filepath.Join(workDir, ".codex", "AGENTS.md"), "codex_workspace")
		add(filepath.Join(workDir, ".codex", "instructions.md"), "codex_workspace")
	}
	return out
}

func cursorCandidates(homeDir, workDir string) []candidate {
	var out []candidate
	if workDir != "" {
		out = append(out, walkRuleDir(
			filepath.Join(workDir, ".cursor", "rules"),
			HarnessCursor,
			"cursor_workspace_rule",
		)...)
	}
	if homeDir != "" {
		for _, dir := range []string{
			filepath.Join(homeDir, ".cursor", "rules"),
			filepath.Join(homeDir, "Library", "Application Support", "Cursor", "User", "rules"),
		} {
			out = append(out, walkRuleDir(dir, HarnessCursor, "cursor_user_rule")...)
		}
	}
	return out
}

func walkRuleDir(root string, h Harness, sourceKind string) []candidate {
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return nil
	}
	var out []candidate
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if path != root && strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		out = append(out, candidate{path: path, harness: h, sourceKind: sourceKind})
		return nil
	})
	return out
}

func readCandidate(c candidate, path, batchID string, maxFileBytes int) (Document, FileManifest, error) {
	if !allowedExtension(path) {
		return Document{}, manifest(c, path, "excluded", "unsupported extension", 0, ""), errors.New("unsupported extension")
	}
	if secretPath(path) {
		return Document{}, manifest(c, path, "excluded", "secret-like path", 0, ""), errors.New("secret-like path")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return Document{}, manifest(c, path, "skipped", "not found", 0, ""), err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return Document{}, manifest(c, path, "excluded", "symlink", 0, ""), errors.New("symlink")
	}
	if !info.Mode().IsRegular() {
		return Document{}, manifest(c, path, "skipped", "not a regular file", 0, ""), errors.New("not regular")
	}
	if info.Size() > int64(maxFileBytes) {
		return Document{}, manifest(c, path, "excluded", "max_file_bytes exceeded", info.Size(), ""), errors.New("too large")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return Document{}, manifest(c, path, "excluded", "read failed", info.Size(), ""), err
	}
	if secretContent(raw) {
		return Document{}, manifest(c, path, "excluded", "secret-like content", info.Size(), ""), errors.New("secret-like content")
	}
	content := strings.TrimSpace(string(raw))
	if content == "" {
		return Document{}, manifest(c, path, "skipped", "empty file", info.Size(), ""), errors.New("empty")
	}
	hash := sourceHash(raw)
	doc := Document{
		Harness:        string(c.harness),
		SourcePath:     path,
		SourceKind:     c.sourceKind,
		Title:          titleFromContent(path, content),
		Content:        content,
		SourceHash:     hash,
		SizeBytes:      info.Size(),
		ModifiedAt:     info.ModTime().UTC().Format(time.RFC3339),
		HarvestBatchID: batchID,
	}
	return doc, manifest(c, path, "ingested", "", info.Size(), hash), nil
}

func allowedExtension(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".md", ".mdc", ".txt", ".json", ".yaml", ".yml", ".toml":
		return true
	default:
		return false
	}
}

func secretPath(path string) bool {
	lower := strings.ToLower(filepath.Base(path))
	for _, token := range []string{
		".env", "secret", "password", "credential", "api-key", "api_key",
		"id_rsa", "id_ed25519", "private-key", "private_key", ".pem",
	} {
		if strings.Contains(lower, token) {
			return true
		}
	}
	return false
}

func secretContent(raw []byte) bool {
	s := string(raw)
	for _, marker := range []string{
		"-----BEGIN RSA PRIVATE KEY-----",
		"-----BEGIN OPENSSH PRIVATE KEY-----",
		"-----BEGIN EC PRIVATE KEY-----",
		"-----BEGIN PGP PRIVATE KEY BLOCK-----",
	} {
		if strings.Contains(s, marker) {
			return true
		}
	}
	return false
}

func sourceHash(raw []byte) string {
	sum := sha256.Sum256(raw)
	return "hctx-" + hex.EncodeToString(sum[:12])
}

func titleFromContent(path, content string) string {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "# ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "# "))
		}
	}
	base := filepath.Base(path)
	return strings.TrimSuffix(base, filepath.Ext(base))
}

func cleanPath(path string) string {
	if strings.TrimSpace(path) == "" {
		return ""
	}
	if abs, err := filepath.Abs(path); err == nil {
		return filepath.Clean(abs)
	}
	return filepath.Clean(path)
}

func manifest(c candidate, path, status, reason string, size int64, hash string) FileManifest {
	return FileManifest{
		Harness:    string(c.harness),
		Path:       path,
		SourceKind: c.sourceKind,
		Status:     status,
		Reason:     reason,
		SizeBytes:  size,
		SourceHash: hash,
	}
}
