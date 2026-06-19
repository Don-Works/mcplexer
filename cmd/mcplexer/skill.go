package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"aead.dev/minisign"

	"github.com/don-works/mcplexer/internal/skills"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

// cmdSkill dispatches `mcplexer skill <subcommand>`.
func cmdSkill(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: mcplexer skill <key|sign|verify|pack|install|list|remove|show> [args...]")
	}
	switch args[0] {
	case "key":
		return cmdSkillKey(args[1:])
	case "sign":
		return cmdSkillSign(args[1:])
	case "verify":
		return cmdSkillVerify(args[1:])
	case "pack":
		return cmdSkillPack(args[1:])
	case "install":
		return cmdSkillInstall(args[1:])
	case "list":
		return cmdSkillList()
	case "remove":
		return cmdSkillRemove(args[1:])
	case "show":
		return cmdSkillShow(args[1:])
	default:
		return fmt.Errorf("unknown skill subcommand: %s\nUsage: mcplexer skill <key|sign|verify|pack|install|list|remove|show>", args[0])
	}
}

// cmdSkillKey dispatches `mcplexer skill key <generate|list>`.
func cmdSkillKey(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: mcplexer skill key <generate|list> [args...]")
	}
	switch args[0] {
	case "generate":
		return cmdSkillKeyGenerate(args[1:])
	case "list":
		return cmdSkillKeyList()
	default:
		return fmt.Errorf("unknown key subcommand: %s\nUsage: mcplexer skill key <generate|list>", args[0])
	}
}

// cmdSkillKeyGenerate creates a fresh keypair and writes the encrypted private
// key to disk. The public key (canonical form + key id) is printed so the user
// can paste it into a trust store / README.
func cmdSkillKeyGenerate(args []string) error {
	fs := flag.NewFlagSet("skill key generate", flag.ContinueOnError)
	out := fs.String("out", defaultDataPath("skills/signer.key"), "output keyfile path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if _, err := os.Stat(*out); err == nil {
		return fmt.Errorf("refusing to overwrite existing keyfile: %s", *out)
	}
	pass, err := promptPassphraseConfirm()
	if err != nil {
		return err
	}
	pub, priv, err := skills.GenerateKeypair(pass)
	if err != nil {
		return fmt.Errorf("generate keypair: %w", err)
	}
	if err := skills.SavePrivateKey(*out, pass, priv); err != nil {
		return fmt.Errorf("save private key: %w", err)
	}
	fmt.Printf("Private key written: %s (mode 0600)\n", *out)
	fmt.Printf("Public key: %s\n", skills.FormatPublicKeyWithPrefix(pub))
	fmt.Printf("Key ID:     %s\n", skills.PublicKeyID(pub))
	fmt.Println("\nAdd it to a recipient's trust store with:")
	fmt.Printf("  mcplexer skill verify --add-signer %q --name <label>\n",
		skills.FormatPublicKeyWithPrefix(pub))
	return nil
}

// cmdSkillKeyList prints the local trusted_signers table.
func cmdSkillKeyList() error {
	ctx := context.Background()
	db, err := openStore(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	rows, err := db.ListTrustedSigners(ctx)
	if err != nil {
		return fmt.Errorf("list trusted signers: %w", err)
	}
	if len(rows) == 0 {
		fmt.Println("No trusted signers.")
		fmt.Println("Add one with: mcplexer skill verify --add-signer <pubkey> --name <label>")
		return nil
	}
	fmt.Printf("%-16s  %-20s  %s\n", "KEY ID", "NAME", "STATUS")
	for _, s := range rows {
		status := "trusted"
		if s.RevokedAt != nil {
			status = "REVOKED " + s.RevokedAt.Format("2006-01-02")
		}
		fmt.Printf("%-16s  %-20s  %s\n", s.PubkeyID, truncate(s.Name, 20), status)
	}
	return nil
}

// cmdSkillSign signs the bundle at args[0] using --key, writing
// <bundle>.minisig next to it.
func cmdSkillSign(args []string) error {
	fs := flag.NewFlagSet("skill sign", flag.ContinueOnError)
	keyPath := fs.String("key", "", "private keyfile (required)")
	comment := fs.String("comment", "",
		"trusted comment fragment, e.g. \"skill=acme/hello version=0.1.0\"")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return fmt.Errorf("usage: mcplexer skill sign <bundle> --key <keyfile> [--comment <text>]")
	}
	if *keyPath == "" {
		return fmt.Errorf("--key is required")
	}
	bundlePath := fs.Arg(0)
	bundleBytes, err := os.ReadFile(bundlePath)
	if err != nil {
		return fmt.Errorf("read bundle: %w", err)
	}
	pass, err := promptPassphrase("Passphrase for " + *keyPath + ": ")
	if err != nil {
		return err
	}
	priv, err := skills.LoadPrivateKey(*keyPath, pass)
	if err != nil {
		return err
	}
	sig, err := skills.Sign(bundleBytes, priv, *comment)
	if err != nil {
		return err
	}
	sigPath := bundlePath + ".minisig"
	if err := os.WriteFile(sigPath, sig, 0o644); err != nil {
		return fmt.Errorf("write signature: %w", err)
	}
	fmt.Printf("Signature written: %s\n", sigPath)
	return nil
}

// cmdSkillVerify either verifies a bundle against the local trust store, or
// (with --add-signer) inserts a new signer row.
func cmdSkillVerify(args []string) error {
	fs := flag.NewFlagSet("skill verify", flag.ContinueOnError)
	addSigner := fs.String("add-signer", "", "add a public key to the trust store and exit")
	name := fs.String("name", "", "label for the signer when using --add-signer")
	sigPath := fs.String("sig", "", "signature path (default: <bundle>.minisig)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *addSigner != "" {
		return cmdSkillVerifyAddSigner(*addSigner, *name)
	}
	if fs.NArg() < 1 {
		return fmt.Errorf("usage: mcplexer skill verify <bundle> [--sig <path>]")
	}
	bundlePath := fs.Arg(0)
	if *sigPath == "" {
		*sigPath = bundlePath + ".minisig"
	}
	return runVerify(bundlePath, *sigPath)
}

// cmdSkillVerifyAddSigner is the implementation of `--add-signer`.
func cmdSkillVerifyAddSigner(pubkeyStr, name string) error {
	pk, err := skills.ParsePublicKey(pubkeyStr)
	if err != nil {
		return err
	}
	ctx := context.Background()
	db, err := openStore(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	row := &store.TrustedSigner{
		PubkeyID:     skills.PublicKeyID(pk),
		PubkeyString: skills.FormatPublicKey(pk),
		Name:         name,
	}
	if err := db.AddTrustedSigner(ctx, row); err != nil {
		if errors.Is(err, store.ErrAlreadyExists) {
			return fmt.Errorf("signer %s already in trust store", row.PubkeyID)
		}
		return fmt.Errorf("add trusted signer: %w", err)
	}
	fmt.Printf("Trusted signer added: %s (%s)\n", row.PubkeyID, name)
	return nil
}

// runVerify reads the bundle + signature, looks up the signer in the trust
// store, and verifies.
func runVerify(bundlePath, sigPath string) error {
	bundleBytes, err := os.ReadFile(bundlePath)
	if err != nil {
		return fmt.Errorf("read bundle: %w", err)
	}
	sigBytes, err := os.ReadFile(sigPath)
	if err != nil {
		return fmt.Errorf("read signature: %w", err)
	}
	pk, signerRow, err := lookupSigner(sigBytes)
	if err != nil {
		return err
	}
	if err := skills.Verify(bundleBytes, sigBytes, pk); err != nil {
		return err
	}
	fmt.Printf("OK %s — signed by %s (%s)\n",
		filepath.Base(bundlePath), signerRow.PubkeyID, displayName(signerRow.Name))
	return nil
}

// lookupSigner extracts the key id from a signature and looks it up in the
// local trust store. Returns ErrUntrustedSigner when not found or revoked.
func lookupSigner(sigBytes []byte) (*minisign.PublicKey, *store.TrustedSigner, error) {
	var sig minisign.Signature
	if err := sig.UnmarshalText(sigBytes); err != nil {
		return nil, nil, fmt.Errorf("parse signature: %w", err)
	}
	id := fmt.Sprintf("%016X", sig.KeyID)

	ctx := context.Background()
	db, err := openStore(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = db.Close() }()
	rows, err := db.ListTrustedSigners(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("list trusted signers: %w", err)
	}
	for i := range rows {
		if rows[i].PubkeyID != id {
			continue
		}
		if rows[i].RevokedAt != nil {
			return nil, nil, fmt.Errorf("%w: %s revoked at %s",
				skills.ErrUntrustedSigner, id, rows[i].RevokedAt.Format("2006-01-02"))
		}
		pk, err := skills.ParsePublicKey(rows[i].PubkeyString)
		if err != nil {
			return nil, nil, fmt.Errorf("trust store row corrupt: %w", err)
		}
		return pk, &rows[i], nil
	}
	return nil, nil, fmt.Errorf("%w: %s not in trust store (run `mcplexer skill key list`)",
		skills.ErrUntrustedSigner, id)
}

// openStore opens the SQLite database from the standard config.
func openStore(ctx context.Context) (*sqlite.DB, error) {
	cfg, err := loadConfig()
	if err != nil {
		return nil, err
	}
	db, err := sqlite.New(ctx, cfg.DBDSN)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	return db, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "…"
}

func displayName(s string) string {
	if s == "" {
		return "unlabelled"
	}
	return s
}
