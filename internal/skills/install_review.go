package skills

import (
	"context"
	"fmt"
	"strings"

	"aead.dev/minisign"

	"github.com/don-works/mcplexer/internal/store"
)

// verifyAndReview cryptographically verifies the bundle (when signature is
// present), looks up the signer in the trust store, parses + validates the
// manifest, and checks capability requirements. Returns the review payload
// the CLI prints before the y/N prompt.
func verifyAndReview(
	ctx context.Context, db store.Store,
	bundleBytes, sigBytes []byte, opts InstallOptions,
) (*InstallReview, error) {
	r := &InstallReview{Source: opts.Source}
	if len(sigBytes) > 0 {
		if err := verifyBundleSignature(ctx, db, bundleBytes, sigBytes, r); err != nil {
			return r, err
		}
	} else if !opts.AllowUnsigned {
		return r, fmt.Errorf("%w: missing .minisig sibling", ErrInvalidSignature)
	}
	man, err := manifestFromBundle(bundleBytes)
	if err != nil {
		return r, err
	}
	r.Manifest = man
	if err := Validate(man); err != nil {
		return r, err
	}
	missing, err := findMissingCapabilities(ctx, db, man)
	if err != nil {
		return r, err
	}
	r.MissingMCP = missing
	if len(missing) > 0 {
		return r, fmt.Errorf("%w: %s",
			ErrCapabilityNotConfigured, strings.Join(missing, ", "))
	}
	return r, nil
}

// verifyBundleSignature parses the signature, looks up the signer's pubkey
// in trusted_signers, and runs the cryptographic check.
func verifyBundleSignature(
	ctx context.Context, db store.Store,
	bundleBytes, sigBytes []byte, r *InstallReview,
) error {
	var sig minisign.Signature
	if err := sig.UnmarshalText(sigBytes); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidSignature, err)
	}
	keyID := fmt.Sprintf("%016X", sig.KeyID)
	r.SignerKeyID = keyID
	signer, err := lookupTrustedSigner(ctx, db, keyID)
	if err != nil {
		// Mark as untrusted but still attempt structural verify, so the
		// review can show a clear "unknown signer" warning.
		r.UnknownSigner = true
		return err
	}
	pk, err := ParsePublicKey(signer.PubkeyString)
	if err != nil {
		return fmt.Errorf("trust store row corrupt: %w", err)
	}
	if err := Verify(bundleBytes, sigBytes, pk); err != nil {
		return err
	}
	r.SignerPubkey = signer.PubkeyString
	r.SignerName = signer.Name
	return nil
}

// lookupTrustedSigner finds an active (non-revoked) signer by 16-hex key id.
func lookupTrustedSigner(
	ctx context.Context, db store.Store, keyID string,
) (*store.TrustedSigner, error) {
	rows, err := db.ListTrustedSigners(ctx)
	if err != nil {
		return nil, fmt.Errorf("list trusted signers: %w", err)
	}
	for i := range rows {
		if rows[i].PubkeyID != keyID {
			continue
		}
		if rows[i].RevokedAt != nil {
			return nil, fmt.Errorf("%w: %s revoked at %s",
				ErrUntrustedSigner, keyID, rows[i].RevokedAt.Format("2006-01-02"))
		}
		return &rows[i], nil
	}
	return nil, fmt.Errorf("%w: key %s not in trust store", ErrUntrustedSigner, keyID)
}

// findMissingCapabilities returns any MCP-server names declared in the
// manifest that are not present in the local downstream_servers table.
// Optional servers are skipped.
func findMissingCapabilities(
	ctx context.Context, db store.Store, m *Manifest,
) ([]string, error) {
	if len(m.Capabilities.MCPServers) == 0 {
		return nil, nil
	}
	servers, err := db.ListDownstreamServers(ctx)
	if err != nil {
		return nil, fmt.Errorf("list downstream servers: %w", err)
	}
	have := make(map[string]struct{}, len(servers))
	for i := range servers {
		have[servers[i].ToolNamespace] = struct{}{}
		have[servers[i].Name] = struct{}{}
	}
	var missing []string
	for _, want := range m.Capabilities.MCPServers {
		if want.Optional {
			continue
		}
		if _, ok := have[want.Name]; !ok {
			missing = append(missing, want.Name)
		}
	}
	return missing, nil
}
