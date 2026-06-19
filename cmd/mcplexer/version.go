package main

import (
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
