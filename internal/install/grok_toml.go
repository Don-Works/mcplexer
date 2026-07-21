package install

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func hasGrokMCPConfig(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	src := string(data)
	return hasTOMLSection(src, grokMCPHeader(serverName)) ||
		hasTOMLSection(src, grokMCPHeader(legacyServerName))
}

func mergeGrokMCPConfig(path string, exePath string, socketPath string) error {
	merged, err := previewGrokMCPConfig(path, exePath, socketPath)
	if err != nil {
		return err
	}
	return writeTextConfig(path, merged)
}

func removeGrokMCPConfig(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	merged := removeTOMLSections(string(data), grokMCPHeader(serverName), grokMCPHeader(legacyServerName))
	return writeTextConfig(path, strings.TrimSpace(merged)+"\n")
}

func previewGrokMCPConfig(path string, exePath string, socketPath string) (string, error) {
	var src string
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return "", err
		}
	} else {
		src = string(data)
	}
	withoutOld := strings.TrimSpace(removeTOMLSections(src,
		grokMCPHeader(serverName),
		grokMCPHeader(legacyServerName),
	))
	if withoutOld == "" {
		return grokMCPBlock(exePath, socketPath), nil
	}
	return withoutOld + "\n\n" + grokMCPBlock(exePath, socketPath), nil
}

func grokMCPBlock(exePath string, socketPath string) string {
	return fmt.Sprintf(`%s
command = %s
args = ["connect", %s]
enabled = true
`, grokMCPHeader(serverName), quoteTOMLString(exePath), quoteTOMLString("--socket="+socketPath))
}

func grokMCPHeader(name string) string {
	return "[mcp_servers." + name + "]"
}

func hasTOMLSection(src string, header string) bool {
	for _, line := range strings.Split(src, "\n") {
		if strings.TrimSpace(line) == header {
			return true
		}
	}
	return false
}

func removeTOMLSections(src string, headers ...string) string {
	if strings.TrimSpace(src) == "" {
		return ""
	}
	remove := make(map[string]bool, len(headers))
	for _, header := range headers {
		remove[header] = true
	}
	var out []string
	skipping := false
	for _, line := range strings.Split(src, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			skipping = shouldRemoveTOMLSection(trimmed, remove)
		}
		if !skipping {
			out = append(out, line)
		}
	}
	return strings.TrimRight(strings.Join(out, "\n"), "\n")
}

func shouldRemoveTOMLSection(section string, roots map[string]bool) bool {
	if roots[section] {
		return true
	}
	for root := range roots {
		prefix := strings.TrimSuffix(root, "]") + "."
		if strings.HasPrefix(section, prefix) {
			return true
		}
	}
	return false
}

func quoteTOMLString(s string) string {
	return strconv.Quote(s)
}

func writeTextConfig(path string, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	if content == "" || !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	return AtomicWriteFile(path, []byte(content), 0o644)
}
