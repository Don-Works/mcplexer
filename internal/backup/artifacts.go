package backup

import (
	"os"
	"path/filepath"
)

// artifact describes one file (or tree) captured into / restored from a
// backup tarball: its on-disk source path and its name/prefix inside the
// archive.
type artifact struct {
	name   string // path inside the tarball (slash-separated)
	src    string // absolute on-disk path
	isTree bool   // true => walk src as a directory under name/
}

// dataArtifacts returns the always-on artifacts (everything except the
// snapshot DB, which is handled separately because it comes from a temp
// VACUUM file rather than a live path). These make a backup a portable
// replica: the master key decrypts the DB + secrets, the config carries
// source=yaml items, addons carry hot-created addon YAML, and skills keeps
// installed bundles available after restore.
//
// Every entry is gated on existence by the caller (writeArtifacts), so it
// is safe to list paths that may not exist on a given install.
func (s *Service) dataArtifacts() []artifact {
	return []artifact{
		{name: "db.age", src: s.dbPath + ".age"},
		{name: "mcplexer.yaml", src: filepath.Join(s.dataDir, "mcplexer.yaml")},
		{name: "api-key", src: filepath.Join(s.dataDir, "api-key")},
		{name: "secrets", src: filepath.Join(s.dataDir, "secrets"), isTree: true},
		{name: "addons", src: filepath.Join(s.dataDir, "addons"), isTree: true},
		{name: "skills", src: filepath.Join(s.dataDir, "skills"), isTree: true},
	}
}

// identityArtifacts returns the opt-in machine-identity artifacts. These
// are NOT data — restoring them onto a second live machine duplicates the
// peer ID — so they are only captured when includeIdentity is set.
func (s *Service) identityArtifacts() []artifact {
	return []artifact{
		{name: "p2p/identity.key.age", src: filepath.Join(s.dataDir, "p2p", "identity.key.age")},
		{name: "secret-transfer.age.key", src: filepath.Join(s.dataDir, "secret-transfer.age.key")},
	}
}

// restoreTarget maps a tarball entry name back to its absolute on-disk
// destination, applied during restore. Returns ok=false for entries that
// are not restorable artifacts (e.g. manifest.json, mcplexer.db, which the
// caller handles explicitly). The bool isTree mirrors writeArtifacts so the
// restorer knows whether the name is a tree prefix.
func (s *Service) restoreTargets() map[string]artifact {
	out := make(map[string]artifact)
	for _, a := range append(s.dataArtifacts(), s.identityArtifacts()...) {
		out[a.name] = artifact{name: a.name, src: s.destPath(a), isTree: a.isTree}
	}
	return out
}

// destPath returns the absolute on-disk path an artifact restores to. For
// the always-on/identity artifacts the source path IS the destination, so
// we just reuse it.
func (s *Service) destPath(a artifact) string { return a.src }

// existingArtifacts filters candidates down to those whose source path
// exists on disk (a regular file, or a non-empty directory for trees).
func existingArtifacts(candidates []artifact) []artifact {
	out := make([]artifact, 0, len(candidates))
	for _, a := range candidates {
		if a.isTree {
			if dirExists(a.src) {
				out = append(out, a)
			}
			continue
		}
		if fileExists(a.src) {
			out = append(out, a)
		}
	}
	return out
}

// hasArtifact reports whether arts contains an entry with the given name.
func hasArtifact(arts []artifact, name string) bool {
	for _, a := range arts {
		if a.name == name {
			return true
		}
	}
	return false
}

func fileExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.Mode().IsRegular()
}
