package collect

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// realCursor is the wire form journald actually emits, captured from a live
// systemd 255 host. Tests use it verbatim so a future charset tightening that
// would reject a real cursor fails here instead of in production.
const realCursor = "s=3dbe7ea510f141f0934706313c9f1527;i=6458;b=c3319f37ee6a4725bd901b36e3a4d490;m=60dee2961;t=656c270b5d9a3;x=6460b3d367f90bdb"

func srcJournald() *store.LogSource {
	return &store.LogSource{
		ID: "s2", WorkspaceID: "ws", RemoteHostID: "h1", Name: "ssh",
		Kind: store.LogSourceKindJournald, Selector: "ssh.service",
		ScheduleSpec: "2m", MaxPullBytes: 1 << 20, Enabled: true,
	}
}

// TestPull_JournaldCursorSuppressesFalseDiscontinuity is the regression test
// for the production bug: every 2-minute pull of ssh.service and docker.service
// filed a fresh warn "discontinuity" template while failures stayed at zero.
//
// The cause was a precision mismatch, not a real gap. The cursor is stored to
// the microsecond but --since only accepts whole seconds, so the window was
// truncated to the top of the cursor's second and journald re-returned every
// earlier line sharing it. The tail-hash check only ever compared position 0,
// saw a line that was not the tail, and cried discontinuity.
//
// It was self-sustaining: the monitoring pull's OWN ssh login writes several
// ssh.service lines into a single second, so the cursor's second always held
// more than one line and the tail was never first. It could never settle.
//
// The stdout below is that exact shape — "session opened" is the stored tail at
// .485367, and the truncated window re-returns "Accepted publickey" at .482510,
// 2.857ms EARLIER. With an exclusive --after-cursor this can no longer happen,
// so no discontinuity may be filed.
func TestPull_JournaldCursorSuppressesFalseDiscontinuity(t *testing.T) {
	const tail = "2026-07-16T22:44:04.485367+00:00 host sshd[1]: pam_unix(sshd:session): session opened for user root"
	runner := &fakeRunner{out: "" +
		"2026-07-16T22:44:04.482510+00:00 host sshd[1]: Accepted publickey for root\n" +
		tail + "\n" +
		"2026-07-16T22:46:19.100000+00:00 host sshd[2]: Accepted publickey for root\n" +
		"-- cursor: " + realCursor + "\n",
	}
	m, fs, sink := newFixture(runner)
	src := srcJournald()
	ts := time.Date(2026, 7, 16, 22, 44, 4, 485367000, time.UTC)
	src.CursorTS = &ts
	src.CursorHash = sourceCursorState{
		Version: 2, TailHash: lineHash(tail), JournalCursor: "s=old;i=1",
	}.encode()

	if err := m.pullSource(context.Background(), src); err != nil {
		t.Fatalf("pull: %v", err)
	}

	// The whole point: an exclusive window cannot re-return the tail, so a
	// first-line mismatch is not evidence of anything.
	for _, l := range sink.lines {
		if strings.Contains(l.Text, "discontinuity") || strings.Contains(l.Text, "non-monotonic") {
			t.Fatalf("false continuity signal filed under an exclusive cursor: %q", l.Text)
		}
	}

	// journald's bookkeeping marker must never reach the sink. It carries no
	// leading timestamp, so an unstripped marker would inherit the previous
	// line's and be ingested as a log line.
	for _, l := range sink.lines {
		if strings.Contains(l.Text, "-- cursor:") || strings.Contains(l.Text, realCursor) {
			t.Fatalf("--show-cursor marker ingested as a log line: %q", l.Text)
		}
	}

	// The pull must hand the stored cursor to the runner, not a --since window.
	if runner.gotCursor != "s=old;i=1" {
		t.Fatalf("runner got cursor %q, want the stored one", runner.gotCursor)
	}
	// And the fresh cursor must be persisted, or the next pull repeats this one.
	if got := decodeCursorState(fs.cursorH).JournalCursor; got != realCursor {
		t.Fatalf("journal cursor not advanced: got %q", got)
	}
}

// TestPull_JournaldFirstPullBootstrapsCursor covers the source's first ever
// pull: there is no cursor yet, so the lossy --since window is used and its
// inclusive duplicate tail is still expected and reconciled. The run must
// capture a cursor so the source leaves that window permanently.
func TestPull_JournaldFirstPullBootstrapsCursor(t *testing.T) {
	runner := &fakeRunner{out: "" +
		"2026-07-16T22:44:04.482510+00:00 host sshd[1]: Accepted publickey for root\n" +
		"-- cursor: " + realCursor + "\n",
	}
	m, fs, sink := newFixture(runner)

	if err := m.pullSource(context.Background(), srcJournald()); err != nil {
		t.Fatalf("pull: %v", err)
	}
	if runner.gotCursor != "" {
		t.Fatalf("first pull has no cursor to send, got %q", runner.gotCursor)
	}
	if got := decodeCursorState(fs.cursorH).JournalCursor; got != realCursor {
		t.Fatalf("bootstrap did not capture a cursor: %q", got)
	}
	if len(sink.lines) != 1 {
		t.Fatalf("expected the single real line, got %+v", sink.lines)
	}
	if strings.Contains(sink.lines[0].Text, "cursor") {
		t.Fatalf("marker leaked into the bootstrap window: %q", sink.lines[0].Text)
	}
}

// TestPull_JournaldLegacyCursorMigrationDoesNotSignal covers rollout over an
// existing journald source. Such a row has a timestamp/tail hash but no opaque
// cursor yet, so its first upgraded pull must bootstrap with --since. That
// timestamp has only whole-second precision and cannot prove which line in the
// cursor's second was the old tail; reporting a mismatch would emit one final
// known-false discontinuity during the very migration intended to stop them.
func TestPull_JournaldLegacyCursorMigrationDoesNotSignal(t *testing.T) {
	const tail = "2026-07-16T22:44:04.485367+00:00 host sshd[1]: pam_unix(sshd:session): session opened for user root"
	runner := &fakeRunner{out: "" +
		"2026-07-16T22:44:04.482510+00:00 host sshd[1]: Accepted publickey for root\n" +
		tail + "\n" +
		"-- cursor: " + realCursor + "\n",
	}
	m, fs, sink := newFixture(runner)
	src := srcJournald()
	ts := time.Date(2026, 7, 16, 22, 44, 4, 485367000, time.UTC)
	src.CursorTS = &ts
	// Plain hashes are the pre-opaque-cursor wire format in cursor_hash.
	src.CursorHash = lineHash(tail)

	if err := m.pullSource(context.Background(), src); err != nil {
		t.Fatalf("pull: %v", err)
	}
	if runner.gotCursor != "" {
		t.Fatalf("legacy row must bootstrap without an opaque cursor, got %q", runner.gotCursor)
	}
	for _, line := range sink.lines {
		if strings.Contains(line.Text, "discontinuity") || strings.Contains(line.Text, "non-monotonic") {
			t.Fatalf("legacy migration filed false continuity evidence: %q", line.Text)
		}
	}
	if got := decodeCursorState(fs.cursorH).JournalCursor; got != realCursor {
		t.Fatalf("legacy migration did not persist the opaque cursor: %q", got)
	}
}

// TestPull_JournaldTruncatedAdvancesViaSince: a truncated journald window has
// its --show-cursor marker cut off, so there is no opaque cursor to advance.
// Rather than hold the cursor and re-pull the same over-cap window forever, the
// collector ingests the complete prefix (dropping the possibly-partial last
// line), advances CursorTS past it, and CLEARS the opaque cursor so the next
// pull falls back to a --since window and the source makes progress.
func TestPull_JournaldTruncatedAdvancesViaSince(t *testing.T) {
	runner := &fakeRunner{
		// Three complete lines then a partial 4th (cut mid-line, no newline, no
		// --show-cursor marker) — the realistic shape of a truncated window.
		out: "2026-07-16T22:44:04.482510+00:00 host sshd[1]: line one\n" +
			"2026-07-16T22:44:05.100000+00:00 host sshd[1]: line two\n" +
			"2026-07-16T22:44:06.200000+00:00 host sshd[1]: line three\n" +
			"2026-07-16T22:44:07.300000+00:00 host sshd[1]: partial fo",
		truncated: true,
	}
	m, fs, sink := newFixture(runner)
	src := srcJournald()
	ts := time.Date(2026, 7, 16, 22, 44, 0, 0, time.UTC)
	src.CursorTS = &ts
	src.CursorHash = sourceCursorState{Version: 2, JournalCursor: "s=old;i=1"}.encode()

	if err := m.pullSource(context.Background(), src); err != nil {
		t.Fatalf("pull: %v", err)
	}
	// Opaque cursor cleared so the next pull uses --since (marker was cut off).
	if got := decodeCursorState(fs.cursorH).JournalCursor; got != "" {
		t.Fatalf("truncated journald cursor = %q, want cleared for --since fallback", got)
	}
	// CursorTS advanced to the last COMPLETE line (line three), not the partial.
	wantTS := time.Date(2026, 7, 16, 22, 44, 6, 200000000, time.UTC)
	if !fs.cursorTS.Equal(wantTS) {
		t.Fatalf("cursorTS = %v, want %v (last complete line)", fs.cursorTS, wantTS)
	}
	// The complete prefix was ingested (progress), plus the truncation signal.
	var sawLineThree, sawTruncation, sawPartial bool
	for _, line := range sink.lines {
		switch {
		case strings.Contains(line.Text, "line three"):
			sawLineThree = true
		case strings.Contains(line.Text, "pull truncated"):
			sawTruncation = true
		case strings.Contains(line.Text, "partial fo"):
			sawPartial = true
		}
	}
	if !sawLineThree || !sawTruncation {
		t.Fatalf("expected complete prefix + truncation signal, got %d lines", len(sink.lines))
	}
	if sawPartial {
		t.Fatalf("the possibly-partial last line must not be ingested")
	}
}

// TestSplitJournalCursor covers the marker parser directly, including the
// shapes that must NOT be treated as a marker.
func TestSplitJournalCursor(t *testing.T) {
	t.Run("strips marker and returns cursor", func(t *testing.T) {
		out, cur := splitJournalCursor([]byte("line one\nline two\n-- cursor: " + realCursor + "\n"))
		if cur != realCursor {
			t.Fatalf("cursor: got %q", cur)
		}
		// The newline that terminated the last real line is left in place;
		// splitStream skips the resulting empty trailing field, so only the
		// marker itself must be gone.
		if strings.TrimRight(string(out), "\n") != "line one\nline two" {
			t.Fatalf("stdout: got %q", string(out))
		}
		if strings.Contains(string(out), "cursor") {
			t.Fatalf("marker survived: %q", string(out))
		}
	})

	// The steady state of a quiet unit under --after-cursor: journald still
	// emits the marker for a window with no entries at all.
	t.Run("marker only, no entries", func(t *testing.T) {
		out, cur := splitJournalCursor([]byte("-- cursor: " + realCursor + "\n"))
		if cur != realCursor {
			t.Fatalf("cursor: got %q", cur)
		}
		if strings.TrimSpace(string(out)) != "" {
			t.Fatalf("expected no lines, got %q", string(out))
		}
	})

	// No marker: return stdout untouched and no cursor, so the caller keeps
	// its previous cursor rather than advancing past unparsed data.
	t.Run("absent marker leaves stdout intact", func(t *testing.T) {
		in := "line one\nline two\n"
		out, cur := splitJournalCursor([]byte(in))
		if cur != "" || string(out) != in {
			t.Fatalf("got %q / %q", string(out), cur)
		}
	})

	// A log line that merely mentions the phrase mid-line is evidence, not
	// bookkeeping. Consuming it would silently drop a real line.
	t.Run("mid-line phrase is not a marker", func(t *testing.T) {
		in := "2026-07-16T22:44:04.482510+00:00 host app[1]: replaying -- cursor: s=x from peer\n"
		out, cur := splitJournalCursor([]byte(in))
		if cur != "" {
			t.Fatalf("mid-line phrase consumed as a marker: %q", cur)
		}
		if string(out) != in {
			t.Fatalf("real log line was mangled: %q", string(out))
		}
	})
}
