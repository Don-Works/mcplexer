package googlechat

import (
	"fmt"
	"strings"
)

// BodyMaxChars caps the text we forward to Google Chat. The API ceiling is
// 4000 chars per message; 1500 leaves ample headroom for title + tag footer
// without flirting with truncation mid-word.
const BodyMaxChars = 1500

// TruncateBody trims a message body to BodyMaxChars, appending an ellipsis
// when truncated.
func TruncateBody(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= BodyMaxChars {
		return s
	}
	return s[:BodyMaxChars-1] + "…"
}

// BuildTitle renders a short "<kind> from <agent>[ (<role>)]" header line.
func BuildTitle(agentName, role, kind string) string {
	who := agentName
	if role != "" && role != agentName {
		who = fmt.Sprintf("%s (%s)", agentName, role)
	}
	return fmt.Sprintf("%s from %s", kind, who)
}

// RenderText formats an OutgoingMessage into Google Chat's flavoured markdown
// (a constrained subset — *bold*, _italic_, ~strike~). Layout mirrors the
// telegram bridge for cross-channel UX parity:
//
//	*<title>*
//	<body>
//	<hashtag line>
//
// Google Chat does not require character escaping for this subset, but the
// dollar-sign + angle-bracket markup it does interpret is out-of-band of our
// content (we don't generate <users/...> hyperlinks), so a plain pass-through
// is safe.
func RenderText(msg OutgoingMessage) string {
	var b strings.Builder
	if title := strings.TrimSpace(msg.Title); title != "" {
		b.WriteString("*")
		b.WriteString(title)
		b.WriteString("*\n")
	}
	if body := strings.TrimSpace(msg.Body); body != "" {
		b.WriteString(body)
	}
	if tags := formatHashtags(msg.Tags, msg.Priority); tags != "" {
		b.WriteString("\n")
		b.WriteString(tags)
	}
	return b.String()
}

// formatHashtags renders a "#tag1 #tag2" footer.
func formatHashtags(tags, priority string) string {
	var out []string
	if priority != "" {
		out = append(out, "#"+sanitizeTag(priority))
	}
	for _, t := range strings.Split(tags, ",") {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		out = append(out, "#"+sanitizeTag(t))
	}
	if len(out) == 0 {
		return ""
	}
	return strings.Join(out, " ")
}

func sanitizeTag(s string) string {
	s = strings.TrimSpace(s)
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '_' || r == '-':
			b.WriteRune('_')
		case r == ' ':
			b.WriteRune('_')
		}
	}
	if b.Len() == 0 {
		return "tag"
	}
	return b.String()
}
