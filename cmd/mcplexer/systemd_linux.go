//go:build linux

package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"text/template"
	"time"
)

const systemdUserUnitName = "mcplexer.service"

func systemdUserServicePath() string {
	configHome := os.Getenv("XDG_CONFIG_HOME")
	if configHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		configHome = filepath.Join(home, ".config")
	}
	return filepath.Join(configHome, "systemd", "user", systemdUserUnitName)
}

func systemdUserAvailable() bool {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, "systemctl", "--user", "show-environment").Run() == nil
}

func systemdUserInstalled() bool {
	p := systemdUserServicePath()
	if p == "" {
		return false
	}
	_, err := os.Stat(p)
	return err == nil
}

func installSystemdUser(exePath, addr, socketPath string) error {
	if !systemdUserAvailable() {
		return fmt.Errorf("systemd user manager is not available; run `mcplexer daemon start` for the built-in background daemon")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("get home dir: %w", err)
	}
	if err := installBinary(exePath); err != nil {
		return fmt.Errorf("install binary: %w", err)
	}
	stablePath, err := stableBinPath()
	if err != nil {
		return fmt.Errorf("resolve stable path: %w", err)
	}

	logPath := filepath.Join(home, ".mcplexer", "mcplexer.log")
	if err := ensureLogFile(logPath); err != nil {
		return fmt.Errorf("pre-create log file at 0600: %w", err)
	}

	unitPath := systemdUserServicePath()
	if unitPath == "" {
		return fmt.Errorf("resolve systemd user unit path")
	}
	if err := os.MkdirAll(filepath.Dir(unitPath), 0755); err != nil {
		return fmt.Errorf("create systemd user dir: %w", err)
	}

	unit, err := renderSystemdUserService(stablePath, addr, socketPath, logPath)
	if err != nil {
		return err
	}
	if err := os.WriteFile(unitPath, []byte(unit), 0644); err != nil {
		return fmt.Errorf("write systemd user service: %w", err)
	}
	if err := exec.Command("systemctl", "--user", "daemon-reload").Run(); err != nil {
		return fmt.Errorf("systemctl --user daemon-reload: %w", err)
	}
	if err := exec.Command("systemctl", "--user", "enable", "--now", systemdUserUnitName).Run(); err != nil {
		return fmt.Errorf("systemctl --user enable --now %s: %w", systemdUserUnitName, err)
	}
	return nil
}

func uninstallSystemdUser() error {
	systemdReady := systemdUserAvailable()
	if systemdReady {
		if err := exec.Command("systemctl", "--user", "disable", "--now", systemdUserUnitName).Run(); err != nil {
			// Continue removing the unit file even if the unit is already inactive.
			fmt.Fprintf(os.Stderr, "warning: systemctl --user disable --now failed: %v\n", err)
		}
	}
	unitPath := systemdUserServicePath()
	if unitPath != "" {
		if err := os.Remove(unitPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove systemd user service: %w", err)
		}
	}
	if systemdReady {
		if err := exec.Command("systemctl", "--user", "daemon-reload").Run(); err != nil {
			return fmt.Errorf("systemctl --user daemon-reload: %w", err)
		}
	}
	return nil
}

func systemdUserStart() error {
	return exec.Command("systemctl", "--user", "start", systemdUserUnitName).Run()
}

func systemdUserStop() error {
	return exec.Command("systemctl", "--user", "stop", systemdUserUnitName).Run()
}

func systemdUserStatus() (bool, error) {
	err := exec.Command("systemctl", "--user", "is-active", "--quiet", systemdUserUnitName).Run()
	if err != nil {
		return false, nil
	}
	return true, nil
}

func readSystemdUserAddr() string {
	b, err := os.ReadFile(systemdUserServicePath())
	if err != nil {
		return ""
	}
	return parseAddrArg(string(b))
}

func renderSystemdUserService(binPath, addr, socketPath, logPath string) (string, error) {
	var buf bytes.Buffer
	data := struct {
		BinPath    string
		Addr       string
		SocketPath string
		LogPath    string
	}{
		BinPath:    binPath,
		Addr:       addr,
		SocketPath: socketPath,
		LogPath:    logPath,
	}
	tmpl := template.Must(template.New("systemd").Parse(systemdUserTemplate))
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("render systemd user service: %w", err)
	}
	return buf.String(), nil
}

const systemdUserTemplate = `[Unit]
Description=MCPlexer daemon
After=network.target

[Service]
Type=simple
ExecStart={{.BinPath}} serve --mode=http --addr={{.Addr}} --socket={{.SocketPath}} --p2p
Restart=on-failure
RestartSec=2
Environment=MCPLEXER_LOG_PATH={{.LogPath}}
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=default.target
`
