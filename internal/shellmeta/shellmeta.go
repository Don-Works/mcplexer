package shellmeta

import "strings"

// ContainsUnquotedMetachar scans cmd for shell command-chaining characters
// (; | & ` \n \r) that appear outside quotes, or inside double quotes for
// backtick (which is still command substitution in double quotes).
// Returns the first such character and true, or 0 and false if none.
// Heredoc bodies with *quoted* delimiters are fully literal (no expansion).
// Bodies with unquoted delimiters still suppress ;|& as data, but ` and
// command substitutions (via Find) inside them are active and must be flagged.
func ContainsUnquotedMetachar(cmd string) (byte, bool) {
	st := stateNormal
	type hd struct {
		delim  string
		quoted bool
	}
	var heredocs []hd
	for i := 0; i < len(cmd); i++ {
		c := cmd[i]
		switch st {
		case stateNormal:
			switch c {
			case '\'':
				st = stateSingleQuote
			case '"':
				st = stateDoubleQuote
			case '\\':
				st = stateBackslash
			case '<':
				if len(heredocs) == 0 {
					if delim, q, next, ok := parseHeredocRedirect(cmd, i); ok {
						heredocs = append(heredocs, hd{delim: delim, quoted: q})
						i = next - 1
					}
				}
			case ';', '|', '&', '`', '\n', '\r':
				// A '&' that is part of a file-descriptor redirect
				// (2>&1, >&2, &>file, 1>&2, 0<&3, <&-) is NOT command
				// chaining — the token after it is a redirect target
				// (an fd number or a filename), never an executable. Only
				// a backgrounding '&' (cmd & next) or '&&' chains a second
				// command, and those are still flagged below. Skip the
				// redirect form before any heredoc/chain handling so
				// `go test ./... 2>&1` stops being a false positive.
				if c == '&' && isRedirectAmpersand(cmd, i) {
					continue
				}
				// Detect closing delim line for pending heredoc (supports mixed quoted/unquoted)
				if (c == '\n' || c == '\r') && len(heredocs) > 0 {
					lineStart := i + 1
					p := lineStart
					for p < len(cmd) && cmd[p] != '\n' && cmd[p] != '\r' {
						p++
					}
					line := cmd[lineStart:p]
					trimmed := strings.TrimLeft(line, "\t ")
					if trimmed == heredocs[0].delim {
						// closing this heredoc
						restStart := p
						if p < len(cmd) {
							if cmd[p] == '\r' && p+1 < len(cmd) && cmd[p+1] == '\n' {
								restStart = p + 2
							} else {
								restStart = p + 1
							}
						}
						heredocs = heredocs[1:]
						if strings.TrimSpace(cmd[restStart:]) != "" {
							// command follows the heredoc: treat the transition as metachar
							return '\n', true
						}
						i = p // will be advanced by for-loop; skip flagging this internal nl
						continue
					}
				}
				// While inside an open heredoc body, suppress data metachars.
				if len(heredocs) > 0 {
					if heredocs[0].quoted {
						// quoted delim: entire body literal, suppress everything including `
						if c == ';' || c == '|' || c == '&' || c == '`' || c == '\n' || c == '\r' {
							continue
						}
					} else {
						// unquoted delim: expansions active in body
						if c == '`' {
							return c, true // ` executes even in heredoc body
						}
						if c == ';' || c == '|' || c == '&' || c == '\n' || c == '\r' {
							continue // data or heredoc line; not top-level chain
						}
					}
				}
				return c, true
			}
		case stateSingleQuote:
			if c == '\'' {
				st = stateNormal
			}
		case stateDoubleQuote:
			switch c {
			case '"':
				st = stateNormal
			case '\\':
				if i+1 < len(cmd) {
					switch cmd[i+1] {
					case '\\', '"', '$', '`', '\n':
						i++
					}
				}
			case '`':
				return c, true
			}
		case stateBackslash:
			st = stateNormal
		}
	}
	return 0, false
}

// FindUnquotedSubstitution scans cmd for shell substitution openers
// ($(, <(, >() that appear outside quotes, or inside double quotes for
// $( only (command substitution works inside double quotes; process
// substitution <( >( is literal in double quotes). $(( arithmetic
// expansion is excluded. Returns the matching opener or "".
// For heredocs: only quoted-delimiter heredocs ('EOF') treat their body
// as inert (subs ignored). Unquoted-delimiter heredocs expand their body,
// so $( <( >( found inside must be reported (P0 bypass vector).
func FindUnquotedSubstitution(cmd string) string {
	st := stateNormal
	type hd struct {
		delim  string
		quoted bool
	}
	var heredocs []hd
	for i := 0; i < len(cmd); i++ {
		c := cmd[i]
		switch st {
		case stateNormal:
			switch c {
			case '\'':
				st = stateSingleQuote
			case '"':
				st = stateDoubleQuote
			case '\\':
				st = stateBackslash
			case '\n', '\r':
				if len(heredocs) > 0 {
					lineStart := i + 1
					p := lineStart
					for p < len(cmd) && cmd[p] != '\n' && cmd[p] != '\r' {
						p++
					}
					line := cmd[lineStart:p]
					trimmed := strings.TrimLeft(line, "\t ")
					if trimmed == heredocs[0].delim {
						heredocs = heredocs[1:]
						i = p
						continue
					}
					// not closing; fallthrough (scan body for unquoted subs if applicable)
				}
			case '$':
				if len(heredocs) > 0 && heredocs[0].quoted {
					// quoted heredoc body: $ is literal data, no expansion
				} else if i+1 < len(cmd) && cmd[i+1] == '(' {
					if i+2 < len(cmd) && cmd[i+2] == '(' {
						continue
					}
					return "$("
				}
			case '<':
				if len(heredocs) == 0 {
					if delim, q, next, ok := parseHeredocRedirect(cmd, i); ok {
						heredocs = append(heredocs, hd{delim: delim, quoted: q})
						i = next - 1
						continue
					}
				}
				if len(heredocs) > 0 && heredocs[0].quoted {
					// literal in quoted body
				} else if i+1 < len(cmd) && cmd[i+1] == '(' {
					return "<("
				}
			case '>':
				if len(heredocs) > 0 && heredocs[0].quoted {
					// literal in quoted body
				} else if i+1 < len(cmd) && cmd[i+1] == '(' {
					return ">("
				}
			}
		case stateSingleQuote:
			if c == '\'' {
				st = stateNormal
			}
		case stateDoubleQuote:
			switch c {
			case '"':
				st = stateNormal
			case '\\':
				if i+1 < len(cmd) {
					switch cmd[i+1] {
					case '\\', '"', '$', '`', '\n':
						i++
					}
				}
			case '$':
				if len(heredocs) > 0 && heredocs[0].quoted {
					// quoted heredoc body: literal
				} else if i+1 < len(cmd) && cmd[i+1] == '(' {
					if i+2 < len(cmd) && cmd[i+2] == '(' {
						continue
					}
					return "$("
				}
			}
		case stateBackslash:
			st = stateNormal
		}
	}
	return ""
}

// isRedirectAmpersand reports whether the '&' at cmd[i] is part of a
// file-descriptor redirect rather than command-chaining or backgrounding.
// Recognised redirect forms (all bash):
//
//	&>file   &>>file        // merge stdout+stderr to a file  ('&' then '>')
//	>&word   N>&M  >&-      // duplicate/close an output fd    ('>' then '&')
//	<&word   N<&M  <&-      // duplicate/close an input fd     ('<' then '&')
//
// In every one of these the token adjacent to '&' is a redirect target (an
// fd number, '-', or a filename) — never an executable — so the '&' cannot
// introduce a second command and is safe to ignore. It deliberately does
// NOT match '&&' (logical-AND chains a command) or a lone backgrounding
// '&' (cmd & next runs next as a separate command); both stay flagged by
// the caller. cmd[i] is assumed to be '&'.
func isRedirectAmpersand(cmd string, i int) bool {
	// &> / &>> : '&' immediately followed by '>'. A following '&' ('&&')
	// is logical-AND, not a redirect, so require the next byte to be '>'.
	if i+1 < len(cmd) && cmd[i+1] == '>' {
		return true
	}
	// >& / <& : '&' immediately preceded by '>' or '<' (an fd-dup redirect
	// such as 2>&1 or 0<&3). An optional leading fd number before the
	// '>'/'<' is irrelevant here — we only need the byte just before '&'.
	if i > 0 && (cmd[i-1] == '>' || cmd[i-1] == '<') {
		return true
	}
	return false
}

func parseHeredocRedirect(cmd string, i int) (delim string, quoted bool, next int, ok bool) {
	if i+1 >= len(cmd) || cmd[i+1] != '<' {
		return "", false, i, false
	}
	j := i + 2
	if j < len(cmd) && cmd[j] == '<' {
		return "", false, i, false // here-string: <<<
	}
	if j < len(cmd) && cmd[j] == '-' {
		j++
	}
	for j < len(cmd) && (cmd[j] == ' ' || cmd[j] == '\t') {
		j++
	}
	if j >= len(cmd) || cmd[j] == '\n' || cmd[j] == '\r' {
		return "", false, i, false
	}

	if cmd[j] == '\'' || cmd[j] == '"' {
		quote := cmd[j]
		start := j + 1
		j = start
		for j < len(cmd) && cmd[j] != quote {
			j++
		}
		if j >= len(cmd) {
			return "", false, i, false
		}
		d := cmd[start:j]
		return d, true, j + 1, d != ""
	}

	start := j
	for j < len(cmd) {
		switch cmd[j] {
		case ' ', '\t', '\n', '\r', ';', '|', '&':
			if j == start {
				return "", false, i, false
			}
			return cmd[start:j], false, j, true
		default:
			j++
		}
	}
	d := cmd[start:j]
	return d, false, j, j > start
}

type state int

const (
	stateNormal state = iota
	stateSingleQuote
	stateDoubleQuote
	stateBackslash
)
