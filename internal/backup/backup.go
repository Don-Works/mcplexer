// Package backup produces and restores tarball snapshots of the mcplexer
// data directory. The canonical use case is "let an agent backup → mutate
// → restore-on-broken" so config experiments are safe; a full backup is
// also a portable replica that brings up an identical gateway on a fresh
// machine.
//
// A backup (schema v3) always contains, when present:
//   - manifest.json    — id, created_at, version, sha256 of the DB, and
//     flags recording what else was captured (schema_version, includes_*)
//   - mcplexer.db      — produced by SQLite's VACUUM INTO (consistent under
//     concurrent writes, no need to stop the daemon)
//   - db.age           — the master age private key ({dbPath}.age). Without
//     it the encrypted DB column + secrets/ tree are cryptographically dead
//     on restore, so it is always shipped when it exists.
//   - mcplexer.yaml    — source=yaml config (lives only on disk, not the DB)
//   - api-key          — the HTTP API auth token
//   - addons/          — the addon YAML tree ({data_dir}/addons/)
//   - skills/          — installed skill bundles and cached skill artifacts
//   - secrets/         — copy of {data_dir}/secrets/ if present (age-encrypted
//     at rest; decryptable thanks to the bundled db.age master key)
//
// Machine IDENTITY files are captured by default (includeIdentity=true at
// the callers) so a backup is a drop-in replica that brings the SAME gateway
// up on a replacement machine. Exclude them (includeIdentity=false) only when
// restoring onto a second machine meant to run CONCURRENTLY with the original,
// to avoid a duplicate peer ID:
//   - p2p/identity.key.age   — the libp2p identity ({data_dir}/p2p/identity.key.age)
//   - secret-transfer.age.key — the peer→peer secret-transfer keypair
//
// Restore is destructive — it always takes a pre-restore snapshot first
// (with identity files included, so a rollback is a true rollback) and
// returns its ID so callers can roll back if the restore breaks something.
// Restoring requires a daemon restart to pick up the new state. Backups
// without a schema_version (pre-v2) still restore: only the artifacts
// actually present in the tarball are applied.
package backup

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// ErrNotFound is returned when a backup ID does not exist.
var ErrNotFound = errors.New("backup not found")

// Manifest describes a single backup. Stored as manifest.json inside the
// tarball; also returned alongside list/get responses so callers don't
// need to crack the archive open.
type Manifest struct {
	ID              string    `json:"id"`
	CreatedAt       time.Time `json:"created_at"`
	MCPlexerVersion string    `json:"mcplexer_version,omitempty"`
	Note            string    `json:"note,omitempty"`
	DBSHA256        string    `json:"db_sha256"`
	SizeBytes       int64     `json:"size_bytes"`
	IncludesSecrets bool      `json:"includes_secrets"`
	PreRestoreOf    string    `json:"pre_restore_of,omitempty"` // set on auto-snapshots taken before a restore

	// SchemaVersion is 3 for the portable-replica format (master key +
	// config + skills + opt-in identity). Absent / 0 means a legacy v1 backup
	// (manifest + db + secrets only); those still restore.
	SchemaVersion int `json:"schema_version,omitempty"`
	// IncludesMasterKey is true when db.age (the master age private key)
	// is in the tarball — required to decrypt the DB + secrets on restore.
	IncludesMasterKey bool `json:"includes_master_key"`
	// IncludesConfig is true when mcplexer.yaml and/or api-key and/or the
	// addons/ tree were captured.
	IncludesConfig bool `json:"includes_config"`
	// IncludesSkills is true when the installed skills tree was captured.
	IncludesSkills bool `json:"includes_skills"`
	// IncludesIdentity is true when machine-identity files (p2p identity,
	// secret-transfer key) were captured. Off unless includeIdentity was set.
	IncludesIdentity bool `json:"includes_identity"`
}

// currentSchemaVersion is the schema_version stamped into new backups.
const currentSchemaVersion = 3

// Service produces and restores backups for one data directory.
type Service struct {
	dataDir   string // absolute path to ~/.mcplexer (DB lives here, secrets/ subdir, backups/ subdir)
	dbPath    string // absolute path to the .db file (may be outside dataDir if MCPLEXER_DB_DSN was set)
	version   string // mcplexer version string, recorded in the manifest
	backupDir string // {dataDir}/backups
}

// New constructs a Service. dbPath is the SQLite file (typically
// {dataDir}/mcplexer.db). version is recorded in manifests.
func New(dataDir, dbPath, version string) *Service {
	return &Service{
		dataDir:   filepath.Clean(dataDir),
		dbPath:    filepath.Clean(dbPath),
		version:   version,
		backupDir: filepath.Join(filepath.Clean(dataDir), "backups"),
	}
}

// Create produces a tarball snapshot of the live data dir and writes it to
// {dataDir}/backups/<id>.tar.gz. The DB is captured via SQLite's VACUUM
// INTO so it's consistent even with concurrent writers. The master age key,
// config, api-key, addons/, skills/ and secrets/ are always captured when present;
// machine-identity files are captured only when includeIdentity is true.
func (s *Service) Create(ctx context.Context, note string, includeIdentity bool) (Manifest, error) {
	return s.createInternal(ctx, note, "", includeIdentity)
}

func (s *Service) createInternal(ctx context.Context, note, preRestoreOf string, includeIdentity bool) (Manifest, error) {
	if err := os.MkdirAll(s.backupDir, 0o700); err != nil {
		return Manifest{}, fmt.Errorf("mkdir backups: %w", err)
	}

	id := time.Now().UTC().Format("20060102-150405") + "-" + randomID(6)
	tarPath := filepath.Join(s.backupDir, id+".tar.gz")

	// Snapshot the DB to a temp file via VACUUM INTO.
	tmpDB := filepath.Join(s.backupDir, ".snap-"+id+".db")
	defer func() { _ = os.Remove(tmpDB) }()
	if err := vacuumInto(ctx, s.dbPath, tmpDB); err != nil {
		return Manifest{}, fmt.Errorf("vacuum into: %w", err)
	}

	dbSum, err := sha256File(tmpDB)
	if err != nil {
		return Manifest{}, fmt.Errorf("hash db: %w", err)
	}

	// Resolve which always-on artifacts actually exist, plus identity
	// artifacts when opted in. This drives both the tarball contents and
	// the manifest flags so they never drift.
	arts := existingArtifacts(s.dataArtifacts())
	if includeIdentity {
		arts = append(arts, existingArtifacts(s.identityArtifacts())...)
	}

	mf := Manifest{
		ID:                id,
		CreatedAt:         time.Now().UTC(),
		MCPlexerVersion:   s.version,
		Note:              note,
		DBSHA256:          dbSum,
		IncludesSecrets:   hasArtifact(arts, "secrets"),
		PreRestoreOf:      preRestoreOf,
		SchemaVersion:     currentSchemaVersion,
		IncludesMasterKey: hasArtifact(arts, "db.age"),
		IncludesConfig: hasArtifact(arts, "mcplexer.yaml") ||
			hasArtifact(arts, "api-key") || hasArtifact(arts, "addons"),
		IncludesSkills: hasArtifact(arts, "skills"),
		IncludesIdentity: includeIdentity && (hasArtifact(arts, "p2p/identity.key.age") ||
			hasArtifact(arts, "secret-transfer.age.key")),
	}

	if err := writeTarball(tarPath, mf, tmpDB, arts); err != nil {
		_ = os.Remove(tarPath)
		return Manifest{}, fmt.Errorf("write tarball: %w", err)
	}

	info, err := os.Stat(tarPath)
	if err != nil {
		return Manifest{}, fmt.Errorf("stat tarball: %w", err)
	}
	mf.SizeBytes = info.Size()

	// Persist the size into the on-disk manifest by re-reading + rewriting.
	// Cheap because the manifest is tiny; keeps List() faithful.
	if err := patchManifestSize(tarPath, mf); err != nil {
		return Manifest{}, fmt.Errorf("patch manifest size: %w", err)
	}
	return mf, nil
}

// Restore replaces the live DB and secrets dir with the contents of the
// given backup. ALWAYS takes a pre-restore snapshot first and returns its
// ID, so callers can roll back if the restore turns out to be wrong.
//
// The daemon is NOT restarted automatically — running supervisors keep
// holding their own DB handles. Callers are expected to instruct the
// user (or the desktop app) to restart after this returns.
func (s *Service) Restore(ctx context.Context, id string) (preSnapshotID string, err error) {
	if !validID(id) {
		return "", ErrNotFound
	}
	tarPath := s.tarPath(id)
	if _, err := os.Stat(tarPath); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", ErrNotFound
		}
		return "", err
	}

	// Step 1: pre-restore snapshot. If this fails, abort — we won't
	// restore without an escape hatch. Include identity files so this
	// snapshot is a TRUE rollback target (the live machine's identity is
	// preserved even though identity is opt-in for user-facing backups).
	pre, err := s.createInternal(ctx, "auto-snapshot before restoring "+id, id, true)
	if err != nil {
		return "", fmt.Errorf("pre-restore snapshot: %w", err)
	}

	// Step 2: apply the backup over the live data dir.
	if err := applyBackup(tarPath, s.dataDir, s.dbPath, s.restoreTargets()); err != nil {
		return pre.ID, fmt.Errorf("apply backup (rollback available, snapshot=%s): %w", pre.ID, err)
	}
	return pre.ID, nil
}

// vacuumInto opens a separate connection to the live DB and runs
// VACUUM INTO to produce a consistent snapshot. Doesn't disturb other
// handles.
func vacuumInto(ctx context.Context, src, dst string) error {
	if err := os.Remove(dst); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	dsn := src + "?_busy_timeout=10000"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	// VACUUM INTO requires a literal string; the path is server-side, not user-supplied.
	_, err = db.ExecContext(ctx, fmt.Sprintf("VACUUM INTO %s", quoteSQLString(dst)))
	return err
}
