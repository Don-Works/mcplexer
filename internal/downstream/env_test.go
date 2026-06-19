package downstream

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestMergeEnvPriority(t *testing.T) {
	os := []string{"A=1", "B=2"}
	srv := map[string]string{"B": "3", "C": "4"}
	auth := map[string]string{"C": "5", "D": "6"}

	env := MergeEnv(os, srv, auth)
	m := envToMap(env)

	if m["A"] != "1" {
		t.Errorf("A = %q, want 1", m["A"])
	}
	if m["B"] != "3" {
		t.Errorf("B = %q, want 3 (server overrides os)", m["B"])
	}
	if m["C"] != "5" {
		t.Errorf("C = %q, want 5 (auth overrides server)", m["C"])
	}
	if m["D"] != "6" {
		t.Errorf("D = %q, want 6", m["D"])
	}
}

func TestMergeEnvAugmentsPath(t *testing.T) {
	env := MergeEnv([]string{"PATH=/usr/bin:/bin"}, nil, nil)
	m := envToMap(env)

	path := m["PATH"]
	if !strings.HasPrefix(path, "/usr/bin:/bin") {
		t.Errorf("PATH should start with original dirs, got %q", path)
	}

	// /usr/local/bin should be appended.
	if !strings.Contains(path, "/usr/local/bin") {
		t.Errorf("PATH should contain /usr/local/bin, got %q", path)
	}
}

func TestMergeEnvDoesNotDuplicatePath(t *testing.T) {
	env := MergeEnv([]string{"PATH=/usr/local/bin:/usr/bin"}, nil, nil)
	m := envToMap(env)

	count := 0
	for _, p := range filepath.SplitList(m["PATH"]) {
		if p == "/usr/local/bin" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("/usr/local/bin appears %d times, want 1", count)
	}
}

func TestAugmentPathEmpty(t *testing.T) {
	result := augmentPath("")
	if result == "" {
		t.Error("augmentPath(\"\") should return common paths")
	}
	if strings.HasPrefix(result, string(filepath.ListSeparator)) {
		t.Error("should not start with separator")
	}
}

func TestExpandVars(t *testing.T) {
	env := map[string]string{"HOST": "localhost", "PORT": "5432"}
	got := expandVars("postgresql://${HOST}:${PORT}/db", env)
	want := "postgresql://localhost:5432/db"
	if got != want {
		t.Errorf("expandVars = %q, want %q", got, want)
	}
}

func TestMergeEnvStripsDaemonSecrets(t *testing.T) {
	osEnv := []string{
		"PATH=/usr/bin",
		"HOME=/Users/test",
		"MCPLEXER_AGE_KEY=/private/path/key.age",
		"MCPLEXER_DB_DSN=/private/path/db",
		"MCPLEXER_API_TOKEN_PATH=/private/path/api-key",
		"AGE_IDENTITY=/secret",
		"AGE_PASSPHRASE=hunter2",
	}
	env := MergeEnv(osEnv, nil, nil)
	m := envToMap(env)

	if m["HOME"] != "/Users/test" {
		t.Errorf("HOME = %q, want pass-through", m["HOME"])
	}

	for _, k := range []string{
		"MCPLEXER_AGE_KEY",
		"MCPLEXER_DB_DSN",
		"MCPLEXER_API_TOKEN_PATH",
		"AGE_IDENTITY",
		"AGE_PASSPHRASE",
	} {
		if _, ok := m[k]; ok {
			t.Errorf("sensitive env %q must not pass through to downstream subprocess", k)
		}
	}
}

func TestIsSensitiveDaemonEnvKey(t *testing.T) {
	cases := []struct {
		key       string
		sensitive bool
	}{
		{"PATH", false},
		{"HOME", false},
		{"USER", false},
		{"MCPLEXER_AGE_KEY", true},
		{"MCPLEXER_DB_DSN", true},
		{"MCPLEXER_FUTURE_VAR", true},
		{"AGE_IDENTITY", true},
		{"AGE_KEY", true},
		{"AGE_PASSPHRASE", true},
		{"NON_MCPLEXER_AGE_THING", false},
	}
	for _, c := range cases {
		got := isSensitiveDaemonEnvKey(c.key)
		if got != c.sensitive {
			t.Errorf("isSensitiveDaemonEnvKey(%q) = %v, want %v", c.key, got, c.sensitive)
		}
	}
}

func envToMap(env []string) map[string]string {
	m := make(map[string]string, len(env))
	for _, e := range env {
		k, v, ok := strings.Cut(e, "=")
		if ok {
			m[k] = v
		}
	}
	return m
}
