package brain

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/don-works/mcplexer/internal/store"
)

// Decryptor decrypts an age blob to plaintext. Satisfied by
// *secrets.AgeEncryptor (its Decrypt method). Defined as an interface so
// the brain package stays decoupled from internal/secrets (no import
// cycle) and tests can supply a stub.
type Decryptor interface {
	Decrypt(ciphertext []byte) ([]byte, error)
}

// secretSource is the minimal store surface the migrator needs: enumerate
// auth scopes (with their encrypted blobs) and oauth providers (with their
// encrypted client secrets). Satisfied by store.Store.
type secretSource interface {
	ListAuthScopes(ctx context.Context) ([]store.AuthScope, error)
	ListOAuthProviders(ctx context.Context) ([]store.OAuthProvider, error)
}

// oauthClientSecretKey is the synthetic KEY name under which an OAuth
// provider's client secret is stored in the SOPS file's per-provider
// scope. Keeps it distinct from auth-scope KEYs.
const oauthClientSecretKey = "OAUTH_CLIENT_SECRET"

// MigrateReport summarises a brain_migrate_secrets run.
type MigrateReport struct {
	Scopes         int      `json:"scopes"`
	Providers      int      `json:"providers"`
	Values         int      `json:"values"`
	RoundTripOK    bool     `json:"round_trip_ok"`
	Recipients     []string `json:"recipients"`
	WroteSopsRules bool     `json:"wrote_sops_rules"`
}

// MigrateSecrets decrypts every auth_scope's secret values and every
// OAuth provider's client secret with the CURRENT age key, re-encrypts
// them into <brainDir>/global/secrets/scopes.enc.yaml under the same KEY
// names using the configured age recipients, verifies the round-trip by
// re-decrypting in-process, and leaves the DB blobs untouched (dual-read
// rollout — SPEC §8/§10). Idempotent: re-running overwrites the file with
// the same content.
//
// It also writes/refreshes .sops.yaml so the `sops` CLI shares the
// in-process policy. Returns a report for the admin tool to surface.
func MigrateSecrets(
	ctx context.Context,
	brainDir string,
	recipients []string,
	src secretSource,
	dec Decryptor,
	ageKeyFile string,
) (*MigrateReport, error) {
	if len(recipients) == 0 {
		return nil, fmt.Errorf("brain secrets: no age recipients configured for migration")
	}

	scopes, valueCount, nScopes, nProviders, err := collectScopes(ctx, src, dec)
	if err != nil {
		return nil, err
	}

	if err := WriteSopsConfig(brainDir, recipients); err != nil {
		return nil, err
	}
	if err := WriteEncryptedScopes(brainDir, recipients, scopes); err != nil {
		return nil, err
	}

	// Round-trip verify: re-decrypt the file we just wrote and assert the
	// decrypted scope map equals what we encrypted. A mismatch aborts the
	// rollout signal (the report carries round_trip_ok=false).
	rtOK, err := verifyRoundTrip(brainDir, ageKeyFile, scopes)
	if err != nil {
		return nil, err
	}

	return &MigrateReport{
		Scopes:         nScopes,
		Providers:      nProviders,
		Values:         valueCount,
		RoundTripOK:    rtOK,
		Recipients:     recipients,
		WroteSopsRules: true,
	}, nil
}

// collectScopes decrypts all auth-scope blobs + oauth client secrets into
// the plaintext scope map, returning value/scope/provider counts.
func collectScopes(
	ctx context.Context,
	src secretSource,
	dec Decryptor,
) (map[string]ScopeSecrets, int, int, int, error) {
	out := map[string]ScopeSecrets{}
	valueCount := 0

	authScopes, err := src.ListAuthScopes(ctx)
	if err != nil {
		return nil, 0, 0, 0, fmt.Errorf("brain secrets: list auth scopes: %w", err)
	}
	for i := range authScopes {
		sc := &authScopes[i]
		values, err := decryptScopeValues(dec, sc.EncryptedData)
		if err != nil {
			return nil, 0, 0, 0, fmt.Errorf("brain secrets: decrypt scope %q: %w", sc.Name, err)
		}
		if len(values) == 0 {
			continue
		}
		out[sc.Name] = ScopeSecrets{
			Type:           sc.Type,
			RedactionHints: parseRedactionHints(sc.RedactionHints),
			Values:         values,
		}
		valueCount += len(values)
	}

	providers, err := src.ListOAuthProviders(ctx)
	if err != nil {
		return nil, 0, 0, 0, fmt.Errorf("brain secrets: list oauth providers: %w", err)
	}
	nProviders := 0
	for i := range providers {
		p := &providers[i]
		if len(p.EncryptedClientSecret) == 0 {
			continue
		}
		plain, err := dec.Decrypt(p.EncryptedClientSecret)
		if err != nil {
			return nil, 0, 0, 0, fmt.Errorf("brain secrets: decrypt oauth provider %q: %w", p.Name, err)
		}
		out["oauth:"+p.Name] = ScopeSecrets{
			Type:   "oauth_provider",
			Values: map[string]string{oauthClientSecretKey: string(plain)},
		}
		valueCount++
		nProviders++
	}

	return out, valueCount, len(out) - nProviders, nProviders, nil
}

// decryptScopeValues decrypts an auth-scope's age blob (JSON
// map[string]string) to plaintext key/value pairs. An empty blob yields an
// empty map (a scope may carry only oauth token data).
func decryptScopeValues(dec Decryptor, blob []byte) (map[string]string, error) {
	if len(blob) == 0 {
		return map[string]string{}, nil
	}
	plain, err := dec.Decrypt(blob)
	if err != nil {
		return nil, err
	}
	var values map[string]string
	if err := json.Unmarshal(plain, &values); err != nil {
		return nil, fmt.Errorf("unmarshal scope values: %w", err)
	}
	if values == nil {
		values = map[string]string{}
	}
	return values, nil
}

// parseRedactionHints decodes the JSON-array redaction_hints column into a
// string slice. Best-effort: malformed/empty yields nil (the hints are
// plaintext metadata, not security-critical).
func parseRedactionHints(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var hints []string
	if err := json.Unmarshal(raw, &hints); err != nil {
		return nil
	}
	return hints
}

// verifyRoundTrip re-decrypts the written file in-process and asserts the
// recovered scope map matches what was encrypted (deep equality over
// names + values). Returns false on any divergence.
func verifyRoundTrip(brainDir, ageKeyFile string, want map[string]ScopeSecrets) (bool, error) {
	src := NewSOPSSource(brainDir, ageKeyFile)
	src.Reload()
	got, err := src.load()
	if err != nil {
		return false, fmt.Errorf("brain secrets: round-trip decrypt: %w", err)
	}
	return scopeMapsEqual(want, got), nil
}

// scopeMapsEqual compares two scope maps by name + per-key values + type.
// RedactionHints order is normalised before compare.
func scopeMapsEqual(a, b map[string]ScopeSecrets) bool {
	if len(a) != len(b) {
		return false
	}
	for name, sa := range a {
		sb, ok := b[name]
		if !ok || sa.Type != sb.Type {
			return false
		}
		if len(sa.Values) != len(sb.Values) {
			return false
		}
		for k, v := range sa.Values {
			if sb.Values[k] != v {
				return false
			}
		}
		ha, hb := append([]string(nil), sa.RedactionHints...), append([]string(nil), sb.RedactionHints...)
		sort.Strings(ha)
		sort.Strings(hb)
		if len(ha) != len(hb) {
			return false
		}
		for i := range ha {
			if ha[i] != hb[i] {
				return false
			}
		}
	}
	return true
}
