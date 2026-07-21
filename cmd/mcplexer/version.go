package main

import (
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"runtime/debug"
	"strings"
)

var (
	buildVersion = "dev"
	buildCommit  = ""
)

func resolveMCPlexerVersion() string {
	version := strings.TrimSpace(buildVersion)
	commit := strings.TrimSpace(buildCommit)
	modified := false

	if info, ok := debug.ReadBuildInfo(); ok {
		if version == "" || version == "dev" {
			if info.Main.Version != "" && info.Main.Version != "(devel)" {
				version = info.Main.Version
			}
		}
		for _, setting := range info.Settings {
			switch setting.Key {
			case "vcs.revision":
				if commit == "" {
					commit = setting.Value
				}
			case "vcs.modified":
				modified = setting.Value == "true"
			}
		}
	}

	return formatBuildVersion(version, commit, modified)
}

type versionInfo struct {
	Version string `json:"version"`
	Commit  string `json:"commit,omitempty"`
	GOOS    string `json:"goos"`
	GOARCH  string `json:"goarch"`
	P2P     bool   `json:"p2p"`
}

func cmdVersion(args []string) error {
	info := currentVersionInfo()
	if len(args) > 0 && args[0] == "--json" {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(info)
	}
	fmt.Println(info.Version)
	return nil
}

func currentVersionInfo() versionInfo {
	return versionInfo{
		Version: resolveMCPlexerVersion(),
		Commit:  shortCommit(buildCommit, 12),
		GOOS:    runtime.GOOS,
		GOARCH:  runtime.GOARCH,
		P2P:     p2pBuildEnabled(),
	}
}

func formatBuildVersion(version, commit string, modified bool) string {
	version = strings.TrimSpace(version)
	commit = strings.TrimSpace(commit)
	if version == "" || version == "(devel)" {
		version = "dev"
	}

	if commit != "" {
		short := shortCommit(commit, 12)
		if short != "" &&
			!strings.Contains(version, commit) &&
			!strings.Contains(version, short) &&
			!strings.Contains(version, shortCommit(commit, 7)) {
			version += "+" + short
		}
	}

	if modified && !strings.Contains(version, "dirty") {
		version += "-dirty"
	}
	return version
}

func shortCommit(commit string, n int) string {
	commit = strings.TrimSpace(commit)
	if commit == "" {
		return ""
	}
	if len(commit) <= n {
		return commit
	}
	return commit[:n]
}
