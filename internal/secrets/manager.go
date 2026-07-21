package secrets

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/don-works/mcplexer/internal/store"
)

// BrainSource is an optional secondary read-only secret source consulted
// by Get BEFORE the age-DB blob (dual-read — SPEC §8/§10). It is keyed by
// scope NAME + key (the human-readable scope identifier the brain repo
// commits), not by the internal scope id. A (nil, false, nil) result means
// "not present here" and the Manager falls back to the age-DB path; a
// non-nil error also falls back (the legacy store stays authoritative
// during rollout). Implemented by brain.SOPSSource.
type BrainSource interface {
	Get(ctx context.Context, scopeName, key string) ([]byte, bool, error)
}

// BrainLister is an OPTIONAL capability a BrainSource may also implement so
// List can present a complete inventory (SPEC §8/§10 dual-read symmetry).
// Without it, List enumerates the age-DB blob only (pre-existing behaviour)
// and SOPS-only keys stay invisible to secret__list_refs though Get resolves
// them. ListKeys returns brain-source key names for a scope NAME; (nil,
// false, nil) means "scope not present here" and any error is swallowed by
// List exactly as Get swallows brain errors. Kept separate from BrainSource
// so existing implementations keep satisfying BrainSource before they grow a
// ListKeys method — List type-asserts at call time, never at construction.
type BrainLister interface {
	ListKeys(ctx context.Context, scopeName string) ([]string, bool, error)
}

// Manager combines store-based auth scope storage with age encryption.
//
// auditor is optional (nil-safe); when set, every Get/Put/Delete/List
// emits a secret.* audit row carrying only scope_id + key (NEVER the
// plaintext value). Wire it post-construction via SetAuditor so existing
// callers keep compiling.
type Manager struct {
	store       store.AuthScopeStore
	encryptor   *AgeEncryptor
	auditor     Auditor
	brainSource BrainSource // optional; nil = today's behaviour (age-DB only)
	changeHook  func(context.Context, string)

	// dbFresherMu guards dbFresher, the set of (scopeID\x00key) pairs whose
	// authoritative value was last written via Put/Delete on the age-DB blob.
	// Get skips the SOPS brain source for these so a post-migration rotation
	// is not silently shadowed by the (mtime-cached, stale) SOPS file —
	// closing the dual-write coexistence hole (SPEC §10). A key is added on
	// every Put/Delete and never removed: once the DB is the writer for a
	// key, it stays authoritative for that key for this process lifetime.
	dbFresherMu sync.RWMutex
	dbFresher   map[string]struct{}
}

// dbFresherKey is the dbFresher set key for a (scopeID, key) pair. NUL is a
// safe separator (neither scope ids nor secret keys contain it).
func dbFresherKey(scopeID, key string) string { return scopeID + "\x00" + key }

// markDBFresher records that (scopeID, key) was just written to the age-DB
// blob, so subsequent Gets bypass the SOPS brain source for it.
func (m *Manager) markDBFresher(scopeID, key string) {
	m.dbFresherMu.Lock()
	if m.dbFresher == nil {
		m.dbFresher = make(map[string]struct{})
	}
	m.dbFresher[dbFresherKey(scopeID, key)] = struct{}{}
	m.dbFresherMu.Unlock()
}

// isDBFresher reports whether (scopeID, key) has been written to the age-DB
// blob since process start (so the SOPS source must be skipped for it).
func (m *Manager) isDBFresher(scopeID, key string) bool {
	m.dbFresherMu.RLock()
	defer m.dbFresherMu.RUnlock()
	_, ok := m.dbFresher[dbFresherKey(scopeID, key)]
	return ok
}

// SetBrainSource wires an optional SOPS-backed secondary source consulted
// by Get before the age-DB blob. nil (the default) preserves today's
// behaviour byte-for-byte. The dispatch-time secret:// substitution
// boundary (downstream/manager.go) is unchanged — this only changes the
// backing store Get reads from first.
func (m *Manager) SetBrainSource(src BrainSource) {
	m.brainSource = src
}

// SetChangeHook registers an optional callback fired after successful
// store-backed secret writes/deletes. The hook receives the auth scope ID and
// must not inspect plaintext; callers normally fan out async work.
func (m *Manager) SetChangeHook(fn func(context.Context, string)) {
	m.changeHook = fn
}

func (m *Manager) emitChange(ctx context.Context, scopeID string) {
	if m != nil && m.changeHook != nil {
		m.changeHook(ctx, scopeID)
	}
}

// NewManager creates a secrets Manager. Audit is disabled until the
// caller attaches a logger via SetAuditor.
func NewManager(s store.AuthScopeStore, enc *AgeEncryptor) *Manager {
	return &Manager{store: s, encryptor: enc}
}

// NewManagerWithAuditor creates a secrets Manager with audit emission
// wired from the start. Equivalent to NewManager + SetAuditor; provided
// so wiring sites that have an Auditor in scope can express intent in
// one call.
func NewManagerWithAuditor(s store.AuthScopeStore, enc *AgeEncryptor, a Auditor) *Manager {
	m := NewManager(s, enc)
	m.SetAuditor(a)
	return m
}

// Put encrypts and stores a secret under the given auth scope and key.
// Emits one secret.write audit row regardless of outcome — the audit
// row carries scope_id + key only, never the plaintext value.
func (m *Manager) Put(ctx context.Context, scopeID, key string, plaintext []byte) (err error) {
	defer func() {
		status, msg := auditStatusFor(err)
		m.emitAudit(ctx, scopeID, key, auditEventSecretWrite, status, msg)
	}()

	scope, err := m.store.GetAuthScope(ctx, scopeID)
	if err != nil {
		return fmt.Errorf("get auth scope %s: %w", scopeID, err)
	}

	secrets, err := m.decryptSecrets(scope.EncryptedData)
	if err != nil {
		return err
	}

	secrets[key] = string(plaintext)

	encrypted, err := m.encryptSecrets(secrets)
	if err != nil {
		return err
	}

	if err = m.store.UpdateAuthScopeEncryptedData(ctx, scopeID, encrypted); err != nil {
		return fmt.Errorf("update auth scope: %w", err)
	}
	// The age-DB blob is now the fresher writer for this key; ensure Get does
	// not let the (mtime-cached) SOPS source shadow the new value.
	m.markDBFresher(scopeID, key)
	m.emitChange(ctx, scopeID)
	return nil
}

// Get decrypts and returns a secret for the given auth scope and key.
// Emits one secret.read audit row. Not-found is recorded as
// status="error" with errMsg="key not found" — every read attempt is
// worth tracking for forensics ("who tried to read $X").
func (m *Manager) Get(ctx context.Context, scopeID, key string) (out []byte, err error) {
	defer func() {
		status, msg := auditStatusFor(err)
		m.emitAudit(ctx, scopeID, key, auditEventSecretRead, status, msg)
	}()

	scope, err := m.store.GetAuthScope(ctx, scopeID)
	if err != nil {
		return nil, fmt.Errorf("get auth scope %s: %w", scopeID, err)
	}

	// Dual-read (SPEC §8/§10): consult the SOPS brain source first, keyed
	// by scope NAME. A hit short-circuits; a miss or an error falls through
	// to the authoritative age-DB blob so rollout is reversible with no
	// data loss. brainSource errors are deliberately swallowed here (the
	// fall-through is the recovery path); the eventual age-path error, if
	// any, is what the caller sees.
	if m.brainSource != nil && !m.isDBFresher(scopeID, key) {
		if v, ok, bErr := m.brainSource.Get(ctx, scope.Name, key); bErr == nil && ok {
			return v, nil
		}
	}

	secrets, err := m.decryptSecrets(scope.EncryptedData)
	if err != nil {
		return nil, err
	}

	val, ok := secrets[key]
	if !ok {
		err = store.ErrNotFound
		return nil, err
	}
	return []byte(val), nil
}

// List returns all secret key names for the given auth scope (no values).
// Emits one secret.list audit row recording that the scope was
// enumerated. The audit row deliberately omits the returned key set and
// its size — only "scope was enumerated" is forensically interesting,
// and a count would leak inventory state to a redacted reader.
func (m *Manager) List(ctx context.Context, scopeID string) (out []string, err error) {
	defer func() {
		// Successful read-only enumeration is intentionally NOT audited.
		// secret__list_refs is a read-only discovery call (ReadOnlyHint)
		// that agents make on nearly every session, and handleSecretListRefs
		// calls List once per auth scope — so a success row per scope per
		// call floods the audit trail with no forensic value (no secret
		// VALUES are revealed, only that key names exist). We still record
		// FAILED/denied enumerations, which are the security-interesting
		// events.
		if err == nil {
			return
		}
		status, msg := auditStatusFor(err)
		m.emitAudit(ctx, scopeID, "", auditEventSecretList, status, msg)
	}()

	scope, err := m.store.GetAuthScope(ctx, scopeID)
	if err != nil {
		return nil, fmt.Errorf("get auth scope %s: %w", scopeID, err)
	}

	secrets, err := m.decryptSecrets(scope.EncryptedData)
	if err != nil {
		return nil, err
	}

	// Seed the inventory with the authoritative age-DB blob keys.
	seen := make(map[string]struct{}, len(secrets))
	keys := make([]string, 0, len(secrets))
	for k := range secrets {
		seen[k] = struct{}{}
		keys = append(keys, k)
	}

	// Dual-source (SPEC §8/§10): fold in SOPS-only keys so the inventory
	// matches what Get resolves. age-DB keys win (already in `seen`); a key
	// the DB authoritatively deleted (isDBFresher, absent from the blob) must
	// NOT resurface from the stale SOPS cache — mirroring Get/Delete
	// shadowing. Brain errors are swallowed; the age-DB enumeration stands.
	if lister, ok := m.brainSource.(BrainLister); m.brainSource != nil && ok {
		if brainKeys, present, bErr := lister.ListKeys(ctx, scope.Name); bErr == nil && present {
			for _, k := range brainKeys {
				if _, dup := seen[k]; dup || m.isDBFresher(scopeID, k) {
					continue
				}
				seen[k] = struct{}{}
				keys = append(keys, k)
			}
		}
	}
	return keys, nil
}

// Delete removes a secret key from the given auth scope. Emits one
// secret.delete audit row regardless of outcome.
func (m *Manager) Delete(ctx context.Context, scopeID, key string) (err error) {
	defer func() {
		status, msg := auditStatusFor(err)
		m.emitAudit(ctx, scopeID, key, auditEventSecretDelete, status, msg)
	}()

	scope, err := m.store.GetAuthScope(ctx, scopeID)
	if err != nil {
		return fmt.Errorf("get auth scope %s: %w", scopeID, err)
	}

	secrets, err := m.decryptSecrets(scope.EncryptedData)
	if err != nil {
		return err
	}

	if _, ok := secrets[key]; !ok {
		err = store.ErrNotFound
		return err
	}
	delete(secrets, key)

	encrypted, err := m.encryptSecrets(secrets)
	if err != nil {
		return err
	}

	if err = m.store.UpdateAuthScopeEncryptedData(ctx, scopeID, encrypted); err != nil {
		return err
	}
	// Deletion is authoritative on the age-DB side; shadow the SOPS source so
	// a deleted key does not reappear from the stale cached file.
	m.markDBFresher(scopeID, key)
	m.emitChange(ctx, scopeID)
	return nil
}

// decryptSecrets decrypts the stored blob into a key/value map.
func (m *Manager) decryptSecrets(data []byte) (map[string]string, error) {
	if len(data) == 0 {
		return make(map[string]string), nil
	}

	plaintext, err := m.encryptor.Decrypt(data)
	if err != nil {
		return nil, fmt.Errorf("decrypt secrets: %w", err)
	}
	defer ZeroBytes(plaintext)

	var secrets map[string]string
	if err := json.Unmarshal(plaintext, &secrets); err != nil {
		return nil, fmt.Errorf("unmarshal secrets: %w", err)
	}
	return secrets, nil
}

// encryptSecrets serializes and encrypts a key/value map.
func (m *Manager) encryptSecrets(secrets map[string]string) ([]byte, error) {
	data, err := json.Marshal(secrets)
	if err != nil {
		return nil, fmt.Errorf("marshal secrets: %w", err)
	}

	encrypted, err := m.encryptor.Encrypt(data)
	if err != nil {
		return nil, fmt.Errorf("encrypt secrets: %w", err)
	}
	return encrypted, nil
}
