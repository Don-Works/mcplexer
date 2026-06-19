// Package attachpolicy holds the daemon-level default policy for task
// attachment uploads. Leaf package — no internal dependencies — so it
// can be imported from the gateway (MCP path) and the api (REST path)
// without creating an import cycle.
//
// The shipped default is an executable-MIME denylist: documents,
// images, archives, and plain text pass; native binaries and shell
// scripts are rejected with scopes.DenialCode("attachment_mime_denied").
//
// Per-workspace overrides via an `attachment_mime_policy` row in the
// workspace config table are a planned follow-up; the surface here is
// designed so EvaluateMIME can take a future override list without
// callers having to change.
package attachpolicy

import (
	"strings"
)

// DeniedMIMEs is the daemon-level executable-MIME blocklist applied by
// default to every upload that doesn't carry an explicit per-workspace
// allow override. The list is intentionally narrow — block the shapes
// that are clearly "code that runs", not the shapes that are merely
// hazardous (e.g. SVG with embedded scripts; the dashboard's MIME
// sanitiser handles those at render time).
//
// Comparisons are case-insensitive and ignore the trailing ";charset="
// parameter so "application/x-sh; charset=utf-8" matches "application/
// x-sh".
//
// Append-only across releases — older clients may rely on the codes;
// removing one is a behaviour break.
var DeniedMIMEs = []string{
	// macOS / Mach-O native binaries (the shape of /usr/bin/* output).
	"application/x-mach-binary",
	// Linux ELF.
	"application/x-executable",
	"application/x-elf",
	// Windows PE/COFF (.exe / .dll).
	"application/vnd.microsoft.portable-executable",
	"application/x-msdownload",
	"application/x-msdos-program",
	// POSIX shells.
	"application/x-sh",
	"application/x-shellscript",
	"application/x-bash",
	"application/x-csh",
	// Windows batch + PowerShell.
	"application/bat",
	"application/x-bat",
	"application/x-msdos-program",
	"application/x-powershell",
	// Java + Android executable bundles.
	"application/java-archive",
	"application/vnd.android.package-archive",
	// Generic "this is an executable" Apple/JNLP shapes.
	"application/x-apple-diskimage",
	"application/x-java-jnlp-file",
}

// DeniedExtensions is a secondary check applied when the declared MIME
// type is generic (e.g. "application/octet-stream") but the filename
// extension nails it as executable. Catches the case where the uploader
// stripped the Content-Type or set it to a noisy default.
var DeniedExtensions = []string{
	".exe", ".dll", ".bat", ".cmd", ".ps1", ".psm1",
	".sh", ".bash", ".zsh", ".fish",
	".jar", ".apk", ".dmg", ".pkg", ".msi",
	".so", ".dylib",
	".scpt", ".applescript",
}

// EvaluateResult is the structured outcome of a policy check. Allowed
// is the happy path; on Denied=true, Code is the typed denial code the
// caller should propagate (typically "attachment_mime_denied") and
// Reason is the human-readable explanation.
type EvaluateResult struct {
	Denied bool
	Code   string
	Reason string
}

// Evaluate runs the default executable-MIME policy against (mime,
// filename). Returns Denied=true when the upload should be rejected.
// Per-workspace policy overrides will be a future overload —
// EvaluateWithPolicy(mime, filename, policy) — but the basic shape
// returned here doesn't change.
//
// Inputs are trimmed + lower-cased before comparison; the caller can
// pass them verbatim.
func Evaluate(mime, filename string) EvaluateResult {
	m := normalizeMIME(mime)
	if m != "" {
		for _, denied := range DeniedMIMEs {
			if m == denied {
				return EvaluateResult{
					Denied: true,
					Code:   "attachment_mime_denied",
					Reason: "MIME " + denied + " is on the executable-attachment denylist",
				}
			}
		}
	}
	// Extension fallback for opaque MIME shapes — when the declared
	// content-type doesn't pin down the file as executable, the
	// extension often does (and a hostile uploader controls the MIME
	// but the receiver wants the filename to render correctly).
	if ext := lowerExtension(filename); ext != "" {
		for _, denied := range DeniedExtensions {
			if ext == denied {
				return EvaluateResult{
					Denied: true,
					Code:   "attachment_mime_denied",
					Reason: "filename extension " + denied + " is on the executable-attachment denylist",
				}
			}
		}
	}
	return EvaluateResult{Denied: false}
}

// normalizeMIME lower-cases and strips parameter suffixes (";charset=
// utf-8", "; boundary=...") from a Content-Type so a single denylist
// entry catches every parametrised variant.
func normalizeMIME(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if i := strings.IndexByte(s, ';'); i >= 0 {
		s = s[:i]
	}
	return strings.ToLower(strings.TrimSpace(s))
}

// lowerExtension returns the lower-cased ".ext" suffix of filename
// (including the leading dot), or "" when there's no extension.
func lowerExtension(filename string) string {
	filename = strings.TrimSpace(filename)
	if filename == "" {
		return ""
	}
	if i := strings.LastIndexByte(filename, '.'); i >= 0 && i < len(filename)-1 {
		return strings.ToLower(filename[i:])
	}
	return ""
}
