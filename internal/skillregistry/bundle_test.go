package skillregistry_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/don-works/mcplexer/internal/skillregistry"
)

// buildBundle assembles a tar.gz with the given entries. When topDir
// is non-empty, every entry is nested under that directory — mirrors
// `tar -czf x.tgz skill-name/` vs `tar -czf x.tgz ./`.
func buildBundle(t *testing.T, topDir string, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, content := range files {
		full := name
		if topDir != "" {
			full = topDir + "/" + name
		}
		hdr := &tar.Header{
			Name:     full,
			Mode:     0o644,
			Size:     int64(len(content)),
			Typeflag: tar.TypeReg,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("tar header: %v", err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatalf("tar write: %v", err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gz close: %v", err)
	}
	return buf.Bytes()
}

func TestValidateBundleHappyPath(t *testing.T) {
	body := sampleBody("widget-printer", "Use when printing widgets.")
	bundle := buildBundle(t, "widget-printer", map[string]string{
		"SKILL.md":            body,
		"scripts/print.mjs":   "#!/usr/bin/env node\nconsole.log('hi');\n",
		"reference/format.md": "# Format\nfoo\n",
	})
	sha, err := skillregistry.ValidateBundle(bundle, body)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if len(sha) != 64 {
		t.Fatalf("sha256 should be 64 hex chars, got %d (%q)", len(sha), sha)
	}
}

func TestValidateBundleRejects(t *testing.T) {
	body := sampleBody("widget-printer", "Use when printing widgets.")
	cases := []struct {
		name    string
		bundle  []byte
		wantSub string
	}{
		{"empty", nil, "bundle is empty"},
		{"no SKILL.md", buildBundle(t, "x", map[string]string{"scripts/foo.mjs": "x"}), "no SKILL.md"},
		{"SKILL.md mismatch", buildBundle(t, "x", map[string]string{"SKILL.md": sampleBody("x", "different desc")}), "does not match"},
		{"not gzip", []byte("not a tar.gz"), "gzip"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := skillregistry.ValidateBundle(tc.bundle, body)
			if err == nil {
				t.Fatalf("expected error for %s, got nil", tc.name)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("error %q does not contain %q", err.Error(), tc.wantSub)
			}
		})
	}
}

func TestValidateBundleAcceptsFlatRoot(t *testing.T) {
	body := sampleBody("flat", "Use when the bundle has no leading dir.")
	bundle := buildBundle(t, "", map[string]string{
		"SKILL.md":       body,
		"scripts/run.sh": "echo hi\n",
	})
	if _, err := skillregistry.ValidateBundle(bundle, body); err != nil {
		t.Fatalf("validate flat root: %v", err)
	}
}

func TestPublishWithBundleStoresAndFetches(t *testing.T) {
	reg, _ := newTestRegistry(t)
	ctx := context.Background()

	body := sampleBody("bundled", "Use when verifying bundle round-trip.")
	bundle := buildBundle(t, "bundled", map[string]string{
		"SKILL.md":          body,
		"scripts/hello.mjs": "console.log('hello');\n",
	})

	res, err := reg.Publish(ctx, skillregistry.PublishOptions{
		Name:   "bundled",
		Body:   body,
		Bundle: bundle,
	})
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if res.Action != "created" || res.Version != 1 {
		t.Fatalf("unexpected publish result: %+v", res)
	}
	if res.BundleSize != len(bundle) || len(res.BundleSHA256) != 64 {
		t.Fatalf("bundle metadata missing: %+v", res)
	}

	got, sha, err := reg.FetchBundle(ctx, skillregistry.AdminScope(), "bundled", skillregistry.VersionRef{Latest: true})
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if !bytes.Equal(got, bundle) {
		t.Fatalf("bundle bytes mismatch: got %d bytes, want %d", len(got), len(bundle))
	}
	if sha != res.BundleSHA256 {
		t.Fatalf("sha mismatch: got %s, want %s", sha, res.BundleSHA256)
	}
}

func TestFetchBundleErrors(t *testing.T) {
	reg, _ := newTestRegistry(t)
	ctx := context.Background()

	body := sampleBody("no-bundle", "Use when the entry is text-only.")
	if _, err := reg.Publish(ctx, skillregistry.PublishOptions{Name: "no-bundle", Body: body}); err != nil {
		t.Fatalf("publish: %v", err)
	}
	_, _, err := reg.FetchBundle(ctx, skillregistry.AdminScope(), "no-bundle", skillregistry.VersionRef{Latest: true})
	if !errors.Is(err, skillregistry.ErrBundleNotPresent) {
		t.Fatalf("expected ErrBundleNotPresent, got %v", err)
	}

	_, _, err = reg.FetchBundle(ctx, skillregistry.AdminScope(), "missing", skillregistry.VersionRef{Latest: true})
	if err == nil {
		t.Fatalf("expected error for missing entry, got nil")
	}
}

func TestPublishBundleDedupAndChange(t *testing.T) {
	reg, _ := newTestRegistry(t)
	ctx := context.Background()

	body := sampleBody("dedup", "Use when checking dedup.")
	bundle := buildBundle(t, "dedup", map[string]string{"SKILL.md": body})

	r1, err := reg.Publish(ctx, skillregistry.PublishOptions{Name: "dedup", Body: body, Bundle: bundle})
	if err != nil {
		t.Fatalf("publish v1: %v", err)
	}
	r2, err := reg.Publish(ctx, skillregistry.PublishOptions{Name: "dedup", Body: body, Bundle: bundle})
	if err != nil {
		t.Fatalf("publish dedup: %v", err)
	}
	if r2.Version != r1.Version || r2.Action != "deduped" {
		t.Fatalf("expected dedup, got %+v", r2)
	}

	body2 := sampleBody("dedup", "Use when checking dedup (revised).")
	bundle2 := buildBundle(t, "dedup", map[string]string{"SKILL.md": body2})
	r3, err := reg.Publish(ctx, skillregistry.PublishOptions{Name: "dedup", Body: body2, Bundle: bundle2})
	if err != nil {
		t.Fatalf("publish v2: %v", err)
	}
	if r3.Version != r1.Version+1 || r3.Action != "created" {
		t.Fatalf("expected v%d created, got %+v", r1.Version+1, r3)
	}
}
