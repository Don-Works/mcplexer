package brain

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/getsops/sops/v3"
	"github.com/getsops/sops/v3/aes"
	"github.com/getsops/sops/v3/age"
	"github.com/getsops/sops/v3/cmd/sops/common"
	"github.com/getsops/sops/v3/config"
	"github.com/getsops/sops/v3/keyservice"
	yamlstore "github.com/getsops/sops/v3/stores/yaml"
	"github.com/getsops/sops/v3/version"
	"gopkg.in/yaml.v3"
)

// encryptedRegex targets ONLY the per-scope `values` sub-tree. Scope names,
// `type`, and `redaction_hints` stay plaintext so the committed file is
// reviewable + diffable (SPEC §8, Research 4(a)).
const encryptedRegex = "^values$"

// WriteEncryptedScopes builds the value-only SOPS-encrypted scope store at
// <brainDir>/global/secrets/scopes.enc.yaml for the given recipients
// (age public keys — Max's machines only, Appendix B #5).
//
// The plaintext map is marshalled to YAML, loaded into a SOPS tree, a data
// key is generated against the age recipients, and only the `values`
// sub-trees are encrypted (encrypted_regex). The age private key never
// touches the repo; only public recipients land in the file's sops
// metadata.
func WriteEncryptedScopes(brainDir string, recipients []string, scopes map[string]ScopeSecrets) error {
	if len(recipients) == 0 {
		return fmt.Errorf("brain secrets: no age recipients configured")
	}

	plain, err := yaml.Marshal(scopes)
	if err != nil {
		return fmt.Errorf("brain secrets: marshal scopes: %w", err)
	}

	keyGroup, err := ageKeyGroup(recipients)
	if err != nil {
		return err
	}

	store := yamlstore.NewStore(&config.YAMLStoreConfig{})
	branches, err := store.LoadPlainFile(plain)
	if err != nil {
		return fmt.Errorf("brain secrets: load plain scopes: %w", err)
	}

	outPath := filepath.Join(brainDir, scopesFileRelPath)
	tree := sops.Tree{
		Branches: branches,
		Metadata: sops.Metadata{
			KeyGroups:      []sops.KeyGroup{keyGroup},
			EncryptedRegex: encryptedRegex,
			Version:        version.Version,
		},
		FilePath: outPath,
	}

	svcs := []keyservice.KeyServiceClient{keyservice.NewLocalClient()}
	dataKey, errs := tree.GenerateDataKeyWithKeyServices(svcs)
	if len(errs) > 0 {
		return fmt.Errorf("brain secrets: generate data key: %v", errs)
	}

	if err := common.EncryptTree(common.EncryptTreeOpts{
		DataKey: dataKey,
		Tree:    &tree,
		Cipher:  aes.NewCipher(),
	}); err != nil {
		return fmt.Errorf("brain secrets: encrypt tree: %w", err)
	}

	encrypted, err := store.EmitEncryptedFile(tree)
	if err != nil {
		return fmt.Errorf("brain secrets: emit encrypted file: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(outPath), 0o700); err != nil {
		return fmt.Errorf("brain secrets: mkdir secrets dir: %w", err)
	}
	// Atomic temp+rename so a reader (or the watcher) never sees a
	// half-written secret file.
	tmp := outPath + ".tmp"
	if err := os.WriteFile(tmp, encrypted, 0o600); err != nil {
		return fmt.Errorf("brain secrets: write temp scopes file: %w", err)
	}
	if err := os.Rename(tmp, outPath); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("brain secrets: rename scopes file: %w", err)
	}
	return nil
}

// ageKeyGroup builds a single-group key set from age public recipients.
func ageKeyGroup(recipients []string) (sops.KeyGroup, error) {
	joined := strings.Join(recipients, ",")
	masterKeys, err := age.MasterKeysFromRecipients(joined)
	if err != nil {
		return nil, fmt.Errorf("brain secrets: parse age recipients: %w", err)
	}
	group := make(sops.KeyGroup, 0, len(masterKeys))
	for _, k := range masterKeys {
		group = append(group, k)
	}
	return group, nil
}
