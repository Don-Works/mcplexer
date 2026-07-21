package sandbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrepareCommandConfigAddsOnlyExplicitInvocationPaths(t *testing.T) {
	root := t.TempDir()
	packageRoot := filepath.Join(root, "node_modules", "@example", "tool")
	bin := filepath.Join(packageRoot, "bin", "tool.js")
	if err := os.MkdirAll(filepath.Dir(bin), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, []byte("#!/usr/bin/env -S sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(root, "workspace")
	scratch := filepath.Join(root, "scratch")
	extra := filepath.Join(root, "prompt")
	cfg := PrepareCommandConfig(Config{Network: NetworkHost}, bin, workspace, scratch, extra)
	canonicalPackageRoot, err := filepath.EvalSymlinks(packageRoot)
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{bin, canonicalPackageRoot, extra} {
		if !contains_ss(cfg.ReadOnlyPaths, want) {
			t.Errorf("ReadOnlyPaths missing %q: %v", want, cfg.ReadOnlyPaths)
		}
	}
	for _, want := range []string{workspace, scratch} {
		if !contains_ss(cfg.ReadWritePaths, want) {
			t.Errorf("ReadWritePaths missing %q: %v", want, cfg.ReadWritePaths)
		}
	}
	if cfg.WorkingDir != workspace {
		t.Fatalf("WorkingDir = %q, want %q", cfg.WorkingDir, workspace)
	}
	if contains_ss(cfg.ReadWritePaths, filepath.Dir(root)) {
		t.Fatalf("parent directory was granted implicitly: %v", cfg.ReadWritePaths)
	}
}

func TestExecutableRuntimeRoot(t *testing.T) {
	tests := map[string]string{
		"homebrew":    "/opt/homebrew/Cellar/node/24.1.0",
		"npm scoped":  "/opt/homebrew/lib/node_modules/@openai/codex",
		"npm plain":   "/usr/local/lib/node_modules/example-cli",
		"application": "/Applications/Example.app/Contents",
		"standalone":  "",
	}
	paths := map[string]string{
		"homebrew":    "/opt/homebrew/Cellar/node/24.1.0/bin/node",
		"npm scoped":  "/opt/homebrew/lib/node_modules/@openai/codex/bin/codex.js",
		"npm plain":   "/usr/local/lib/node_modules/example-cli/bin/run.js",
		"application": "/Applications/Example.app/Contents/MacOS/example",
		"standalone":  "/usr/bin/true",
	}
	for name, want := range tests {
		t.Run(name, func(t *testing.T) {
			if got := executableRuntimeRoot(paths[name]); got != want {
				t.Fatalf("root = %q, want %q", got, want)
			}
		})
	}
}

func TestAllowlistedCommandEnvironmentIsFailClosed(t *testing.T) {
	got := AllowlistedCommandEnvironment([]string{
		"PATH=/bin",
		"HOME=/home/example",
		"LANG=en_GB.UTF-8",
		"TERM=xterm-256color",
		"HTTP_PROXY=http://proxy.example:8080",
		"HTTPS_PROXY=http://proxy.example:8443",
		"ALL_PROXY=socks5://proxy.example:1080",
		"NO_PROXY=localhost,127.0.0.1",
		"http_proxy=http://lower-proxy.example:8080",
		"https_proxy=http://lower-proxy.example:8443",
		"all_proxy=socks5://lower-proxy.example:1080",
		"no_proxy=localhost,.internal.example",
		"SSL_CERT_FILE=/etc/provider-ca.pem",
		"SSL_CERT_DIR=/etc/provider-certs",
		"NODE_EXTRA_CA_CERTS=/etc/node-provider-ca.pem",
		"OPENAI_API_KEY=provider-key",
		"OPENAI_BASE_URL=https://provider.example/v1",
		"ANTHROPIC_API_KEY=other-provider-key",
		"MCPLEXER_AUTH_TOKEN=daemon-secret",
		"MCPLEXER_FUTURE_NON_SECRET=still-not-approved",
		"SLACK_BOT_TOKEN=slack-secret",
		"TELEGRAM_BOT_TOKEN=telegram-secret",
		"OPENWA_API_KEY=openwa-secret",
		"DATABASE_URL=postgres://operator-secret",
		"POSTGRES_PASSWORD=database-secret",
		"SENTRY_AUTH_TOKEN=sentry-secret",
		"FUTURE_VENDOR_SECRET=future-secret",
		"FUTURE_PROVIDER_API_KEY=future-provider-secret",
		"UNKNOWN_TOKEN=unknown-secret",
		"UNRELATED_SETTING=not-approved",
		"TMPDIR=/old",
		"TMP=/old2",
		"TEMP=/old3",
	}, "/scratch", "OPENAI_API_KEY", "OPENAI_BASE_URL")

	for _, want := range []string{
		"PATH=/bin",
		"HOME=/home/example",
		"LANG=en_GB.UTF-8",
		"TERM=xterm-256color",
		"HTTP_PROXY=http://proxy.example:8080",
		"HTTPS_PROXY=http://proxy.example:8443",
		"ALL_PROXY=socks5://proxy.example:1080",
		"NO_PROXY=localhost,127.0.0.1",
		"http_proxy=http://lower-proxy.example:8080",
		"https_proxy=http://lower-proxy.example:8443",
		"all_proxy=socks5://lower-proxy.example:1080",
		"no_proxy=localhost,.internal.example",
		"SSL_CERT_FILE=/etc/provider-ca.pem",
		"SSL_CERT_DIR=/etc/provider-certs",
		"NODE_EXTRA_CA_CERTS=/etc/node-provider-ca.pem",
		"OPENAI_API_KEY=provider-key",
		"OPENAI_BASE_URL=https://provider.example/v1",
		"TMPDIR=/scratch",
		"TMP=/scratch",
		"TEMP=/scratch",
	} {
		if !contains_ss(got, want) {
			t.Errorf("environment missing %q: %v", want, got)
		}
	}
	for _, deniedKey := range []string{
		"ANTHROPIC_API_KEY",
		"MCPLEXER_AUTH_TOKEN",
		"MCPLEXER_FUTURE_NON_SECRET",
		"SLACK_BOT_TOKEN",
		"TELEGRAM_BOT_TOKEN",
		"OPENWA_API_KEY",
		"DATABASE_URL",
		"POSTGRES_PASSWORD",
		"SENTRY_AUTH_TOKEN",
		"FUTURE_VENDOR_SECRET",
		"FUTURE_PROVIDER_API_KEY",
		"UNKNOWN_TOKEN",
		"UNRELATED_SETTING",
	} {
		for _, entry := range got {
			if strings.HasPrefix(entry, deniedKey+"=") {
				t.Errorf("environment leaked denied key %q in %q", deniedKey, entry)
			}
		}
	}
	for _, inheritedTemp := range []string{"TMPDIR=/old", "TMP=/old2", "TEMP=/old3"} {
		if contains_ss(got, inheritedTemp) {
			t.Errorf("environment retained inherited temp entry %q: %v", inheritedTemp, got)
		}
	}
}

func TestCommandEnvironmentReadPathsReturnsOnlyConfiguredExistingAbsoluteCAPaths(t *testing.T) {
	root := t.TempDir()
	certFile := filepath.Join(root, "provider-ca.pem")
	nodeCertFile := filepath.Join(root, "node-provider-ca.pem")
	certDirA := filepath.Join(root, "certs-a")
	certDirB := filepath.Join(root, "certs-b")
	for _, dir := range []string{certDirA, certDirB} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, file := range []string{certFile, nodeCertFile} {
		if err := os.WriteFile(file, []byte("test certificate"), 0600); err != nil {
			t.Fatal(err)
		}
	}

	got := CommandEnvironmentReadPaths([]string{
		"SSL_CERT_FILE=" + certFile,
		"SSL_CERT_DIR=" + strings.Join([]string{certDirA, certDirB}, string(os.PathListSeparator)),
		"NODE_EXTRA_CA_CERTS=" + nodeCertFile,
		"SSL_CERT_FILE=relative-ca.pem",
		"NODE_EXTRA_CA_CERTS=" + filepath.Join(root, "missing.pem"),
		"REQUESTS_CA_BUNDLE=" + filepath.Join(root, "unapproved.pem"),
	})
	for _, want := range []string{certFile, certDirA, certDirB, nodeCertFile} {
		if !contains_ss(got, want) {
			t.Errorf("read paths missing %q: %v", want, got)
		}
	}
	for _, path := range got {
		if !filepath.IsAbs(path) {
			t.Errorf("read paths contained relative path %q", path)
		}
		if _, err := os.Lstat(path); err != nil {
			t.Errorf("read paths contained missing path %q: %v", path, err)
		}
	}
	for _, denied := range []string{"relative-ca.pem", filepath.Join(root, "missing.pem"), filepath.Join(root, "unapproved.pem")} {
		if contains_ss(got, denied) {
			t.Errorf("read paths admitted unapproved path %q: %v", denied, got)
		}
	}
}

// TestWrapCommandDisabledIsIdentity — a nil or disabled wrapper must
// hand back program/args untouched, mirroring Wrap's contract.
func TestWrapCommandDisabledIsIdentity(t *testing.T) {
	var nilWrapper *CommandWrapper
	prog, args, cleanup := nilWrapper.WrapCommand("/bin/echo", []string{"hi"}, "", "")
	cleanup()
	if prog != "/bin/echo" || len(args) != 1 || args[0] != "hi" {
		t.Fatalf("nil wrapper must be identity, got %q %v", prog, args)
	}
	disabled := &CommandWrapper{}
	prog, args, cleanup = disabled.WrapCommand("/bin/echo", []string{"hi"}, "", "")
	cleanup()
	if prog != "/bin/echo" || len(args) != 1 || args[0] != "hi" {
		t.Fatalf("disabled wrapper must be identity, got %q %v", prog, args)
	}
}

func TestPackageManagerLibraryRoots(t *testing.T) {
	cases := []struct {
		name string
		path string
		want []string
	}{
		{"homebrew cellar node", "/opt/homebrew/Cellar/node/23.11.0/bin/node",
			[]string{"/opt/homebrew/opt", "/opt/homebrew/lib", "/opt/homebrew/Cellar", "/opt/homebrew/etc"}},
		{"homebrew opt symlink", "/opt/homebrew/opt/node/bin/node",
			[]string{"/opt/homebrew/opt", "/opt/homebrew/lib", "/opt/homebrew/Cellar", "/opt/homebrew/etc"}},
		{"intel homebrew", "/usr/local/Cellar/node/23/bin/node",
			[]string{"/usr/local/opt", "/usr/local/lib", "/usr/local/Cellar", "/usr/local/etc"}},
		{"linuxbrew", "/home/linuxbrew/.linuxbrew/Cellar/node/23/bin/node",
			[]string{"/home/linuxbrew/.linuxbrew/opt", "/home/linuxbrew/.linuxbrew/lib", "/home/linuxbrew/.linuxbrew/Cellar", "/home/linuxbrew/.linuxbrew/etc"}},
		{"system binary no roots", "/usr/bin/env", nil},
		{"arbitrary path", "/home/user/bin/tool", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := packageManagerLibraryRoots(tc.path)
			if len(got) != len(tc.want) {
				t.Fatalf("roots = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("roots = %v, want %v", got, tc.want)
				}
			}
		})
	}
}

// TestExecutableReadPathsIncludesHomebrewLibs — a Homebrew binary's read
// paths must include the brew library roots so dyld can load its
// dependencies (the live mimo->node->libuv "blocked by sandbox" abort).
func TestExecutableReadPathsIncludesHomebrewLibs(t *testing.T) {
	paths := ExecutableReadPaths("/opt/homebrew/Cellar/node/23.11.0/bin/node")
	var haveOpt, haveLib bool
	for _, p := range paths {
		switch p {
		case "/opt/homebrew/opt":
			haveOpt = true
		case "/opt/homebrew/lib":
			haveLib = true
		}
	}
	if !haveOpt || !haveLib {
		t.Fatalf("homebrew lib roots missing from %v", paths)
	}
}
