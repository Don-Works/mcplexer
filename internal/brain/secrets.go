package brain

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"filippo.io/age"
	"github.com/getsops/sops/v3/decrypt"
	"gopkg.in/yaml.v3"
)

// EnvAgeKeyFile is the SOPS env var pointing at the age identity (private
// key) file. The brain reuses SOPS's own convention so an operator can
// also drive the `sops` CLI against the same file. Defaults to
// ~/.mcplexer/secrets/age/keys.txt (SPEC §8 key management).
const EnvAgeKeyFile = "SOPS_AGE_KEY_FILE"

// DefaultAgeKeyRelPath is the age identity file location relative to the
// mcplexer data dir. The private key stays machine-local under the
// existing lockdown (0600 file, 0700 dir) — only public recipients ever
// land in the repo.
const DefaultAgeKeyRelPath = "secrets/age/keys.txt"

// scopesFileRelPath is the SOPS-encrypted scope store inside the brain
// repo. Value-only encrypted (scope names + type + redaction_hints stay
// plaintext for reviewability — SPEC §8).
const scopesFileRelPath = "global/secrets/scopes.enc.yaml"

// ScopeSecrets is the plaintext, in-memory shape of one auth scope's
// secret material. Type + RedactionHints are committed in plaintext; only
// Values are SOPS-encrypted on disk.
type ScopeSecrets struct {
	Type           string            `yaml:"type,omitempty"`
	RedactionHints []string          `yaml:"redaction_hints,omitempty"`
	Values         map[string]string `yaml:"values"`
}

// ScopesFileRelPath returns the repo-relative path to the encrypted scope
// store. Exported so the migrate tool + wiring can locate it without
// re-deriving the constant.
func ScopesFileRelPath() string { return scopesFileRelPath }

// SOPSSource is the SOPS-backed secret read path. It decrypts
// global/secrets/scopes.enc.yaml in-process (no `sops` binary — SPEC §8)
// and caches the plaintext map, reloading only when the file mtime
// changes. The age private identity is supplied via SOPS_AGE_KEY_FILE,
// which the loader sets from ageKeyFile when the env is unset.
//
// SOPSSource never logs or returns plaintext beyond the requested value,
// and the dispatch-time secret:// substitution boundary
// (downstream/manager.go) is unchanged — this only changes the backing
// store the secrets.Manager consults first.
type SOPSSource struct {
	path       string
	ageKeyFile string

	mu       sync.Mutex
	cache    map[string]ScopeSecrets
	cacheMod int64 // unix-nano mtime of the file the cache was built from
	loaded   bool
}

// NewSOPSSource constructs a SOPS secret source rooted at the brain dir.
// ageKeyFile points at the age identity; when empty the caller-supplied
// SOPS_AGE_KEY_FILE env (if any) is used by the decrypt path.
func NewSOPSSource(brainDir, ageKeyFile string) *SOPSSource {
	return &SOPSSource{
		path:       filepath.Join(brainDir, scopesFileRelPath),
		ageKeyFile: ageKeyFile,
	}
}

// Path returns the absolute path to the encrypted scopes file.
func (s *SOPSSource) Path() string { return s.path }

// Get returns the plaintext bytes for (scopeID, key), or store-style
// not-found semantics via a nil result + false. It loads/reloads the
// encrypted file on demand (mtime-gated cache). Errors decrypting are
// returned so the caller can fall back to the legacy age-DB blob (dual
// read — SPEC §10).
//
// Note: scopeID here is matched against the scope *name* recorded in the
// file (the human-readable key), mirroring how the migrate tool writes
// scopes keyed by their KEY/name. The caller resolves a scope row to its
// name before consulting this source.
func (s *SOPSSource) Get(ctx context.Context, scopeName, key string) ([]byte, bool, error) {
	scopes, err := s.load()
	if err != nil {
		return nil, false, err
	}
	sc, ok := scopes[scopeName]
	if !ok {
		return nil, false, nil
	}
	val, ok := sc.Values[key]
	if !ok {
		return nil, false, nil
	}
	return []byte(val), true, nil
}

// load returns the decrypted scope map, reloading only when the file's
// mtime has changed since the cache was built. A missing file yields an
// empty map (no scopes migrated yet) rather than an error so the dual-read
// fallback stays clean.
func (s *SOPSSource) load() (map[string]ScopeSecrets, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	fi, statErr := os.Stat(s.path)
	if os.IsNotExist(statErr) {
		s.cache = map[string]ScopeSecrets{}
		s.loaded = true
		s.cacheMod = 0
		return s.cache, nil
	}
	if statErr != nil {
		return nil, fmt.Errorf("brain secrets: stat %s: %w", s.path, statErr)
	}

	mod := fi.ModTime().UnixNano()
	if s.loaded && mod == s.cacheMod {
		return s.cache, nil
	}

	scopes, err := s.decryptFile()
	if err != nil {
		return nil, err
	}
	s.cache = scopes
	s.cacheMod = mod
	s.loaded = true
	return s.cache, nil
}

// decryptFile runs the in-process SOPS decrypt and unmarshals into the
// scope map. It sets SOPS_AGE_KEY_FILE from the configured key file when
// the env is unset so the age keysource can find the private identity.
func (s *SOPSSource) decryptFile() (map[string]ScopeSecrets, error) {
	if s.ageKeyFile != "" && os.Getenv(EnvAgeKeyFile) == "" {
		// Scope the env to this process; the daemon owns it. Best-effort —
		// a set error is non-fatal because decrypt may still succeed from
		// a default key location.
		_ = os.Setenv(EnvAgeKeyFile, s.ageKeyFile)
	}

	plaintext, err := decrypt.File(s.path, "yaml")
	if err != nil {
		return nil, fmt.Errorf("brain secrets: sops decrypt %s: %w", s.path, err)
	}

	var scopes map[string]ScopeSecrets
	if err := yaml.Unmarshal(plaintext, &scopes); err != nil {
		return nil, fmt.Errorf("brain secrets: unmarshal decrypted scopes: %w", err)
	}
	if scopes == nil {
		scopes = map[string]ScopeSecrets{}
	}
	return scopes, nil
}

// Reload forces a cache rebuild on the next Get (used after a migrate
// writes a fresh file in the same process).
func (s *SOPSSource) Reload() {
	s.mu.Lock()
	s.loaded = false
	s.cacheMod = 0
	s.mu.Unlock()
}

// DefaultAgeKeyFile returns the conventional age identity path under the
// mcplexer data dir: <dataDir>/secrets/age/keys.txt. The private key stays
// machine-local under the existing lockdown.
func DefaultAgeKeyFile(dataDir string) string {
	return filepath.Join(dataDir, DefaultAgeKeyRelPath)
}

// EnsureAgeKeyFile loads the age identity at keyPath, generating a fresh
// X25519 identity (0600 file, 0700 dir tree) when absent. Returns the
// public recipient strings (age1...) for use as SOPS recipients. The
// private key never leaves the machine; only recipients land in the repo.
func EnsureAgeKeyFile(keyPath string) ([]string, error) {
	if data, err := os.ReadFile(keyPath); err == nil {
		return ageRecipientsFromIdentities(string(data))
	}

	id, err := age.GenerateX25519Identity()
	if err != nil {
		return nil, fmt.Errorf("brain secrets: generate age identity: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(keyPath), 0o700); err != nil {
		return nil, fmt.Errorf("brain secrets: mkdir age key dir: %w", err)
	}
	content := fmt.Sprintf("# created by mcplexer brain\n# public key: %s\n%s\n", id.Recipient(), id)
	if err := os.WriteFile(keyPath, []byte(content), 0o600); err != nil {
		return nil, fmt.Errorf("brain secrets: write age key file: %w", err)
	}
	return []string{id.Recipient().String()}, nil
}

// ageRecipientsFromIdentities parses an age key file's contents and returns
// the public recipient string for each X25519 identity found.
func ageRecipientsFromIdentities(keyData string) ([]string, error) {
	identities, err := age.ParseIdentities(strings.NewReader(keyData))
	if err != nil {
		return nil, fmt.Errorf("brain secrets: parse age identities: %w", err)
	}
	out := make([]string, 0, len(identities))
	for _, id := range identities {
		if x, ok := id.(*age.X25519Identity); ok {
			out = append(out, x.Recipient().String())
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("brain secrets: no X25519 identities in age key file")
	}
	return out, nil
}
