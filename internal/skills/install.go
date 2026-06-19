// Package skills — installation, listing, and removal of .mcskill bundles.
//
// This file builds on M2.1 (manifest schema) and M2.4 (signing primitives)
// to implement the M2.2 install pipeline:
//
//  1. Read the .mcskill bundle bytes and the sibling .minisig signature.
//  2. Verify the signature against a trusted-signer pubkey.
//  3. Parse + validate manifest.toml from the bundle.
//  4. Check declared MCP-server capabilities are configured locally.
//  5. Atomically commit: extract files to {data_dir}/skills/<name>/ AND
//     record the row in installed_skills. Rollback on any failure.
//
// The capability review screen lives in M2.5; for v1 the CLI prints a text
// summary and asks y/N (cmd/mcplexer/skill_install.go). This package supplies
// the data the prompt needs via InstallReview.
package skills

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// Sentinel errors for skill installation. All errors returned by Install,
// Remove, and List wrap these so callers can match with errors.Is.
var (
	// ErrSkillNotInstalled indicates a skill name was not found in the
	// installed_skills table (returned by Remove and the CLI's `show`).
	ErrSkillNotInstalled = errors.New("skill not installed")

	// ErrSkillAlreadyInstalled indicates a skill with the same name is
	// already present and the caller did not pass Force=true.
	ErrSkillAlreadyInstalled = errors.New("skill already installed")

	// ErrCapabilityNotConfigured indicates the bundle declares an MCP-server
	// capability that is not present in the local downstream_servers table.
	// The user must provision the server first; the install is rejected with
	// no partial state on disk or in the database.
	ErrCapabilityNotConfigured = errors.New("capability not configured")

	// ErrBundleMalformed indicates the .mcskill archive could not be parsed
	// (bad gzip header, malformed tar, missing manifest.toml, path escape).
	ErrBundleMalformed = errors.New("malformed bundle")
)

// MaxBundleSize caps the size of a .mcskill bundle accepted from any
// source (file or URL). 100 MB is plenty for skills with bundled assets
// while still bounding memory + temp-disk usage.
const MaxBundleSize = 100 * 1024 * 1024 // 100 MB

// InstallOptions tunes Install behavior.
type InstallOptions struct {
	// SkillsDir is the parent directory under which a per-skill folder is
	// created (e.g. ~/.mcplexer/skills). Required.
	SkillsDir string

	// Source is recorded in the installed_skills row (e.g. "file:/abs/foo.mcskill"
	// or "https://...").
	Source string

	// Force, when true, replaces an already-installed skill instead of
	// returning ErrSkillAlreadyInstalled.
	Force bool

	// AllowUnsigned permits installing a bundle without a signature. The
	// caller (CLI) gates this behind an explicit flag — never default true.
	AllowUnsigned bool
}

// InstallReview is the data the CLI shows to the operator before they
// confirm an install. It is computed by Verify+Parse and passed into the
// y/N prompt.
type InstallReview struct {
	Manifest      *Manifest
	SignerPubkey  string // 56-char canonical form, "" when unsigned
	SignerName    string // human label from trusted_signers
	SignerKeyID   string // 16-char hex id
	UnknownSigner bool   // true when bundle was signed but signer is not trusted
	MissingMCP    []string
	Source        string
}

// Install verifies, validates, and installs a .mcskill bundle.
//
// Order of operations is critical for the "leave state untouched on failure"
// guarantee:
//  1. Read bundle into memory (capped at MaxBundleSize).
//  2. Verify signature (or accept unsigned when AllowUnsigned).
//  3. Extract manifest.toml from the in-memory tar; parse + validate.
//  4. Check capability requirements against the local store.
//  5. Begin DB tx, write installed_skills row.
//  6. Extract files to a temp dir under SkillsDir, then atomically rename
//     into place (skills/<name>). Roll back on any failure.
//  7. Commit tx.
func Install(
	ctx context.Context, db store.Store, bundlePath string, opts InstallOptions,
) (*store.InstalledSkill, *InstallReview, error) {
	if opts.SkillsDir == "" {
		return nil, nil, errors.New("Install: SkillsDir required")
	}
	bundleBytes, err := readCapped(bundlePath, MaxBundleSize)
	if err != nil {
		return nil, nil, err
	}
	sigBytes, sigErr := readSignatureSibling(bundlePath)
	if sigErr != nil && !opts.AllowUnsigned {
		return nil, nil, sigErr
	}
	return InstallFromBytes(ctx, db, bundleBytes, sigBytes, opts)
}

// InstallFromBytes is the in-memory variant of Install: callers (e.g.
// the M2.7 mesh skill share path) already have the bundle bytes and can
// skip the file-system read. The signature semantics match Install:
// sigBytes==nil means "no signature available", which is allowed only
// when opts.AllowUnsigned is true.
func InstallFromBytes(
	ctx context.Context, db store.Store,
	bundleBytes, sigBytes []byte, opts InstallOptions,
) (*store.InstalledSkill, *InstallReview, error) {
	if opts.SkillsDir == "" {
		return nil, nil, errors.New("InstallFromBytes: SkillsDir required")
	}
	if int64(len(bundleBytes)) > MaxBundleSize {
		return nil, nil, fmt.Errorf("%w: bundle exceeds %d bytes",
			ErrBundleMalformed, MaxBundleSize)
	}
	if len(sigBytes) == 0 && !opts.AllowUnsigned {
		return nil, nil, fmt.Errorf("%w: missing signature", ErrInvalidSignature)
	}
	review, err := verifyAndReview(ctx, db, bundleBytes, sigBytes, opts)
	if err != nil {
		return nil, review, err
	}
	row, err := commitInstall(ctx, db, bundleBytes, sigBytes, review, opts)
	if err != nil {
		return nil, review, err
	}
	return row, review, nil
}

// Remove uninstalls a skill: deletes the on-disk directory and the row.
// Returns ErrSkillNotInstalled if the row is absent. The directory deletion
// is best-effort if the row was missing the on-disk dir (warning logged by
// caller). Filesystem and DB are always brought to a consistent state.
func Remove(ctx context.Context, db store.Store, skillsDir, name string) error {
	if name == "" {
		return errors.New("Remove: name required")
	}
	if _, err := db.GetInstalledSkill(ctx, name); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("%w: %s", ErrSkillNotInstalled, name)
		}
		return err
	}
	if err := db.DeleteInstalledSkill(ctx, name); err != nil {
		return fmt.Errorf("delete row: %w", err)
	}
	skillDir := filepath.Join(skillsDir, name)
	if err := os.RemoveAll(skillDir); err != nil {
		return fmt.Errorf("remove %s: %w", skillDir, err)
	}
	return nil
}

// List returns every installed skill ordered by name.
func List(ctx context.Context, db store.Store) ([]store.InstalledSkill, error) {
	return db.ListInstalledSkills(ctx)
}

// readCapped reads up to max+1 bytes from path; returns ErrBundleMalformed
// when the file exceeds max.
func readCapped(path string, max int64) ([]byte, error) {
	f, err := os.Open(path) //nolint:gosec
	if err != nil {
		return nil, fmt.Errorf("open bundle: %w", err)
	}
	defer f.Close() //nolint:errcheck
	buf, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, fmt.Errorf("read bundle: %w", err)
	}
	if int64(len(buf)) > max {
		return nil, fmt.Errorf("%w: bundle exceeds %d bytes", ErrBundleMalformed, max)
	}
	return buf, nil
}

// readSignatureSibling reads <bundle>.minisig if present.
func readSignatureSibling(bundlePath string) ([]byte, error) {
	sigPath := bundlePath + ".minisig"
	b, err := os.ReadFile(sigPath) //nolint:gosec
	if err != nil {
		return nil, fmt.Errorf("read signature %s: %w", sigPath, err)
	}
	return b, nil
}

// commitInstall does the on-disk extract + DB row write inside one tx.
// On any failure the temp dir is removed and the tx rolls back, so the
// installation is all-or-nothing.
func commitInstall(
	ctx context.Context, db store.Store, bundleBytes, sigBytes []byte,
	r *InstallReview, opts InstallOptions,
) (*store.InstalledSkill, error) {
	if !opts.Force {
		if existing, err := db.GetInstalledSkill(ctx, r.Manifest.Name); err == nil {
			return nil, fmt.Errorf("%w: %s (%s)",
				ErrSkillAlreadyInstalled, existing.Name, existing.Version)
		} else if !errors.Is(err, store.ErrNotFound) {
			return nil, err
		}
	}
	stagedDir, err := stageBundle(opts.SkillsDir, r.Manifest.Name, bundleBytes)
	if err != nil {
		return nil, err
	}
	// Stash the original bytes inside the staged dir so M2.7 mesh share can
	// forward the signed bundle verbatim. Best-effort: a missing sig is fine
	// when AllowUnsigned permitted the install.
	if err := WriteBundleCache(
		filepath.Dir(stagedDir),
		filepath.Base(stagedDir),
		bundleBytes, sigBytes,
	); err != nil {
		_ = os.RemoveAll(stagedDir)
		return nil, err
	}
	row, err := finalizeInstall(ctx, db, r, opts, stagedDir)
	if err != nil {
		_ = os.RemoveAll(stagedDir)
		return nil, err
	}
	return row, nil
}

// finalizeInstall renames the staged dir into the final location and writes
// the registry row inside a single store transaction. The directory rename
// is the last filesystem mutation so a DB error rolls back cleanly.
func finalizeInstall(
	ctx context.Context, db store.Store,
	r *InstallReview, opts InstallOptions, stagedDir string,
) (*store.InstalledSkill, error) {
	finalDir := filepath.Join(opts.SkillsDir, r.Manifest.Name)
	if opts.Force {
		_ = os.RemoveAll(finalDir)
	}
	manifestJSON, err := json.Marshal(r.Manifest)
	if err != nil {
		return nil, fmt.Errorf("marshal manifest: %w", err)
	}
	row := &store.InstalledSkill{
		Name:         r.Manifest.Name,
		Version:      r.Manifest.Version,
		ManifestJSON: manifestJSON,
		SignerPubkey: r.SignerPubkey,
		Source:       opts.Source,
		InstalledAt:  time.Now().UTC(),
	}
	if err := db.Tx(ctx, func(tx store.Store) error {
		return tx.UpsertInstalledSkill(ctx, row)
	}); err != nil {
		return nil, err
	}
	if err := os.Rename(stagedDir, finalDir); err != nil {
		// Best-effort rollback: remove the row we just wrote.
		_ = db.DeleteInstalledSkill(ctx, row.Name)
		return nil, fmt.Errorf("rename %s -> %s: %w", stagedDir, finalDir, err)
	}
	return row, nil
}
