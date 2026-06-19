//go:build darwin && cgo

package main

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework ApplicationServices -framework CoreFoundation
#include <ApplicationServices/ApplicationServices.h>
#include <stdbool.h>

// mcplexer_check_accessibility_prompt wraps AXIsProcessTrustedWithOptions
// with kAXTrustedCheckOptionPrompt=true so the system surfaces the
// "Allow X to control your computer using accessibility features" prompt
// the first time a binary asks. macOS is idempotent here: subsequent
// calls when already trusted return true without re-prompting, and
// repeated calls while denied don't re-spam the user — the system
// rate-limits the dialog itself.
//
// Returns true when the calling process is in the Accessibility allowlist
// at /System/Library/PrivateFrameworks/... (System Settings → Privacy &
// Security → Accessibility). When false, the system has already shown or
// queued the prompt for the user.
static bool mcplexer_check_accessibility_prompt(void) {
    const void *keys[]   = { kAXTrustedCheckOptionPrompt };
    const void *values[] = { kCFBooleanTrue };
    CFDictionaryRef options = CFDictionaryCreate(
        kCFAllocatorDefault, keys, values, 1,
        &kCFTypeDictionaryKeyCallBacks,
        &kCFTypeDictionaryValueCallBacks);
    bool trusted = AXIsProcessTrustedWithOptions(options);
    CFRelease(options);
    return trusted;
}
*/
import "C"

import (
	"log/slog"
	"os"
)

// requestAccessibility asks macOS to grant the daemon Accessibility
// permission, triggering the "Allow … to control your computer using
// accessibility features" system prompt the first time. Required for
// native UI control surfaces — focus_app, send_keys, key chords —
// that mcplexer drives directly (separate from the Hammerspoon bridge,
// which has its own grant against the Hammerspoon binary).
//
// Idempotent: macOS no-ops when already granted, and won't re-spam the
// dialog when denied. Safe to call on every daemon startup.
//
// The system prompt identifies the calling binary by absolute path
// ("Allow /Users/<you>/.mcplexer/bin/mcplexer to control your computer…").
// Granting attaches to that exact path, so a `make upgrade` that
// atomically swaps the file under the same path keeps the grant — but
// re-installing to a different location requires re-grant.
func requestAccessibility(logger *slog.Logger) {
	if logger == nil {
		logger = slog.Default()
	}
	trusted := bool(C.mcplexer_check_accessibility_prompt())
	exe, _ := os.Executable()
	if trusted {
		logger.Info("macOS Accessibility: granted",
			"binary", exe,
		)
		return
	}
	logger.Warn("macOS Accessibility: NOT granted — system prompt surfaced",
		"binary", exe,
		"hint", "open System Settings → Privacy & Security → Accessibility and enable the entry for this mcplexer binary",
	)
}
