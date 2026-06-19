package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/skills"
)

// httpDownloadTimeout caps the wall-clock for a network install. 5 minutes
// is generous; large bundles still finish well under this on a typical link.
const httpDownloadTimeout = 5 * time.Minute

// cmdSkillInstall implements `mcplexer skill install <file_or_url>`.
//
// Flags:
//
//	--yes               skip the y/N capability-review prompt (for scripted use)
//	--allow-unsigned    install bundles without a .minisig sibling
//	--force             overwrite an already-installed skill
func cmdSkillInstall(args []string) error {
	fs := flag.NewFlagSet("skill install", flag.ContinueOnError)
	yes := fs.Bool("yes", false, "skip the capability-review prompt")
	allowUnsigned := fs.Bool("allow-unsigned", false,
		"install bundles without a .minisig sibling (development only)")
	force := fs.Bool("force", false,
		"replace an already-installed skill instead of erroring")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return fmt.Errorf("usage: mcplexer skill install [--yes] [--allow-unsigned] [--force] <file-or-https-url>")
	}
	target := fs.Arg(0)
	bundlePath, source, cleanup, err := acquireBundle(target)
	if err != nil {
		return err
	}
	defer cleanup()
	return runInstall(bundlePath, source, *yes, *allowUnsigned, *force)
}

// acquireBundle returns a local file path to the bundle, the source string
// recorded in the registry, and a cleanup function (a no-op for files,
// removes the temp file for HTTPS URLs).
func acquireBundle(target string) (string, string, func(), error) {
	if strings.HasPrefix(target, "http://") {
		return "", "", noopCleanup, fmt.Errorf("refusing http:// install — use https://")
	}
	if strings.HasPrefix(target, "https://") {
		return downloadHTTPS(target)
	}
	abs, err := filepath.Abs(target)
	if err != nil {
		return "", "", noopCleanup, fmt.Errorf("abs path: %w", err)
	}
	return abs, "file:" + abs, noopCleanup, nil
}

func noopCleanup() {}

// downloadHTTPS fetches an https:// URL into a temp file capped at MaxBundleSize.
// The .minisig sibling is fetched too so the verify step finds it locally.
func downloadHTTPS(rawURL string) (string, string, func(), error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "https" {
		return "", "", noopCleanup, fmt.Errorf("invalid https url: %s", rawURL)
	}
	tmpDir, err := os.MkdirTemp("", "mcplexer-skill-*")
	if err != nil {
		return "", "", noopCleanup, fmt.Errorf("tempdir: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(tmpDir) }
	bundlePath := filepath.Join(tmpDir, filepath.Base(u.Path))
	if err := fetchTo(rawURL, bundlePath); err != nil {
		cleanup()
		return "", "", noopCleanup, err
	}
	// Sibling .minisig is best-effort: install will reject if it's required
	// and missing.
	_ = fetchTo(rawURL+".minisig", bundlePath+".minisig")
	return bundlePath, rawURL, cleanup, nil
}

// fetchTo GETs url and writes the body (capped at MaxBundleSize) to dst.
func fetchTo(rawURL, dst string) error {
	client := &http.Client{Timeout: httpDownloadTimeout}
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("GET %s: %w", rawURL, err)
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: status %d", rawURL, resp.StatusCode)
	}
	f, err := os.Create(dst) //nolint:gosec
	if err != nil {
		return fmt.Errorf("create %s: %w", dst, err)
	}
	defer f.Close() //nolint:errcheck
	n, err := io.Copy(f, io.LimitReader(resp.Body, skills.MaxBundleSize+1))
	if err != nil {
		return fmt.Errorf("download body: %w", err)
	}
	if n > skills.MaxBundleSize {
		return fmt.Errorf("download exceeds %d bytes", skills.MaxBundleSize)
	}
	return nil
}

// runInstall prepares the review, prompts for y/N (unless --yes), and calls
// skills.Install.
func runInstall(
	bundlePath, source string, yes, allowUnsigned, force bool,
) error {
	ctx := context.Background()
	db, err := openStore(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	skillsDir := defaultDataPath("skills")
	opts := skills.InstallOptions{
		SkillsDir: skillsDir, Source: source,
		AllowUnsigned: allowUnsigned, Force: force,
	}
	row, review, err := skills.Install(ctx, db, bundlePath, opts)
	if err != nil {
		printReviewIfPresent(review)
		return err
	}
	if !yes {
		printReview(review)
		ok, err := confirm("Install this skill? [y/N]: ")
		if err != nil {
			_ = skills.Remove(ctx, db, skillsDir, row.Name)
			return err
		}
		if !ok {
			_ = skills.Remove(ctx, db, skillsDir, row.Name)
			return errors.New("install cancelled by user")
		}
	}
	fmt.Printf("Installed: %s %s\n", row.Name, row.Version)
	return nil
}

// confirm reads y/N from stdin; default (empty input) is No.
func confirm(prompt string) (bool, error) {
	fmt.Fprint(os.Stderr, prompt)
	r := bufio.NewReader(os.Stdin)
	line, err := r.ReadString('\n')
	if err != nil && line == "" {
		return false, fmt.Errorf("read stdin: %w", err)
	}
	s := strings.ToLower(strings.TrimSpace(line))
	return s == "y" || s == "yes", nil
}
