package downstream

import (
	"slices"
	"testing"
)

// TestEnvWithTempDir — the sandbox scratch dir must fully replace every
// temp-dir variable so a child never falls back to the profile-denied
// host default under /var/folders.
func TestEnvWithTempDir(t *testing.T) {
	in := []string{
		"PATH=/usr/bin",
		"TMPDIR=/var/folders/xy/old",
		"TMP=/somewhere/else",
		"TEMP=/a/third/place",
		"HOME=/Users/example",
	}
	out := envWithTempDir(in, "/scratch/run-1")

	for _, e := range out {
		switch e {
		case "TMPDIR=/var/folders/xy/old", "TMP=/somewhere/else", "TEMP=/a/third/place":
			t.Fatalf("stale temp var survived: %s", e)
		}
	}
	for _, want := range []string{"PATH=/usr/bin", "HOME=/Users/example",
		"TMPDIR=/scratch/run-1", "TMP=/scratch/run-1", "TEMP=/scratch/run-1"} {
		if !slices.Contains(out, want) {
			t.Fatalf("missing %q in %v", want, out)
		}
	}
}
