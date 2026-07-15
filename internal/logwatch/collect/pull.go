// pull.go — one source's bounded, cursored, redacted pull.
package collect

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/audit"
	"github.com/don-works/mcplexer/internal/logwatch/sshx"
	"github.com/don-works/mcplexer/internal/store"
)

// pullSource dials the host, runs the fixed docker-logs command since
// the cursor, redacts, detects discontinuity, ingests, and advances
// the cursor. Any error leaves the cursor untouched so the next pull
// re-covers the window.
func (m *Manager) pullSource(ctx context.Context, src *store.LogSource) error {
	switch src.Kind {
	case store.LogSourceKindDocker, store.LogSourceKindCompose, store.LogSourceKindSwarm, store.LogSourceKindJournald:
		// collected kinds
	default:
		return fmt.Errorf("logwatch: source kind %q is not collected (file kind needs byte-offset cursoring — tracked in M6)", src.Kind)
	}
	// Dial-time re-validation, defence in depth (ADR 0007 §1).
	if err := store.ValidateSelector(src.Selector); err != nil {
		return err
	}
	host, err := m.store.GetRemoteHost(ctx, src.RemoteHostID)
	if err != nil {
		return fmt.Errorf("logwatch: host for source %s: %w", src.Name, err)
	}
	if !host.Enabled {
		return nil
	}
	cred, err := m.credential(ctx, host)
	if err != nil {
		return err
	}

	since := time.Time{}
	if src.CursorTS != nil {
		since = *src.CursorTS
	}
	pctx, cancel := context.WithTimeout(ctx, pullTimeout)
	defer cancel()
	res, err := m.runner.Pull(pctx, host, cred, src, since)
	if res.NewPin != "" {
		// TOFU: persist the first-seen fingerprint even when the read
		// itself failed — the identity observation stands.
		if perr := m.store.SetRemoteHostPin(ctx, host.ID, res.NewPin); perr != nil {
			return fmt.Errorf("logwatch: persist TOFU pin: %w", perr)
		}
	}
	if err != nil {
		return err
	}

	lines, firstRaw, lastRaw := parseLogLines(res.Output)
	lines, discontinuity := reconcileCursor(lines, firstRaw, src)
	if discontinuity {
		lines = append([]Line{{TS: m.now().UTC(),
			Text: "logwatch: source discontinuity — container/service restarted, recreated, or logs rotated"}}, lines...)
	}
	if res.Truncated {
		lines = append(lines, Line{TS: m.now().UTC(),
			Text: fmt.Sprintf("logwatch: pull truncated at %d bytes — window incomplete, raise max_pull_bytes or shorten schedule_spec", src.MaxPullBytes)})
	}
	if len(lines) == 0 {
		return nil
	}
	if err := m.sink.Ingest(ctx, src, host, lines); err != nil {
		return fmt.Errorf("logwatch: ingest: %w", err)
	}
	if lastRaw.ts.IsZero() {
		return nil
	}
	return m.store.UpdateLogSourceCursor(ctx, src.ID, lastRaw.ts, lineHash(lastRaw.raw))
}

// credential resolves the host's auth scope into dial material. PEM
// bytes exist only inside this call chain (ADR 0007 §2).
func (m *Manager) credential(ctx context.Context, host *store.RemoteHost) (sshx.Credential, error) {
	scope, err := m.store.GetAuthScope(ctx, host.AuthScopeID)
	if err != nil {
		return sshx.Credential{}, fmt.Errorf("logwatch: auth scope for host %s: %w", host.Name, err)
	}
	switch scope.Type {
	case sshx.AuthScopeTypeSSHKey:
		pem, err := m.secrets.Get(ctx, scope.ID, sshx.SecretKeyPrivateKey)
		if err != nil {
			return sshx.Credential{}, fmt.Errorf("logwatch: read ssh key: %w", err)
		}
		return sshx.Credential{PrivateKeyPEM: pem}, nil
	case sshx.AuthScopeTypeSSHAgent:
		sock, err := m.secrets.Get(ctx, scope.ID, sshx.SecretKeySocketPath)
		if err != nil {
			sock = nil // optional — fall back to SSH_AUTH_SOCK
		}
		return sshx.Credential{AgentSocket: strings.TrimSpace(string(sock))}, nil
	default:
		return sshx.Credential{}, fmt.Errorf("logwatch: auth scope %s has type %q; monitoring hosts need ssh_key or ssh_agent", scope.ID, scope.Type)
	}
}

type rawLine struct {
	ts  time.Time
	raw string
}

// parseLogLines splits timestamped log output (docker/compose
// --timestamps, journald short-iso-precise) into redacted Lines. Each
// raw line begins with a timestamp token in one of several layouts;
// malformed lines keep their raw text and inherit the previous line's
// timestamp. Returns the parsed lines plus the first and last raw
// lines (pre-redaction) for cursor hashing.
func parseLogLines(out []byte) ([]Line, rawLine, rawLine) {
	var lines []Line
	var first, last rawLine
	var prevTS time.Time
	for raw := range strings.SplitSeq(string(out), "\n") {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		ts, text := splitLeadingTimestamp(raw)
		if ts.IsZero() {
			ts = prevTS
			text = raw
		}
		prevTS = ts
		lines = append(lines, Line{TS: ts, Text: audit.RedactString(text, nil)})
		if first.raw == "" {
			first = rawLine{ts: ts, raw: raw}
		}
		last = rawLine{ts: ts, raw: raw}
	}
	return lines, first, last
}

// tsLayouts are the leading-timestamp formats the collector accepts,
// tried in order: docker/compose RFC3339Nano+Z, RFC3339, and journald
// short-iso-precise (space-separated date/time, numeric zone).
var tsLayouts = []string{
	time.RFC3339Nano,
	time.RFC3339,
	"2006-01-02T15:04:05.000000-0700",
	"2006-01-02T15:04:05-0700",
}

func splitLeadingTimestamp(raw string) (time.Time, string) {
	sp := strings.IndexByte(raw, ' ')
	if sp <= 0 {
		return time.Time{}, raw
	}
	for _, layout := range tsLayouts {
		if ts, err := time.Parse(layout, raw[:sp]); err == nil {
			return ts.UTC(), raw[sp+1:]
		}
	}
	return time.Time{}, raw
}

// reconcileCursor implements continuity checking: the pull requests
// --since <cursor_ts> (inclusive), so a continuous stream re-returns
// the previous tail line first. Hash match → drop the duplicate and
// carry on; anything else with a recorded cursor → discontinuity
// (restart / recreation / rotation), which is itself signal.
func reconcileCursor(lines []Line, firstRaw rawLine, src *store.LogSource) ([]Line, bool) {
	if src.CursorTS == nil || src.CursorHash == "" || len(lines) == 0 {
		return lines, false
	}
	if lineHash(firstRaw.raw) == src.CursorHash {
		return lines[1:], false
	}
	return lines, true
}

func lineHash(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:8])
}

// sshRunner is the production Runner: fixed per-kind command builder +
// bounded sshx run.
type sshRunner struct{}

func (sshRunner) Pull(ctx context.Context, host *store.RemoteHost, cred sshx.Credential, src *store.LogSource, since time.Time) (sshx.Result, error) {
	cmd, err := sshx.CommandForSource(src, since)
	if err != nil {
		return sshx.Result{}, err
	}
	client, err := sshx.Dial(ctx, host, cred)
	if err != nil {
		return sshx.Result{}, err
	}
	defer client.Close()
	res, err := client.Run(ctx, cmd, src.MaxPullBytes)
	res.NewPin = client.NewPin()
	return res, err
}
