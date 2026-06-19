// Package skills — bundle cache so re-shared skills carry the original
// signature.
//
// After an install (M2.2), the on-disk skill dir contains the extracted
// files but not the original .mcskill archive bytes. M2.7 share-via-mesh
// needs to forward the bundle + signature verbatim so the receiver can
// run the same signature check as a registry install. We therefore stash
// the original bundle alongside the extracted contents, in the hidden
// files .bundle.mcskill and .bundle.mcskill.minisig under the skill dir.
//
// The cache is best-effort: a missing bundle file simply means re-share
// is unavailable for that skill (the user can re-install from source).
package skills

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

const (
	bundleCacheName    = ".bundle.mcskill"
	bundleCacheSigName = ".bundle.mcskill.minisig"
)

// ErrBundleCacheMissing indicates the skill is installed but the original
// bundle bytes were not preserved (e.g. installed before M2.7 landed). The
// gateway translates this to a user-friendly "re-install to enable sharing"
// error. Callers can use errors.Is to match.
var ErrBundleCacheMissing = errors.New("skill bundle cache missing")

// WriteBundleCache stashes the original bundle + signature bytes inside
// {skillsDir}/{name}/. Both files are written 0o600 — the bundle is
// public but its location is internal.
func WriteBundleCache(
	skillsDir, name string, bundleBytes, sigBytes []byte,
) error {
	dir := filepath.Join(skillsDir, name)
	if err := os.WriteFile(
		filepath.Join(dir, bundleCacheName), bundleBytes, 0o600,
	); err != nil {
		return fmt.Errorf("write bundle cache: %w", err)
	}
	if len(sigBytes) == 0 {
		return nil
	}
	if err := os.WriteFile(
		filepath.Join(dir, bundleCacheSigName), sigBytes, 0o600,
	); err != nil {
		return fmt.Errorf("write bundle sig cache: %w", err)
	}
	return nil
}

// ReadBundleCache returns the original .mcskill bytes + .minisig bytes for
// an installed skill. ErrBundleCacheMissing is returned when the cache
// file is absent (e.g. legacy install). Signature may be empty on a skill
// that was installed with --allow-unsigned.
func ReadBundleCache(skillsDir, name string) ([]byte, []byte, error) {
	dir := filepath.Join(skillsDir, name)
	bundle, err := os.ReadFile(filepath.Join(dir, bundleCacheName)) //nolint:gosec
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil, fmt.Errorf("%w: skill %q was installed before M2.7 and cannot be re-shared; re-install it from the original source", ErrBundleCacheMissing, name)
		}
		return nil, nil, fmt.Errorf("read bundle cache: %w", err)
	}
	sig, err := os.ReadFile(filepath.Join(dir, bundleCacheSigName)) //nolint:gosec
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, nil, fmt.Errorf("read bundle sig cache: %w", err)
	}
	return bundle, sig, nil
}
