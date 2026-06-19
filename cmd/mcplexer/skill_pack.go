package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/don-works/mcplexer/internal/skills"
)

// cmdSkillPack implements `mcplexer skill pack <dir> [-o out.mcskill] [--key <keyfile>]`.
//
// It reads a skill source directory (which must contain manifest.toml and
// skill.md), gzips the tar to <out>, and — if --key is provided — also
// signs it via M2.4's Sign(), producing <out>.minisig.
func cmdSkillPack(args []string) error {
	fs := flag.NewFlagSet("skill pack", flag.ContinueOnError)
	out := fs.String("o", "", "output bundle path (default: <basename>.mcskill)")
	keyPath := fs.String("key", "",
		"private keyfile to sign with; omit to produce an unsigned bundle")
	comment := fs.String("comment", "",
		"trusted comment fragment passed to skills.Sign (e.g. \"skill=acme/x version=1.0.0\")")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return fmt.Errorf("usage: mcplexer skill pack <dir> [-o out.mcskill] [--key <keyfile>]")
	}
	srcDir := fs.Arg(0)
	outPath := derivePackOut(*out, srcDir)
	bundle, err := skills.PackDir(srcDir)
	if err != nil {
		return err
	}
	if err := writeBundle(outPath, bundle); err != nil {
		return err
	}
	fmt.Printf("Wrote bundle: %s (%d bytes)\n", outPath, len(bundle))
	if *keyPath == "" {
		fmt.Println("(unsigned — install will require --allow-unsigned)")
		return nil
	}
	return signBundleAndWrite(outPath, bundle, *keyPath, *comment)
}

// derivePackOut returns the output path: -o flag, or <dir-basename>.mcskill
// in the current working directory.
func derivePackOut(out, srcDir string) string {
	if out != "" {
		return out
	}
	base := filepath.Base(filepath.Clean(srcDir))
	if base == "." || base == "/" || base == "" {
		base = "skill"
	}
	return base + ".mcskill"
}

// writeBundle writes b to path with mode 0644.
func writeBundle(path string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil && filepath.Dir(path) != "." {
		return fmt.Errorf("mkdir parent: %w", err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil { //nolint:gosec
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// signBundleAndWrite asks for the signer passphrase, signs bundleBytes, and
// writes <outPath>.minisig.
func signBundleAndWrite(outPath string, bundleBytes []byte, keyPath, comment string) error {
	pass, err := promptPassphrase("Passphrase for " + keyPath + ": ")
	if err != nil {
		return err
	}
	priv, err := skills.LoadPrivateKey(keyPath, pass)
	if err != nil {
		return err
	}
	c := comment
	if strings.TrimSpace(c) == "" {
		c = ""
	}
	sig, err := skills.Sign(bundleBytes, priv, c)
	if err != nil {
		return err
	}
	sigPath := outPath + ".minisig"
	if err := os.WriteFile(sigPath, sig, 0o644); err != nil { //nolint:gosec
		return fmt.Errorf("write signature: %w", err)
	}
	fmt.Printf("Wrote signature: %s\n", sigPath)
	return nil
}
