package brain

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// sopsConfigRelPath is the repo-root SOPS rules file. The `sops` CLI reads
// it to know which recipients + encrypted_regex apply to a given path, so a
// human can also run `sops global/secrets/scopes.enc.yaml` against the same
// policy the in-process encoder uses.
const sopsConfigRelPath = ".sops.yaml"

// sopsCreationRule mirrors the subset of SOPS's creation_rules we emit. age
// recipients are Max's machines only (Appendix B #5); encrypted_regex is
// value-only so scope names/type/redaction_hints stay plaintext.
type sopsCreationRule struct {
	PathRegex      string `yaml:"path_regex"`
	EncryptedRegex string `yaml:"encrypted_regex"`
	Age            string `yaml:"age"`
}

type sopsConfigFile struct {
	CreationRules []sopsCreationRule `yaml:"creation_rules"`
}

// WriteSopsConfig writes <brainDir>/.sops.yaml with a single creation rule
// scoping the encrypted scope store to the given age recipients and the
// value-only encrypted_regex. Idempotent — rewrites the file each call so
// recipient changes (rotation) land deterministically.
func WriteSopsConfig(brainDir string, recipients []string) error {
	if len(recipients) == 0 {
		return fmt.Errorf("brain secrets: no age recipients for .sops.yaml")
	}

	cfg := sopsConfigFile{
		CreationRules: []sopsCreationRule{
			{
				PathRegex:      `secrets/.*\.enc\.yaml$`,
				EncryptedRegex: encryptedRegex,
				Age:            joinRecipients(recipients),
			},
		},
	}

	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("brain secrets: marshal .sops.yaml: %w", err)
	}

	if err := os.MkdirAll(brainDir, 0o755); err != nil {
		return fmt.Errorf("brain secrets: mkdir brain dir: %w", err)
	}
	outPath := filepath.Join(brainDir, sopsConfigRelPath)
	if err := os.WriteFile(outPath, data, 0o644); err != nil {
		return fmt.Errorf("brain secrets: write .sops.yaml: %w", err)
	}
	return nil
}

// joinRecipients renders the comma-separated age recipient list SOPS
// expects in the `age:` field.
func joinRecipients(recipients []string) string {
	out := ""
	for i, r := range recipients {
		if i > 0 {
			out += ","
		}
		out += r
	}
	return out
}
