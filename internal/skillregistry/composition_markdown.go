package skillregistry

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

var (
	includeMarkerRE = regexp.MustCompile(`^<!-- mcpx:include ([a-z0-9][a-z0-9-]*[a-z0-9]|[a-z0-9]) -->$`)
	sectionMarkerRE = regexp.MustCompile(`^<!-- mcpx:section ([a-z0-9][a-z0-9-]*[a-z0-9]|[a-z0-9]) -->$`)
)

func splitSkillDocument(body string) (prefix, markdown string, err error) {
	trimmed := strings.TrimLeft(body, " \t\r\n")
	leading := len(body) - len(trimmed)
	if !strings.HasPrefix(trimmed, "---") {
		return "", "", errors.New("missing leading --- frontmatter fence")
	}
	restStart := leading + len("---")
	if restStart < len(body) && body[restStart] == '\r' {
		restStart++
	}
	if restStart < len(body) && body[restStart] == '\n' {
		restStart++
	}
	closing := strings.Index(body[restStart:], "\n---")
	if closing < 0 {
		return "", "", errors.New("missing closing --- frontmatter fence")
	}
	tailStart := restStart + closing + len("\n---")
	if tailStart < len(body) && body[tailStart] == '\r' {
		tailStart++
	}
	if tailStart < len(body) && body[tailStart] == '\n' {
		tailStart++
	}
	return body[:tailStart], body[tailStart:], nil
}

func selectCompositionSection(markdown, requested string) (string, error) {
	var out strings.Builder
	seen := map[string]bool{}
	activeSection := ""
	fenceChar, fenceLen := byte(0), 0
	found := requested == ""
	for _, line := range splitLinesAfter(markdown) {
		trimmed := strings.TrimSpace(strings.TrimRight(line, "\r\n"))
		if nextChar, nextLen, boundary := advanceFence(trimmed, fenceChar, fenceLen); boundary {
			fenceChar, fenceLen = nextChar, nextLen
			if requested == "" || activeSection == requested {
				out.WriteString(line)
			}
			continue
		}
		if fenceChar != 0 {
			if requested == "" || activeSection == requested {
				out.WriteString(line)
			}
			continue
		}
		if match := sectionMarkerRE.FindStringSubmatch(trimmed); len(match) > 0 && validCompositionMarkerName(match[1]) {
			name := match[1]
			if activeSection != "" {
				return "", fmt.Errorf("section %q is nested inside %q", name, activeSection)
			}
			if seen[name] {
				return "", fmt.Errorf("section %q is declared more than once", name)
			}
			seen[name] = true
			activeSection = name
			if name == requested {
				found = true
			}
			continue
		}
		if trimmed == "<!-- mcpx:endsection -->" {
			if activeSection == "" {
				return "", errors.New("endsection has no matching section")
			}
			activeSection = ""
			continue
		}
		if strings.HasPrefix(trimmed, "<!-- mcpx:section") || strings.HasPrefix(trimmed, "<!-- mcpx:endsection") {
			return "", fmt.Errorf("malformed section marker %q", trimmed)
		}
		if requested == "" || activeSection == requested {
			out.WriteString(line)
		}
	}
	if activeSection != "" {
		return "", fmt.Errorf("section %q has no matching endsection", activeSection)
	}
	if !found {
		return "", fmt.Errorf("section %q was not found", requested)
	}
	return out.String(), nil
}

func validCompositionMarkerName(name string) bool {
	return len(name) <= 64 && !strings.Contains(name, "--")
}

func splitLinesAfter(s string) []string {
	if s == "" {
		return nil
	}
	return strings.SplitAfter(s, "\n")
}

func fenceMarker(trimmed string) (byte, int, bool) {
	if len(trimmed) < 3 || (trimmed[0] != '`' && trimmed[0] != '~') {
		return 0, 0, false
	}
	ch := trimmed[0]
	count := 0
	for count < len(trimmed) && trimmed[count] == ch {
		count++
	}
	if count < 3 {
		return 0, 0, false
	}
	return ch, count, true
}

func advanceFence(trimmed string, activeChar byte, activeLen int) (byte, int, bool) {
	ch, count, marker := fenceMarker(trimmed)
	if activeChar == 0 {
		if marker {
			return ch, count, true
		}
		return 0, 0, false
	}
	if !marker || ch != activeChar || count < activeLen {
		return activeChar, activeLen, false
	}
	// CommonMark closing fences may only carry whitespace after the fence.
	// A line such as ```go inside a backtick fence is content, not a close.
	if strings.TrimSpace(trimmed[count:]) != "" {
		return activeChar, activeLen, false
	}
	return 0, 0, true
}
