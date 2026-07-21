package skillregistry

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"

	"github.com/don-works/mcplexer/internal/store"
)

// ErrBundleNotPresent is returned when a registry entry has no bundle.
var ErrBundleNotPresent = errors.New("skillregistry: entry has no bundle")

// ValidateBundle checks that raw is a valid tar.gz containing a SKILL.md
// at the archive root (or under one top-level directory) whose contents
// equal expectedBody. Returns the hex sha256 of raw on success.
//
// We enforce the SKILL.md ≡ body invariant at publish time so the
// search index and skill_get text response stay consistent with what
// skill_install would extract — agents asking by intent never get one
// thing in the indexed body and a different thing in the bundle.
func ValidateBundle(raw []byte, expectedBody string) (string, error) {
	if len(raw) == 0 {
		return "", errors.New("bundle is empty")
	}
	if len(raw) > MaxBundleBytes {
		return "", fmt.Errorf("bundle %d bytes exceeds cap of %d", len(raw), MaxBundleBytes)
	}

	skillMD, err := readSkillMDFromTarGz(raw)
	if err != nil {
		return "", err
	}
	if normalizeForCompare(skillMD) != normalizeForCompare(expectedBody) {
		return "", errors.New("bundle SKILL.md does not match the body argument (publish them together)")
	}

	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// readSkillMDFromTarGz returns the contents of the first SKILL.md the
// archive yields, ignoring the leading directory component if every
// entry shares one (the common "tar -czf x.tgz ./skill-name" layout).
func readSkillMDFromTarGz(raw []byte) (string, error) {
	gz, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return "", fmt.Errorf("gzip: %w", err)
	}
	defer gz.Close() //nolint:errcheck

	tr := tar.NewReader(gz)
	var (
		body         []byte
		bytesRead    int64
		entriesRead  int
		entriesLimit = 4096
	)
	for {
		if entriesRead++; entriesRead > entriesLimit {
			return "", fmt.Errorf("bundle has more than %d entries — refusing", entriesLimit)
		}
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", fmt.Errorf("tar next: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg && hdr.Typeflag != 0 {
			continue
		}
		name := path.Clean(hdr.Name)
		if strings.HasPrefix(name, "/") || strings.HasPrefix(name, "..") {
			return "", fmt.Errorf("bundle has unsafe path %q", hdr.Name)
		}
		base := path.Base(name)
		depth := strings.Count(name, "/")
		if base == "SKILL.md" && depth <= 1 && body == nil {
			buf, readErr := io.ReadAll(io.LimitReader(tr, MaxBundleBytes))
			if readErr != nil {
				return "", fmt.Errorf("read SKILL.md: %w", readErr)
			}
			body = buf
			continue
		}
		bytesRead += hdr.Size
		if bytesRead > MaxBundleBytes {
			return "", fmt.Errorf("bundle content exceeds %d bytes uncompressed", MaxBundleBytes)
		}
	}
	if body == nil {
		return "", errors.New("bundle has no SKILL.md at the root or one level deep")
	}
	return string(body), nil
}

// normalizeForCompare strips trailing whitespace per line and a trailing
// final newline so cosmetic line-ending drift between the body argument
// and the file inside the tarball doesn't break the equivalence check.
func normalizeForCompare(s string) string {
	lines := strings.Split(s, "\n")
	for i, ln := range lines {
		lines[i] = strings.TrimRight(ln, " \t\r")
	}
	out := strings.Join(lines, "\n")
	return strings.TrimRight(out, "\n")
}

// FetchBundle returns (bundle, sha256, error) for the head version of
// name in scope. Returns ErrBundleNotPresent when the row exists but
// has no bundle attached, and store.ErrNotFound when the row itself is
// missing. Workspace rows shadow global rows of the same name.
func (r *Registry) FetchBundle(
	ctx context.Context, scope store.SkillScope, name string, ref VersionRef,
) ([]byte, string, error) {
	if r == nil || r.store == nil {
		return nil, "", errors.New("skillregistry: not initialised")
	}
	entry, err := r.Get(ctx, scope, name, ref)
	if err != nil {
		return nil, "", err
	}
	return r.FetchBundleForEntry(ctx, entry)
}

// FetchBundleForEntry loads the bundle for one already-resolved exact row.
// Unlike FetchBundle it performs no visibility or fallback resolution: if a
// selected workspace row disappears, it returns ErrNotFound rather than
// silently reading a global row with the same name/version.
func (r *Registry) FetchBundleForEntry(
	ctx context.Context, entry *store.SkillRegistryEntry,
) ([]byte, string, error) {
	if r == nil || r.store == nil {
		return nil, "", errors.New("skillregistry: not initialised")
	}
	if entry == nil {
		return nil, "", errors.New("skillregistry: bundle entry is nil")
	}
	if entry.BundleSHA256 == "" {
		return nil, "", ErrBundleNotPresent
	}
	bundle, sha, err := r.store.GetSkillRegistryBundle(ctx, entry.WorkspaceID, entry.Name, entry.Version)
	if err != nil {
		return nil, "", err
	}
	if len(bundle) == 0 {
		return nil, "", ErrBundleNotPresent
	}
	if sha != entry.BundleSHA256 {
		return nil, "", fmt.Errorf("bundle sha mismatch: row=%s blob=%s", entry.BundleSHA256, sha)
	}
	return bundle, sha, nil
}
