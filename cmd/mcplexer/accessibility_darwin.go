//go:build darwin && cgo

package main

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework ApplicationServices -framework CoreFoundation
#include <ApplicationServices/ApplicationServices.h>
#include <stdbool.h>

static bool mcplexer_check_accessibility(void) {
    return AXIsProcessTrusted();
}

// mcplexer_request_accessibility_prompt wraps AXIsProcessTrustedWithOptions
// with kAXTrustedCheckOptionPrompt=true so the system surfaces the
// "Allow X to control your computer using accessibility features" prompt.
// This must be called only from an explicit user action, not daemon startup.
static bool mcplexer_request_accessibility_prompt(void) {
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

const accessibilityPromptEnv = "MCPLEXER_REQUEST_ACCESSIBILITY"

func accessibilityTrusted() bool {
	return bool(C.mcplexer_check_accessibility())
}

func promptAccessibility() bool {
	return bool(C.mcplexer_request_accessibility_prompt())
}

// requestAccessibility checks macOS Accessibility state for daemon logs.
// It deliberately does not open the system prompt during normal daemon
// startup. Rebuilt command-line binaries can get new TCC code identities,
// so prompting from launchd startup causes repeated macOS dialogs during
// local upgrades. Use `mcplexer doctor --request-accessibility`, or set
// MCPLEXER_REQUEST_ACCESSIBILITY=1 for a one-off explicit prompt.
func requestAccessibility(logger *slog.Logger) {
	if logger == nil {
		logger = slog.Default()
	}
	exe, _ := os.Executable()
	if accessibilityTrusted() {
		logger.Info("macOS Accessibility: granted",
			"binary", exe,
		)
		return
	}

	if os.Getenv(accessibilityPromptEnv) == "1" {
		trusted := promptAccessibility()
		if trusted {
			logger.Info("macOS Accessibility: granted after explicit prompt",
				"binary", exe,
			)
			return
		}
		logger.Warn("macOS Accessibility: explicit prompt requested, still not granted",
			"binary", exe,
			"hint", "open System Settings > Privacy & Security > Accessibility and enable this mcplexer binary",
		)
		return
	}

	logger.Warn("macOS Accessibility: not granted; not prompting automatically",
		"binary", exe,
		"hint", exe+" doctor --request-accessibility",
	)
}
