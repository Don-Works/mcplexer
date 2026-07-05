//go:build darwin

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"html/template"
)

const plistName = "com.mcplexer.daemon"

func launchdPlistPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, "Library", "LaunchAgents", plistName+".plist")
}

func launchdInstalled() bool {
	_, err := os.Stat(launchdPlistPath())
	return err == nil
}

func installLaunchd(exePath, addr, socketPath string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("get home dir: %w", err)
	}

	// Copy binary to stable location
	if err := installBinary(exePath); err != nil {
		return fmt.Errorf("install binary: %w", err)
	}
	stablePath, err := stableBinPath()
	if err != nil {
		return fmt.Errorf("resolve stable path: %w", err)
	}

	// Write plist
	plistPath := launchdPlistPath()
	if err := os.MkdirAll(filepath.Dir(plistPath), 0755); err != nil {
		return fmt.Errorf("create LaunchAgents dir: %w", err)
	}

	logPath := filepath.Join(home, ".mcplexer", "mcplexer.log")

	// Pre-create the log file at 0600 BEFORE launchctl load. launchd opens
	// StandardOutPath/StandardErrorPath with the existing file's perms
	// when the file is already present; if we skip this step launchd
	// creates the file with the user's umask (typically 0644 = world-
	// readable) and our log lines leak. ensureLogFile is idempotent.
	if err := ensureLogFile(logPath); err != nil {
		return fmt.Errorf("pre-create log file at 0600: %w", err)
	}

	tmpl := template.Must(template.New("plist").Parse(plistTemplate))
	f, err := os.Create(plistPath)
	if err != nil {
		return fmt.Errorf("create plist: %w", err)
	}
	defer func() { _ = f.Close() }()

	data := struct {
		Label      string
		BinPath    string
		Addr       string
		SocketPath string
		LogPath    string
	}{
		Label:      plistName,
		BinPath:    stablePath,
		Addr:       addr,
		SocketPath: socketPath,
		LogPath:    logPath,
	}
	if err := tmpl.Execute(f, data); err != nil {
		return fmt.Errorf("write plist: %w", err)
	}

	// Load the agent
	if err := exec.Command("launchctl", "load", plistPath).Run(); err != nil {
		return fmt.Errorf("launchctl load: %w", err)
	}

	return nil
}

const plistTemplate = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>{{.Label}}</string>
	<key>ProgramArguments</key>
	<array>
		<string>{{.BinPath}}</string>
		<string>serve</string>
		<string>--mode=http</string>
		<string>--addr={{.Addr}}</string>
		<string>--socket={{.SocketPath}}</string>
		<string>--p2p</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<true/>
	<key>StandardOutPath</key>
	<string>{{.LogPath}}</string>
	<key>StandardErrorPath</key>
	<string>{{.LogPath}}</string>
	<key>EnvironmentVariables</key>
	<dict>
		<key>MCPLEXER_LOG_PATH</key>
		<string>{{.LogPath}}</string>
	</dict>
</dict>
</plist>`

func uninstallLaunchd() error {
	plistPath := launchdPlistPath()
	if !launchdInstalled() {
		return fmt.Errorf("launchd agent not installed")
	}

	// Unload the agent (bootout)
	uid := os.Getuid()
	domain := fmt.Sprintf("gui/%d", uid)
	// Try bootout first (modern), fall back to unload (legacy)
	if err := exec.Command("launchctl", "bootout", domain+"/"+plistName).Run(); err != nil {
		exec.Command("launchctl", "unload", plistPath).Run() //nolint:errcheck
	}

	if err := os.Remove(plistPath); err != nil {
		return fmt.Errorf("remove plist: %w", err)
	}
	return nil
}

func launchdStart() error {
	uid := os.Getuid()
	domain := fmt.Sprintf("gui/%d/%s", uid, plistName)
	return exec.Command("launchctl", "kickstart", domain).Run()
}

func launchdStop() error {
	uid := os.Getuid()
	domain := fmt.Sprintf("gui/%d/%s", uid, plistName)
	return exec.Command("launchctl", "kill", "SIGTERM", domain).Run()
}

func launchdStatus() (bool, error) {
	uid := os.Getuid()
	domain := fmt.Sprintf("gui/%d/%s", uid, plistName)
	err := exec.Command("launchctl", "print", domain).Run()
	if err != nil {
		return false, nil
	}
	return true, nil
}
