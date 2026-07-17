// pull.go — one source's bounded, cursored, redacted pull.
package collect

import (
	"context"
	"fmt"
	"slices"
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
	if err := validateCollectedKind(src.Kind); err != nil {
		return err
	}
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
	result, pullErr := m.executePull(ctx, host, cred, src)
	if err := m.persistObservedPin(ctx, host.ID, result.NewPin); err != nil {
		return err
	}
	if pullErr != nil {
		return pullErr
	}
	return m.ingestPull(ctx, src, host, result)
}

func validateCollectedKind(kind string) error {
	switch kind {
	case store.LogSourceKindDocker, store.LogSourceKindCompose,
		store.LogSourceKindSwarm, store.LogSourceKindJournald:
		return nil
	default:
		return fmt.Errorf("logwatch: source kind %q is not collected (file kind needs byte-offset cursoring — tracked in M6)", kind)
	}
}

func (m *Manager) executePull(
	ctx context.Context, host *store.RemoteHost, cred sshx.Credential, src *store.LogSource,
) (PullResult, error) {
	since := time.Time{}
	if src.CursorTS != nil {
		since = *src.CursorTS
		// Docker's --since boundary is inclusive, including when Compose
		// aggregates several containers. Advance one nanosecond so a steady
		// pull is exclusive without losing any representable Docker timestamp.
		// This also removes any dependence on the stored tail appearing first:
		// Compose can return the exact boundary later in an otherwise valid,
		// cross-container aggregation.
		if usesExclusiveTimestampWindow(src) {
			since = since.Add(time.Nanosecond)
		}
	}
	// decodeCursorState is cheap and the state is re-decoded in ingestPull;
	// reading it here keeps the command builder's window choice driven by the
	// same persisted row the ingest path reconciles against.
	cursor := decodeCursorState(src.CursorHash).JournalCursor
	pullCtx, cancel := context.WithTimeout(ctx, pullTimeout)
	defer cancel()
	return m.runner.Pull(pullCtx, host, cred, src, since, cursor)
}

func (m *Manager) persistObservedPin(ctx context.Context, hostID, pin string) error {
	if pin == "" {
		return nil
	}
	if err := m.store.SetRemoteHostPin(ctx, hostID, pin); err != nil {
		return fmt.Errorf("logwatch: persist TOFU pin: %w", err)
	}
	return nil
}

func (m *Manager) ingestPull(
	ctx context.Context, src *store.LogSource, host *store.RemoteHost, result PullResult,
) error {
	state := decodeCursorState(src.CursorHash)
	stdout := result.Stdout
	// journald's --show-cursor marker is bookkeeping, not evidence; it must
	// come off before parsing (see splitJournalCursor).
	var newJournalCursor string
	if src.Kind == store.LogSourceKindJournald {
		stdout, newJournalCursor = splitJournalCursor(stdout)
	}
	lines, firstRaw, lastRaw := parseLogLines(stdout, result.Stderr)
	// Every collected steady-state kind now uses an exclusive window:
	// journald has --after-cursor; Docker/Compose/Swarm receive cursorTS+1ns.
	// An exclusive window cannot re-return the stored tail, while Compose's
	// cross-container output is not guaranteed to put an inclusive tail first
	// anyway. The bootstrap path has no prior cursor, so reconciliation is a
	// no-op there. Keep the old check only for a future non-exclusive kind.
	discontinuity := false
	if !skipsTailReconciliation(src) {
		lines, discontinuity = reconcileCursor(lines, firstRaw, src, state)
	}
	var signals []Line
	if result.Docker != nil {
		var lifecycle []Line
		lifecycle, state = m.lifecycleLines(src, state, result.Docker)
		signals = append(signals, lifecycle...)
		var portLines []Line
		portLines, state.PortState = m.portExposureLines(host, result.Docker, state.PortState)
		signals = append(signals, portLines...)
	}
	// Continuity is independent evidence. Even when Docker separately proves a
	// lifecycle transition, preserving this observation exposes interleaved or
	// duplicate logging rather than laundering it into a restart diagnosis.
	if discontinuity {
		signals = append(signals, cursorDiscontinuityLine(src, firstRaw, m.now().UTC()))
	}
	truncationIncidentID := m.truncationEpisode(src.ID, result.Truncated)
	if result.Truncated {
		now := m.now().UTC()
		signals = append(signals, Line{TS: now, Notify: true,
			IncidentID: truncationIncidentID,
			Text:       fmt.Sprintf("logwatch: pull truncated at %d bytes — window incomplete and untrustworthy; silence is not evidence of health", src.MaxPullBytes)})
		// Do not mine an arbitrary prefix as application evidence. The cursor is
		// deliberately held, so retaining these rows would duplicate them on
		// every retry and manufacture counts from an untrustworthy window.
		lines = nil
	}
	// A truncated window is untrustworthy, so the cursor is held and the
	// window re-covered. Advancing the journal cursor here would step past
	// the bytes the truncation discarded and lose them permanently.
	if !result.Truncated && newJournalCursor != "" {
		state.JournalCursor = newJournalCursor
	}
	lines = append(signals, lines...)
	if len(lines) == 0 {
		return m.persistCursorState(ctx, src, state, lastRaw, result.Truncated)
	}
	if err := m.sink.Ingest(ctx, src, host, lines); err != nil {
		return fmt.Errorf("logwatch: ingest: %w", err)
	}
	return m.persistCursorState(ctx, src, state, lastRaw, result.Truncated)
}

func (m *Manager) persistCursorState(
	ctx context.Context, src *store.LogSource, state sourceCursorState,
	lastRaw rawLine, truncated bool,
) error {
	cursorTS := time.Time{}
	if src.CursorTS != nil {
		cursorTS = *src.CursorTS
	}
	if !truncated && !lastRaw.ts.IsZero() {
		cursorTS = lastRaw.ts
		state.TailHash = lineHash(lastRaw.raw)
	}
	// A zero cursorTS means this source has never yielded a timestamped line,
	// so there is no window to advance past. The journald cursor is left
	// unpersisted in that case rather than paired with a zero timestamp: the
	// next pull simply re-issues its --since bootstrap, which is what happens
	// today, and the cursor is captured on the first pull that returns a line.
	if cursorTS.IsZero() {
		return nil
	}
	return m.store.UpdateLogSourceCursor(ctx, src.ID, cursorTS, state.encode())
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

// parsedLine is one split-but-not-yet-redacted line from a single
// stream, kept alongside its full raw text for cursor hashing.
type parsedLine struct {
	ts   time.Time
	text string // raw line minus its leading timestamp token
	raw  string // full raw line, pre-redaction
}

// splitStream splits one stream's timestamped output (docker/compose
// --timestamps, journald short-iso-precise) into parsed lines. Each
// raw line begins with a timestamp token in one of several layouts;
// malformed lines keep their raw text and inherit the PREVIOUS LINE
// IN THIS STREAM's timestamp — stdout and stderr are timestamped
// independently by the remote tool and must not borrow each other's
// continuation timestamps.
func splitStream(out []byte) []parsedLine {
	var lines []parsedLine
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
		lines = append(lines, parsedLine{ts: ts, text: text, raw: raw})
	}
	return lines
}

// mergeByTS stably interleaves two independently-timestamped streams
// by timestamp, ties favoring a. Docker preserves stream separation,
// so app-stdout and app-stderr carry independent timestamps with no
// cross-stream ordering guarantee on the wire; merging by timestamp
// keeps the combined sequence — and therefore the cursor's
// first/last-line semantics — chronologically meaningful.
func mergeByTS(a, b []parsedLine) []parsedLine {
	merged := make([]parsedLine, 0, len(a)+len(b))
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		if a[i].ts.After(b[j].ts) {
			merged = append(merged, b[j])
			j++
		} else {
			merged = append(merged, a[i])
			i++
		}
	}
	merged = append(merged, a[i:]...)
	merged = append(merged, b[j:]...)
	return merged
}

// parseLogLines merges stdout and stderr into one chronological,
// redacted line sequence. Returns the parsed lines plus the first and
// last raw lines (pre-redaction) for cursor hashing.
func parseLogLines(stdout, stderr []byte) ([]Line, rawLine, rawLine) {
	stdoutLines := splitStream(stdout)
	stderrLines := splitStream(stderr)
	// Docker Compose aggregates several container streams onto stdout and can
	// emit service groups out of timestamp order. Sort each captured stream
	// before the stable stdout/stderr merge so the persisted cursor is the
	// maximum observed timestamp rather than whichever service printed last.
	byTimestamp := func(a, b parsedLine) int { return a.ts.Compare(b.ts) }
	// Stable ordering is required for multiline entries: splitStream assigns a
	// continuation the timestamp of its parent, and equal-timestamp frames must
	// retain their wire order rather than being sorted by message text.
	slices.SortStableFunc(stdoutLines, byTimestamp)
	slices.SortStableFunc(stderrLines, byTimestamp)
	merged := mergeByTS(stdoutLines, stderrLines)
	if len(merged) == 0 {
		return nil, rawLine{}, rawLine{}
	}
	lines := make([]Line, len(merged))
	for i, p := range merged {
		lines[i] = Line{TS: p.ts, Text: audit.RedactString(p.text, nil)}
	}
	first := rawLine{ts: merged[0].ts, raw: merged[0].raw}
	last := rawLine{ts: merged[len(merged)-1].ts, raw: merged[len(merged)-1].raw}
	return lines, first, last
}

// usesExclusiveTimestampWindow reports whether the remote CLI accepts a
// nanosecond RFC3339 --since boundary. Journald has its own opaque cursor and
// is deliberately excluded.
func usesExclusiveTimestampWindow(src *store.LogSource) bool {
	if src.CursorTS == nil {
		return false
	}
	switch src.Kind {
	case store.LogSourceKindDocker, store.LogSourceKindCompose, store.LogSourceKindSwarm:
		return true
	default:
		return false
	}
}

func skipsTailReconciliation(src *store.LogSource) bool {
	return src.Kind == store.LogSourceKindJournald || usesExclusiveTimestampWindow(src)
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

// sshRunner is the production Runner: fixed per-kind command builder +
// bounded sshx run.
type sshRunner struct{}

func (sshRunner) Pull(ctx context.Context, host *store.RemoteHost, cred sshx.Credential, src *store.LogSource, since time.Time, cursor string) (PullResult, error) {
	cmd, err := sshx.CommandForSource(src, since, cursor)
	if err != nil {
		return PullResult{}, err
	}
	client, err := sshx.Dial(ctx, host, cred)
	if err != nil {
		return PullResult{}, err
	}
	defer client.Close()
	observation := collectDockerObservation(ctx, client, src, since)
	res, err := client.Run(ctx, cmd, src.MaxPullBytes)
	res.NewPin = client.NewPin()
	return PullResult{Result: res, Docker: observation}, err
}
