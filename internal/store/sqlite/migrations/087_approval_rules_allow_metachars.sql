-- Per-rule opt-in: when AllowMetachars=1, the shell-hook cheap-block on
-- shell metacharacters (`;|&` + backtick + newlines, see
-- internal/api/hooks_handler.go:147) is bypassed for any approval that
-- matches THIS rule. Without it, the wildcard "allow + audit everything"
-- rule (pattern=*, decision=allow) can never honour its own name for
-- common multi-step Bash invocations like `ssh host 'cmd1 | cmd2'`,
-- `cmd; cmd2`, or `cmd 2>&1` -- the cheap-block fires before the
-- resolver ever sees them.
--
-- This is intentionally narrower than dangerous-mode. dangerous-mode
-- disables EVERY approval gate (secret prompts, MCP banned interpreters,
-- protected-path checks, ...). AllowMetachars only lifts the metachar
-- cheap-block, and only for requests that match a rule the user has
-- explicitly opted into (typically the amber "Allow + audit everything"
-- wildcard, where the UI sets this true). Every other guard layer --
-- protected mcplexer paths, downstream-config registration validation,
-- audit logging -- still applies.

ALTER TABLE approval_rules
    ADD COLUMN allow_metachars INTEGER NOT NULL DEFAULT 0;
