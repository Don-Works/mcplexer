package escalate

import (
	"fmt"
	"net/url"
	"strings"
	"unicode"

	"github.com/don-works/mcplexer/internal/logwatch/distill"
)

const googleChatEvidenceLimit = 280

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func normalizePublicURL(raw string) string {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return ""
	}
	return raw
}

func taskURL(publicURL, workspaceID, taskID string) string {
	if publicURL == "" || workspaceID == "" || taskID == "" {
		return ""
	}
	return publicURL + "/tasks/" + url.PathEscape(taskID) +
		"?workspace=" + url.QueryEscape(workspaceID)
}

// RenderMessage is the compact Google Chat representation.
func RenderMessage(workspaceName, gatewayHost, publicURL string, n distill.Notification) string {
	workspaceName = firstNonEmpty(workspaceName, "unknown-system")
	gatewayHost = firstNonEmpty(gatewayHost, "mcplexer")

	var body strings.Builder
	fmt.Fprintf(&body, "*%s · %s*\n%s", upper(n.Severity), workspaceName, strings.TrimSpace(n.Title))
	contextLines := renderContextLines(gatewayHost, n)
	if len(contextLines) > 0 {
		body.WriteString("\n\n")
		body.WriteString(strings.Join(contextLines, "\n"))
	}
	if evidence := strings.TrimSpace(n.Body); evidence != "" {
		body.WriteString("\n\n")
		body.WriteString("*Evidence:* ")
		body.WriteString(compactChatEvidence(evidence))
	}
	if footer := renderRichFooter(publicURL, n); len(footer) > 0 {
		body.WriteString("\n\n")
		body.WriteString(strings.Join(footer, "\n"))
	}
	return body.String()
}

func renderContextLines(gatewayHost string, n distill.Notification) []string {
	lines := make([]string, 0, 3)
	if n.RemoteHostName != "" || n.RemoteHostAddr != "" {
		host := n.RemoteHostName
		if host == "" {
			host = n.RemoteHostAddr
		} else if n.RemoteHostAddr != "" {
			host += " (" + n.RemoteHostAddr + ")"
		}
		lines = append(lines, "*Host:* "+host)
	}
	if n.SourceName != "" {
		lines = append(lines, "*Source:* `"+n.SourceName+"`")
	}
	return append(lines, "*Watcher:* `"+gatewayHost+"`")
}

func renderRichFooter(publicURL string, n distill.Notification) []string {
	footer := make([]string, 0, 1)
	if n.TaskID != "" {
		if link := taskURL(normalizePublicURL(publicURL), n.WorkspaceID, n.TaskID); link != "" {
			footer = append(footer, "*Task:* <"+link+"|"+n.TaskID+">")
		} else {
			footer = append(footer, "*Task:* `"+n.TaskID+"`")
		}
	}
	return footer
}

// compactChatEvidence turns the worker's structured multi-section report into
// one bounded operator summary. The canonical task retains the full evidence;
// Chat should carry enough to decide whether to open it, not reproduce it.
func compactChatEvidence(raw string) string {
	parts := make([]string, 0, 4)
	skipSection := false
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		line = strings.TrimSpace(strings.TrimLeft(line, "-*•"))
		if line == "" {
			continue
		}
		switch chatEvidenceHeading(line) {
		case "hypotheses":
			skipSection = true
			continue
		case "evidence", "next":
			skipSection = false
			continue
		}
		if skipSection {
			continue
		}
		parts = append(parts, strings.Join(strings.Fields(line), " "))
	}
	summary := strings.Join(parts, " · ")
	if summary == "" {
		summary = strings.Join(strings.Fields(raw), " ")
	}
	runes := []rune(summary)
	if len(runes) <= googleChatEvidenceLimit {
		return summary
	}
	cut := googleChatEvidenceLimit - 1
	for cut > googleChatEvidenceLimit-40 && !unicode.IsSpace(runes[cut]) {
		cut--
	}
	if cut <= googleChatEvidenceLimit-40 {
		cut = googleChatEvidenceLimit - 1
	}
	return strings.TrimSpace(string(runes[:cut])) + "…"
}

func chatEvidenceHeading(line string) string {
	switch strings.ToLower(strings.TrimSuffix(line, ":")) {
	case "observed evidence", "verified facts":
		return "evidence"
	case "hypotheses / unknowns", "hypotheses/unknowns":
		return "hypotheses"
	case "next step", "next steps":
		return "next"
	default:
		return ""
	}
}

// RenderPlainMessage is the deterministic representation for all channels
// that do not speak Google Chat markup.
func RenderPlainMessage(workspaceName, gatewayHost, publicURL string, n distill.Notification) string {
	workspaceName = firstNonEmpty(workspaceName, "unknown-system")
	gatewayHost = firstNonEmpty(gatewayHost, "mcplexer")
	var body strings.Builder
	body.WriteString(Envelope(workspaceName, gatewayHost, n.Severity, n.RemoteHostName, n.RemoteHostAddr))
	if title := strings.TrimSpace(n.Title); title != "" {
		body.WriteString("\n" + title)
	}
	if n.SourceName != "" {
		body.WriteString("\n\nSource: " + n.SourceName)
	}
	if evidence := strings.TrimSpace(n.Body); evidence != "" {
		body.WriteString("\n\n" + evidence)
	}
	if footer := renderPlainFooter(publicURL, n); len(footer) > 0 {
		body.WriteString("\n\n" + strings.Join(footer, "\n"))
	}
	return body.String()
}

func renderPlainFooter(publicURL string, n distill.Notification) []string {
	footer := make([]string, 0, 2)
	if n.TaskID != "" {
		if link := taskURL(normalizePublicURL(publicURL), n.WorkspaceID, n.TaskID); link != "" {
			footer = append(footer, "Task: "+link)
		} else {
			footer = append(footer, "Task: "+n.TaskID)
		}
	}
	if n.TemplateID != "" {
		footer = append(footer, "Template: "+n.TemplateID)
	}
	return footer
}

func Envelope(workspaceName, gatewayHost, severity, remoteHostName, remoteHostAddr string) string {
	host := remoteHostName
	if remoteHostAddr != "" {
		host += " (" + remoteHostAddr + ")"
	}
	if host == "" {
		host = "-"
	}
	return fmt.Sprintf("[%s · via %s] %s · %s", workspaceName, gatewayHost, upper(severity), host)
}

func upper(value string) string {
	return strings.ToUpper(value)
}
