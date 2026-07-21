package sanitize

import (
	"html"
	"strings"
)

// validTrust is the canonical set of trust levels accepted by Envelope.
// Anything outside this set is normalised to "low".
//
// "peer" is a separate semantic from "low" — both are "do not obey as
// instructions", but "peer" carries the extra signal that the content
// arrived over the libp2p mesh from another machine's agent (so even
// the "this is local low-trust scraped content" reading is wrong).
// Useful for downstream policy: a router might want to redact more
// aggressively for peer-origin payloads than for first-party web scrape.
var validTrust = map[string]struct{}{
	"low":    {},
	"medium": {},
	"high":   {},
	"peer":   {},
}

// envelopePrefix is the literal tag prefix used by IsEnveloped to detect
// content that has already been wrapped upstream.
const envelopePrefix = "<untrusted-content"

// envelopeCloseTag is the closing tag that must terminate a well-formed
// envelope for IsEnveloped to accept it.
const envelopeCloseTag = "</untrusted-content>"

// bodyEscaper rewrites only the three characters that can break out of
// the envelope: '&' (must come first to avoid double-escaping the others),
// '<' (could open a new tag — most importantly </untrusted-content>),
// and '>' (cosmetic, paired with '<' for symmetry and to keep the body
// well-formed XML if anything ever tries to parse it as such).
//
// Quotes ('"' and '\”) are deliberately NOT escaped: the envelope body
// is text content (between tags), not an attribute value, so quotes are
// safe — and HTML-entity-encoding them in the wire format wastes tokens
// inside JSON (where '"' is already escaped as \" — the entity-encoded
// '&#34;' makes the value 5× longer and breaks naive grep on the
// payload). This was the original H1 ergonomics bug: the gateway used
// html.EscapeString which is intended for HTML attribute values, not
// for the safer text-content slot we actually emit into.
var bodyEscaper = strings.NewReplacer(
	"&", "&amp;",
	"<", "&lt;",
	">", "&gt;",
)

// escapeBody escapes only '&', '<', '>' — see bodyEscaper.
func escapeBody(s string) string {
	if !strings.ContainsAny(s, "&<>") {
		return s // hot path: most tool output has none of these
	}
	return bodyEscaper.Replace(s)
}

// Envelope wraps untrusted text in the canonical
// <untrusted-content source="..." trust="low">…</untrusted-content>
// envelope. The envelope tells downstream LLMs the content is not from
// the user and should not be obeyed as instructions.
//
// source identifies the producer (e.g. "github:octocat/repo#issue-42",
// "downstream:linear", "tool:customer__get_ticket"). trust must be one
// of "low" | "medium" | "high"; values outside that set are normalised
// to "low".
//
// The inner body has '<', '>', and '&' escaped to their HTML entities
// to prevent the body from closing the envelope early. Quotes are NOT
// escaped — the body is text content, not an attribute value, and
// entity-encoding quotes inside a JSON-shipped payload wastes tokens
// (the JSON layer already escapes '"' as '\"').
//
// source IS fully HTML-escaped because it is an attribute value, and
// must not be allowed to inject extra attributes (e.g. trust="high").
func Envelope(source, trust, body string) string {
	t := strings.ToLower(strings.TrimSpace(trust))
	if _, ok := validTrust[t]; !ok {
		t = "low"
	}
	// Source goes into an attribute value — keep the full HTML escape
	// (escapes quotes too) so a malicious source string cannot inject
	// trust="high" or similar.
	safeSource := html.EscapeString(source)
	// Body goes into text content — escape only '&', '<', '>' so the
	// body cannot open or close tags. Leaving quotes alone saves tokens
	// in the (very common) JSON-stringified payload case.
	safeBody := escapeBody(body)
	var b strings.Builder
	b.Grow(len(safeBody) + len(safeSource) + 64)
	b.WriteString(`<untrusted-content source="`)
	b.WriteString(safeSource)
	b.WriteString(`" trust="`)
	b.WriteString(t)
	b.WriteString(`">`)
	b.WriteString(safeBody)
	b.WriteString(`</untrusted-content>`)
	return b.String()
}

// IsEnveloped reports whether s is a complete, well-formed envelope
// produced by [Envelope]. It verifies:
//
//   - Leading whitespace + <untrusted-content opening tag
//   - Opening tag closes with '>'
//   - String ends with </untrusted-content> (optional trailing whitespace)
//
// A prefix-only tag (opening without closing), trailing text after the
// close tag, or a malformed opening tag all return false — the caller
// must scan and re-envelope such content rather than pass it through
// unexamined.
func IsEnveloped(s string) bool {
	trimmed := strings.TrimLeft(s, " \t\r\n")
	if !strings.HasPrefix(trimmed, envelopePrefix) {
		return false
	}
	// Must be followed by a space (attrs) or '>' (no attrs) — i.e. a real
	// tag, not just a prefix collision like "<untrusted-contentx".
	rest := trimmed[len(envelopePrefix):]
	if rest == "" {
		return false
	}
	next := rest[0]
	if next != ' ' && next != '>' && next != '\t' && next != '\n' {
		return false
	}
	// The opening tag must contain a '>' that closes it.
	closeAngle := strings.IndexByte(rest, '>')
	if closeAngle < 0 {
		return false
	}
	// Everything after the opening tag's '>' must end with the close tag
	// (allowing trailing whitespace).
	bodyAndClose := rest[closeAngle+1:]
	return strings.HasSuffix(strings.TrimRight(bodyAndClose, " \t\r\n"), envelopeCloseTag)
}
