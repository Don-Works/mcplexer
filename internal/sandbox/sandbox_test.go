package sandbox

import (
	"runtime"
	"strings"
	"testing"
)

func TestValidateConfig_ProxyWithoutSocketFailsClosed(t *testing.T) {
	err := ValidateConfig(Config{Network: NetworkProxy})
	if err == nil {
		t.Fatal("ValidateConfig(Network=proxy, no socket) must return error")
	}
	if err != ErrProxyNotConfigured {
		t.Fatalf("error = %v, want ErrProxyNotConfigured", err)
	}
}

func TestValidateConfig_ProxyWithSocketPasses(t *testing.T) {
	err := ValidateConfig(Config{Network: NetworkProxy, ProxySocket: "/tmp/mcplexer-proxy.sock"})
	if err != nil {
		t.Fatalf("ValidateConfig(proxy+socket) = %v, want nil", err)
	}
}

func TestValidateConfig_ZeroConfigIsValid(t *testing.T) {
	if err := ValidateConfig(Config{}); err != nil {
		t.Fatalf("ValidateConfig(zero) = %v, want nil", err)
	}
}

func TestValidateConfig_DenyIsValid(t *testing.T) {
	if err := ValidateConfig(Config{Network: NetworkDeny}); err != nil {
		t.Fatalf("ValidateConfig(deny) = %v, want nil", err)
	}
}

func TestValidateConfig_HostIsValid(t *testing.T) {
	if err := ValidateConfig(Config{Network: NetworkHost}); err != nil {
		t.Fatalf("ValidateConfig(host) = %v, want nil", err)
	}
}

func TestProxySocketFromEnv_ReadsEnvVar(t *testing.T) {
	t.Setenv(ProxySocketEnvVar, "/var/run/mcplexer-proxy.sock")
	if got := ProxySocketFromEnv(); got != "/var/run/mcplexer-proxy.sock" {
		t.Fatalf("ProxySocketFromEnv() = %q, want /var/run/mcplexer-proxy.sock", got)
	}
}

func TestProxySocketFromEnv_EmptyWhenUnset(t *testing.T) {
	t.Setenv(ProxySocketEnvVar, "")
	if got := ProxySocketFromEnv(); got != "" {
		t.Fatalf("ProxySocketFromEnv() = %q, want empty when env unset", got)
	}
}

func TestDefaultDenyPaths_IncludesCanonicals(t *testing.T) {
	got := DefaultDenyPaths("/Users/test")
	mustHave := []string{
		"/Users/test/.ssh",
		"/Users/test/.mcplexer",
		"/Users/test/.aws",
		"/Users/test/.docker/config.json",
		"/var/run/docker.sock",
	}
	for _, want := range mustHave {
		if !contains_ss(got, want) {
			t.Errorf("DefaultDenyPaths missing %q (got=%v)", want, got)
		}
	}
}

func TestDefaultDenyPaths_EmptyHomeStillCoversDockerSocket(t *testing.T) {
	got := DefaultDenyPaths("")
	if !contains_ss(got, "/var/run/docker.sock") {
		t.Fatalf("empty-home fallback should still deny docker socket, got %v", got)
	}
}

func TestMergeDenyPaths_DeduplicatesAndAppends(t *testing.T) {
	got := MergeDenyPaths("/Users/test", []string{
		"/Users/test/.ssh", // dup of default
		"/extra/secret",
		"",              // ignored
		"/extra/secret", // dup of self
	})
	if !contains_ss(got, "/extra/secret") {
		t.Fatal("custom deny path missing")
	}
	// Count "/Users/test/.ssh" should be exactly 1.
	count := 0
	for _, p := range got {
		if p == "/Users/test/.ssh" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected .ssh dedup'd to 1 entry, got %d", count)
	}
}

func TestSelectDriver_PicksSomethingOnSupportedOS(t *testing.T) {
	d := SelectDriver()
	switch runtime.GOOS {
	case "darwin":
		if d == nil {
			t.Fatal("SelectDriver returned nil on darwin")
		}
		if d.Name() != "sandbox-exec" {
			t.Fatalf("darwin should pick sandbox-exec, got %q", d.Name())
		}
	case "linux":
		// Either bwrap or unshare or nil (if neither tool is present).
		if d != nil && d.Name() != "bwrap" && d.Name() != "unshare" {
			t.Fatalf("linux selector returned unexpected %q", d.Name())
		}
	default:
		if d != nil {
			t.Fatalf("unsupported OS should yield nil, got %q", d.Name())
		}
	}
}

// contains_ss is a tiny slice-of-string lookup, named to avoid shadowing
// the `contains` helper from sandbox_exec_darwin_test.go (which works
// on strings, not slices).
func contains_ss(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
		if strings.HasPrefix(s, needle) && s == needle {
			return true
		}
	}
	return false
}
