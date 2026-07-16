package audit

import (
	"encoding/json"
	"regexp"
	"strings"
)

// globalRedactPatterns are key substrings that always trigger redaction.
var globalRedactPatterns = []string{
	"token",
	"key",
	"secret",
	"password",
	"passphrase",
	"authorization",
	"cookie",
	"credential",
	"bearer",
	"api_key",
	"apikey",
	"access_token",
	"refresh_token",
	"client_secret",
	"private_key",
	"session",
}

// valueRedactPatterns match values whose content looks like a credential
// regardless of the key name. Tools sometimes accept credentials in a
// generic field like `instructions`, `note`, `description`, or `body`.
//
// LOAD-BEARING for memory audit: memory__save's `content` field is a
// free-form markdown body and reaches the audit row's ParamsRedacted via
// gateway dispatch (recordAudit → auditor.Record → Redact). Every
// canonical secret shape we want kept out of the audit ledger MUST have
// a pattern here. testdata/secret_patterns.txt is the regression corpus
// (see redact_patterns_test.go) — adding a pattern here without adding
// a fixture line will leave us with silent regressions.
var valueRedactPatterns = []*regexp.Regexp{
	// Bearer tokens with at least 16 chars after "Bearer "
	regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9._\-+/=]{16,}`),
	// "Authorization: ..." style headers
	regexp.MustCompile(`(?i)\bauthorization\s*[:=]\s*[^\s,;}]+`),
	// GitHub PAT (ghp_) and fine-grained PAT
	regexp.MustCompile(`\bghp_[A-Za-z0-9]{30,}\b`),
	regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{40,}\b`),
	// GitHub OAuth-flow tokens: user-to-server (ghu_), server-to-server
	// (ghs_), refresh (ghr_), OAuth app installation (gho_). Same
	// 30+-char tail as ghp_ — these were a coverage gap caught by the
	// memory deep-redaction scenario (D7.5).
	regexp.MustCompile(`\bgh[ousr]_[A-Za-z0-9]{30,}\b`),
	// OpenAI / Anthropic / generic sk- secret keys. Provider namespaces have
	// grown from one segment (sk-ant-...) to multi-segment forms; treat any
	// long, token-shaped sk- value as sensitive instead of trying to enumerate
	// the namespace grammar.
	regexp.MustCompile(`\bsk-[A-Za-z0-9][A-Za-z0-9_-]{19,}\b`),
	// Bare OpenAI "classic" keys (sk-<48 base62>) — no namespace
	// subprefix (no `-proj-`, no `-ant-`). The mainline sk- pattern
	// above requires a lowercase namespace word after "sk-"; a key
	// that starts with uppercase (sk-ABC…) slips past it.
	regexp.MustCompile(`\bsk-[A-Za-z0-9]{32,}\b`),
	// Stripe secret keys (sk_live_..., sk_test_..., rk_live_..., rk_test_...)
	regexp.MustCompile(`\b(?:sk|rk)_(?:live|test)_[A-Za-z0-9]{20,}\b`),
	// GitLab personal access tokens
	regexp.MustCompile(`\bglpat-[A-Za-z0-9_\-]{16,}\b`),
	// AWS access key id
	regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),
	// AWS temporary access key id (STS, role-assumption)
	regexp.MustCompile(`\bASIA[0-9A-Z]{16}\b`),
	// Google API keys: "AIza" + 35+ url-safe chars. Real keys are
	// exactly 35 after the prefix; the {35,} bound is a defensive
	// upper-open so a future format bump or a fixture with extra
	// padding still trips redaction.
	regexp.MustCompile(`\bAIza[0-9A-Za-z_\-]{35,}\b`),
	// Slack tokens (bot/app/user/refresh)
	regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9-]{10,}\b`),
	// Slack incoming-webhook URLs (the URL IS the secret).
	regexp.MustCompile(`https://hooks\.slack\.com/services/T[A-Za-z0-9]+/B[A-Za-z0-9]+/[A-Za-z0-9]+`),
	// Google Chat incoming-webhook URLs — the key+token query IS the secret.
	regexp.MustCompile(`https://chat\.googleapis\.com/v1/spaces/[^/\s]+/messages\?[^\s"']+`),
	// Telegram bot tokens: "<bot_id>:<30+_char_alnum_dash_underscore>".
	// The colon + length make this distinctive enough to avoid
	// false-positives on ordinary "id:thing" pairs. Real Telegram tokens
	// settle at 35 chars after the colon but the format has drifted
	// historically, so allow >=30.
	regexp.MustCompile(`\b[0-9]{6,12}:[A-Za-z0-9_\-]{30,}\b`),
	// Standard JWT shape (header.payload.signature, all base64url).
	regexp.MustCompile(`\beyJ[A-Za-z0-9_\-]{10,}\.[A-Za-z0-9_\-]{10,}\.[A-Za-z0-9_\-]{10,}\b`),
	// Private key PEM blocks (RSA, EC, OpenSSH, generic). The header
	// alone is enough signal — if it's quoted in a memory body we
	// don't want it in the audit row.
	regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?-----END [A-Z ]*PRIVATE KEY-----`),
}

const redactedValue = "[REDACTED]"

// Redact replaces sensitive values in a JSON params object with [REDACTED].
// It matches keys against global patterns and the provided per-scope hints.
func Redact(params json.RawMessage, hints []string) json.RawMessage {
	if len(params) == 0 {
		return params
	}

	// Try as object first.
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(params, &obj); err == nil {
		changed := false
		for key, val := range obj {
			if shouldRedact(key, hints) {
				redacted, _ := json.Marshal(redactedValue)
				obj[key] = redacted
				changed = true
				continue
			}
			// Recurse into nested objects and arrays
			if redacted := Redact(val, hints); !jsonEqual(val, redacted) {
				obj[key] = redacted
				changed = true
			}
		}
		if !changed {
			return params
		}
		result, err := json.Marshal(obj)
		if err != nil {
			return params
		}
		return result
	}

	// Try as array — recurse into each element.
	var arr []json.RawMessage
	if err := json.Unmarshal(params, &arr); err == nil {
		changed := false
		for i, val := range arr {
			if redacted := Redact(val, hints); !jsonEqual(val, redacted) {
				arr[i] = redacted
				changed = true
			}
		}
		if !changed {
			return params
		}
		result, err := json.Marshal(arr)
		if err != nil {
			return params
		}
		return result
	}

	// Scalar — try as string and pattern-match to catch credentials in
	// generic fields like `instructions` or `note`.
	var s string
	if err := json.Unmarshal(params, &s); err == nil && s != "" {
		if redacted, changed := redactStringValue(s); changed {
			out, err := json.Marshal(redacted)
			if err != nil {
				return params
			}
			return out
		}
	}

	return params
}

// redactStringValue applies value-pattern redaction to a string, returning
// the (possibly modified) string and whether it changed.
func redactStringValue(s string) (string, bool) {
	original := s
	for _, re := range valueRedactPatterns {
		s = re.ReplaceAllString(s, redactedValue)
	}
	return s, s != original
}

// PatternMatches reports whether s contains any value shape the redactor
// would mask. Used by offline scrub tooling that needs to find historical
// audit_log rows that should have been redacted but weren't (e.g. rows
// written before a new pattern was added). Uses the exact same pattern
// slice as redactStringValue so the scan can't drift from the redactor.
func PatternMatches(s string) bool {
	if s == "" {
		return false
	}
	for _, re := range valueRedactPatterns {
		if re.MatchString(s) {
			return true
		}
	}
	return false
}

// RedactString applies the value-pattern redaction set (and hint-based
// substring redaction) to a free-form string. Use for fields like
// AuditRecord.ErrorMessage that carry subprocess stderr or adapter
// errors and may contain credentials that arrived through the message,
// not through a structured key.
//
// Hints are treated as case-insensitive substring matches: any
// occurrence of a hint inside s is replaced with [REDACTED]. This is
// the string-level analogue of shouldRedact's key-substring rule, and
// lets per-scope hints like "GITHUB_TOKEN" catch the raw value when an
// adapter folds it into an error message.
func RedactString(s string, hints []string) string {
	if s == "" {
		return s
	}
	out, _ := redactStringValue(s)
	for _, hint := range hints {
		if hint == "" {
			continue
		}
		out = replaceCaseInsensitive(out, hint, redactedValue)
	}
	return out
}

// replaceCaseInsensitive replaces every case-insensitive occurrence of
// needle in haystack with repl. Returns haystack unchanged when needle
// is empty (callers already filter empties, but be defensive).
//
// Matching is done with a case-insensitive regexp built from the literal
// needle rather than by lowercasing the haystack and indexing into the
// original. The lowercase-and-index approach is unsafe: strings.ToLower
// can change byte length (e.g. 'İ' U+0130, 'ß') so indices computed
// against the lowered copy no longer align with the original — that
// either corrupts the output (slicing mid-rune) or panics on adversarial
// error-message bytes. The regexp path keeps byte positions in the
// original string and never indexes a derived copy.
func replaceCaseInsensitive(haystack, needle, repl string) string {
	if needle == "" {
		return haystack
	}
	re, err := regexp.Compile(`(?i)` + regexp.QuoteMeta(needle))
	if err != nil {
		// QuoteMeta output is always a valid pattern, so this is
		// unreachable; fall back to the original unchanged on the
		// off-chance the regexp engine ever rejects it.
		return haystack
	}
	return re.ReplaceAllLiteralString(haystack, repl)
}

// shouldRedact checks if a key matches any global pattern or per-scope hint.
func shouldRedact(key string, hints []string) bool {
	lower := strings.ToLower(key)
	for _, pattern := range globalRedactPatterns {
		if strings.Contains(lower, pattern) {
			return true
		}
	}
	for _, hint := range hints {
		if strings.Contains(lower, strings.ToLower(hint)) {
			return true
		}
	}
	return false
}

func jsonEqual(a, b json.RawMessage) bool {
	return string(a) == string(b)
}

// RedactArgs returns a copy of args with credential-shaped substrings
// replaced by [REDACTED]. It handles three element shapes:
//
//  1. bare positional value matching any valueRedactPattern
//     (e.g. "ghp_AAAA…" → "[REDACTED]").
//  2. "--flag=value" or "-x=value" — if the flag basename matches a
//     key pattern (token/key/secret/...), the value half is replaced;
//     otherwise the value still passes through redactStringValue.
//  3. "key=value" (no leading dash) — same key-pattern check; either
//     way the value half is run through redactStringValue.
//
// Args are not split across positions: ["--token", "ghp_real"] leaves
// element 0 alone and only element 1 is value-pattern matched.
// The input slice is not modified.
func RedactArgs(args []string, hints []string) []string {
	out := make([]string, len(args))
	for i, a := range args {
		out[i] = redactArgElement(a, hints)
	}
	return out
}

// redactArgElement applies the per-element redaction rules described
// on RedactArgs to a single arg string.
func redactArgElement(a string, hints []string) string {
	key, val, ok := strings.Cut(a, "=")
	if !ok {
		// Positional — just value-pattern match.
		redacted, _ := redactStringValue(a)
		return redacted
	}
	bareKey := strings.TrimLeft(key, "-")
	if bareKey != "" && shouldRedact(bareKey, hints) {
		return key + "=" + redactedValue
	}
	redactedVal, _ := redactStringValue(val)
	return key + "=" + redactedVal
}

// RedactEnv returns a copy of env with values redacted when either
// the key matches a key pattern (global + hints) OR the value matches
// any valueRedactPattern. Keys are never modified. Input map is not
// modified; nil input yields an empty (non-nil) map.
func RedactEnv(env map[string]string, hints []string) map[string]string {
	out := make(map[string]string, len(env))
	for k, v := range env {
		if shouldRedact(k, hints) {
			out[k] = redactedValue
			continue
		}
		redacted, _ := redactStringValue(v)
		out[k] = redacted
	}
	return out
}
