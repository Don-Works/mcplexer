package install

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestManagerGrokCLIInstallPreviewAndUninstall(t *testing.T) {
	home := t.TempDir()
	configDir := filepath.Join(home, ".grok")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatal(err)
	}
	m := &Manager{home: home, exePath: "/tmp/mcplexer", socketPath: "/run/user/1000/mcplexer.sock"}

	status, err := m.Status()
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	grok := clientByID(t, status.Clients, GrokCLI)
	if !grok.Detected {
		t.Fatalf("grok detected = false")
	}
	if grok.Configured {
		t.Fatalf("grok configured before install = true")
	}

	preview, err := m.Preview(GrokCLI)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	assertContains(t, preview.Content, "[mcp_servers.mcplexer]")
	assertContains(t, preview.Content, `command = "/tmp/mcplexer"`)
	assertContains(t, preview.Content, `args = ["connect", "--socket=/run/user/1000/mcplexer.sock"]`)

	installed, err := m.Install(GrokCLI)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if !installed.Configured {
		t.Fatalf("configured after install = false")
	}
	body, err := os.ReadFile(grokPath(home))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	assertContains(t, string(body), "[mcp_servers.mcplexer]")

	removed, err := m.Uninstall(GrokCLI)
	if err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if removed.Configured {
		t.Fatalf("configured after uninstall = true")
	}
	body, err = os.ReadFile(grokPath(home))
	if err != nil {
		t.Fatalf("read config after uninstall: %v", err)
	}
	if strings.Contains(string(body), "[mcp_servers.mcplexer]") {
		t.Fatalf("mcplexer section still present after uninstall:\n%s", body)
	}
}

func TestMergeGrokMCPConfigPreservesOtherTOMLSections(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	existing := `
[ui]
theme = "dark"

[mcp_servers.other]
command = "other"

[mcp_servers.mx]
command = "old"

[mcp_servers.mx.env]
OLD = "1"
`
	if err := os.WriteFile(path, []byte(existing), 0644); err != nil {
		t.Fatal(err)
	}

	if err := mergeGrokMCPConfig(path, "/opt/mcplexer", "/run/user/1000/mcplexer.sock"); err != nil {
		t.Fatalf("merge: %v", err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	got := string(body)
	assertContains(t, got, "[ui]")
	assertContains(t, got, `theme = "dark"`)
	assertContains(t, got, "[mcp_servers.other]")
	assertContains(t, got, "[mcp_servers.mcplexer]")
	assertContains(t, got, `command = "/opt/mcplexer"`)
	assertContains(t, got, `args = ["connect", "--socket=/run/user/1000/mcplexer.sock"]`)
	if strings.Contains(got, "[mcp_servers.mx]") {
		t.Fatalf("legacy mx section still present:\n%s", got)
	}
	if strings.Contains(got, "[mcp_servers.mx.env]") {
		t.Fatalf("legacy mx env section still present:\n%s", got)
	}
}

func TestManagerServerEntryUsesResolvedSocketPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MCPLEXER_SOCKET_PATH", "/custom/mcplexer.sock")
	m := &Manager{home: home, exePath: "/tmp/mcplexer"}

	status, err := m.Status()
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	args, ok := status.ServerEntry["args"].([]string)
	if !ok {
		t.Fatalf("server entry args = %#v", status.ServerEntry["args"])
	}
	if got, want := args[1], "--socket=/custom/mcplexer.sock"; got != want {
		t.Fatalf("socket arg = %q, want %q", got, want)
	}
}

func clientByID(t *testing.T, clients []ClientInfo, id ClientID) ClientInfo {
	t.Helper()
	for _, c := range clients {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("client %q not found", id)
	return ClientInfo{}
}

func assertContains(t *testing.T, got, want string) {
	t.Helper()
	if !strings.Contains(got, want) {
		t.Fatalf("content missing %q:\n%s", want, got)
	}
}
