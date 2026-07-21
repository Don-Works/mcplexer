package skillregistry

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/don-works/mcplexer/internal/store"
)

// MigrationAction records the outcome of importing one local skill.
type MigrationAction string

const (
	// ActionImported — bundle was published as a brand new (or new
	// version of an) entry in the registry.
	ActionImported MigrationAction = "imported"
	// ActionSkipped — already present with the same content_hash.
	ActionSkipped MigrationAction = "skipped"
	// ActionUpdated — overwrote a different-hash entry with a new version.
	ActionUpdated MigrationAction = "updated"
	// ActionFailed — bundle/parse/publish error; ArchivedTo is empty.
	ActionFailed MigrationAction = "failed"
)

// MigrationResult is one row of the per-skill outcome table.
type MigrationResult struct {
	Name         string          `json:"name"`
	DirName      string          `json:"dir"`
	Path         string          `json:"path"`
	Action       MigrationAction `json:"action"`
	Version      int             `json:"version,omitempty"`
	BundleSHA256 string          `json:"bundle_sha256,omitempty"`
	ArchivedTo   string          `json:"archived_to,omitempty"`
	Error        string          `json:"error,omitempty"`
	DryRun       bool            `json:"dry_run,omitempty"`
}

// MigrateOptions controls a single-skill import call.
type MigrateOptions struct {
	// Path is the absolute path of the skill directory on disk.
	Path string
	// ArchiveDir, when non-empty, is the destination root the original
	// directory will be moved to on success. Path's basename becomes a
	// subdir under ArchiveDir. The directory is created with mode 0700.
	ArchiveDir string
	// Overwrite must be true to publish when a different-hash entry
	// already exists for the same name (StatusVersionConflict). When
	// false, ImportLocalSkill returns ActionFailed without touching the
	// registry.
	Overwrite bool
	// DryRun returns the proposed Action without touching the registry
	// or filesystem.
	DryRun bool
	// Author is passed through to PublishOptions; defaults to "migrate"
	// when empty.
	Author string
}

// LocalSkillPayload is the on-disk skill material ready for a normal
// registry Publish call. Unlike ImportLocalSkill, preparing a local
// payload never archives or mutates the source directory.
type LocalSkillPayload struct {
	Name       string
	Body       string
	Bundle     []byte
	SourcePath string
	IsDir      bool
}

// PrepareLocalSkill reads a local SKILL.md file or skill directory and
// returns the body plus an optional bundle of sidecar files. Directory
// inputs are bundled; file inputs are text-only.
func PrepareLocalSkill(path string) (*LocalSkillPayload, error) {
	abs, err := filepath.Abs(ExpandUserHome(path))
	if err != nil {
		return nil, fmt.Errorf("abs %s: %w", path, err)
	}
	info, err := statNonSymlink(abs)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return prepareLocalSkillDir(abs)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file or directory", abs)
	}
	if filepath.Base(abs) != "SKILL.md" {
		return nil, fmt.Errorf("source_path file must be named SKILL.md; got %s", filepath.Base(abs))
	}
	return prepareLocalSkillFile(abs)
}

func statNonSymlink(path string) (os.FileInfo, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("stat %s: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%s is a symlink; refusing local skill source", path)
	}
	return info, nil
}

func prepareLocalSkillDir(dir string) (*LocalSkillPayload, error) {
	body, parsed, err := readAndParse(dir)
	if err != nil {
		return nil, err
	}
	bundle, err := packSkillDir(dir, body, parsed.Name)
	if err != nil {
		return nil, fmt.Errorf("bundle: %w", err)
	}
	return &LocalSkillPayload{
		Name: parsed.Name, Body: body, Bundle: bundle, SourcePath: dir, IsDir: true,
	}, nil
}

func prepareLocalSkillFile(path string) (*LocalSkillPayload, error) {
	raw, err := os.ReadFile(path) //nolint:gosec
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	body := string(raw)
	parsed, err := Parse(body, "")
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &LocalSkillPayload{Name: parsed.Name, Body: body, SourcePath: path}, nil
}

// ImportLocalSkill reads <opts.Path>/SKILL.md, bundles the directory,
// publishes to the registry, then moves the source dir to ArchiveDir.
// Returns one MigrationResult — never an error — so callers can loop
// over a batch and present a structured summary.
func (r *Registry) ImportLocalSkill(ctx context.Context, opts MigrateOptions) MigrationResult {
	res := MigrationResult{
		Path:    opts.Path,
		DirName: filepath.Base(opts.Path),
		DryRun:  opts.DryRun,
	}
	if r == nil || r.store == nil {
		res.Action = ActionFailed
		res.Error = "skillregistry: not initialised"
		return res
	}
	body, parsed, err := readAndParse(opts.Path)
	if err != nil {
		res.Action = ActionFailed
		res.Error = err.Error()
		return res
	}
	res.Name = parsed.Name
	if harnessOwnedSkills[parsed.Name] {
		res.Action = ActionFailed
		res.Error = fmt.Sprintf("%s is owned by harness sync; refusing to archive or republish it", parsed.Name)
		return res
	}

	head, headErr := r.store.GetSkillRegistryHead(ctx, AdminScope(), parsed.Name)
	if shortCircuit, done := handleHeadLookup(&res, head, headErr, parsed.ContentHash, opts); done {
		return shortCircuit
	}

	bundle, bundleErr := packSkillDir(opts.Path, body, parsed.Name)
	if bundleErr != nil {
		res.Action = ActionFailed
		res.Error = fmt.Sprintf("bundle: %v", bundleErr)
		return res
	}

	if opts.DryRun {
		if errors.Is(headErr, store.ErrNotFound) {
			res.Action = ActionImported
		} else {
			res.Action = ActionUpdated
			res.Version = head.Version
		}
		return res
	}

	return finalizePublish(ctx, r, &res, opts, body, parsed.Name, bundle, headErr)
}

// handleHeadLookup interprets the registry-head fetch and returns either
// a fully-formed short-circuit result (skipped / failed / lookup-error)
// or the zero value when the caller should proceed with packing + publish.
// The second return value is true when the result should be returned
// without further work.
func handleHeadLookup(
	res *MigrationResult,
	head *store.SkillRegistryEntry,
	headErr error,
	contentHash string,
	opts MigrateOptions,
) (MigrationResult, bool) {
	switch {
	case errors.Is(headErr, store.ErrNotFound):
		return MigrationResult{}, false
	case headErr != nil:
		res.Action = ActionFailed
		res.Error = fmt.Sprintf("registry lookup: %v", headErr)
		return *res, true
	case head.ContentHash == contentHash:
		res.Action = ActionSkipped
		res.Version = head.Version
		if !opts.DryRun {
			if dest, archErr := archiveDir(opts.Path, opts.ArchiveDir); archErr != nil {
				res.Error = fmt.Sprintf("archive (skipped): %v", archErr)
			} else {
				res.ArchivedTo = dest
			}
		}
		return *res, true
	default:
		if !opts.Overwrite {
			res.Action = ActionFailed
			res.Version = head.Version
			res.Error = fmt.Sprintf(
				"version-conflict: registry v%d has different content_hash; pass overwrite to publish a new version",
				head.Version,
			)
			return *res, true
		}
	}
	return MigrationResult{}, false
}

// finalizePublish runs the actual Publish call and archives the source
// dir afterward. Split out of ImportLocalSkill to keep that function
// under the 50-line guideline.
func finalizePublish(
	ctx context.Context,
	r *Registry,
	res *MigrationResult,
	opts MigrateOptions,
	body, name string,
	bundle []byte,
	headErr error,
) MigrationResult {
	author := opts.Author
	if author == "" {
		author = "migrate"
	}
	pub, pubErr := r.Publish(ctx, PublishOptions{
		Name:   name,
		Body:   body,
		Author: author,
		Bundle: bundle,
	})
	if pubErr != nil {
		res.Action = ActionFailed
		res.Error = fmt.Sprintf("publish: %v", pubErr)
		return *res
	}
	res.Version = pub.Version
	res.BundleSHA256 = pub.BundleSHA256
	if errors.Is(headErr, store.ErrNotFound) {
		res.Action = ActionImported
	} else {
		res.Action = ActionUpdated
	}
	if dest, archErr := archiveDir(opts.Path, opts.ArchiveDir); archErr != nil {
		res.Error = fmt.Sprintf("archive (published): %v", archErr)
	} else {
		res.ArchivedTo = dest
	}
	return *res
}

// readAndParse reads the SKILL.md from dir and parses it.
func readAndParse(dir string) (string, *Parsed, error) {
	mdPath := filepath.Join(dir, "SKILL.md")
	raw, err := os.ReadFile(mdPath) //nolint:gosec
	if err != nil {
		return "", nil, fmt.Errorf("read %s: %w", mdPath, err)
	}
	body := string(raw)
	parsed, err := Parse(body, "")
	if err != nil {
		return "", nil, fmt.Errorf("parse %s: %w", mdPath, err)
	}
	return body, parsed, nil
}
