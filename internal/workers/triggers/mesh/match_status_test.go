package mesh

import (
	"testing"

	"github.com/don-works/mcplexer/internal/store"
)

// statusEvtTags renders the tag list the tasks event emitter stamps on a
// task_event:status_changed message for the given transition.
func statusEvtTags(from, to string) string {
	return "task_event:status_changed,task_id:t1,workspace:ws1," +
		"status_from:" + from + ",status_to:" + to
}

// TestStatusTransition_Parse confirms status_from:/status_to: tags are
// extracted (and absence yields empties).
func TestStatusTransition_Parse(t *testing.T) {
	from, to := statusTransition(statusEvtTags("doing", "review"))
	if from != "doing" || to != "review" {
		t.Fatalf("parse = (%q,%q), want (doing,review)", from, to)
	}
	// A status with an internal space survives (splitTags only splits on
	// comma, TrimSpace only trims the token edges).
	_, to2 := statusTransition("task_event:status_changed,status_to:in review")
	if to2 != "in review" {
		t.Fatalf("space-bearing status = %q, want 'in review'", to2)
	}
	// Non-status message → no transition tags.
	f3, t3 := statusTransition("finding,sev:high")
	if f3 != "" || t3 != "" {
		t.Fatalf("non-status parse = (%q,%q), want empties", f3, t3)
	}
}

// TestMatchesStatusTransition tabulates the AND'd transition gate.
func TestMatchesStatusTransition(t *testing.T) {
	cases := []struct {
		name      string
		from, to  string // trigger constraints ("" = any)
		msgFrom   string
		msgTo     string
		isStatus  bool // false → a non-status message (no transition tags)
		wantMatch bool
	}{
		{"no constraint matches anything", "", "", "doing", "review", true, true},
		{"no constraint matches non-status msg", "", "", "", "", false, true},
		{"to=review fires on doing→review", "", "review", "doing", "review", true, true},
		{"to=review skips doing→done", "", "review", "doing", "done", true, false},
		{"to=review skips non-status msg", "", "review", "", "", false, false},
		{"from+to both must match", "planned", "ready", "planned", "ready", true, true},
		{"from mismatch rejects", "planned", "ready", "open", "ready", true, false},
		{"from=any to=ready fires", "", "ready", "open", "ready", true, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			trig := &store.WorkerMeshTrigger{
				StatusFromMatch: c.from,
				StatusToMatch:   c.to,
			}
			var tags string
			if c.isStatus {
				tags = statusEvtTags(c.msgFrom, c.msgTo)
			} else {
				tags = "finding,sev:high"
			}
			msg := &store.MeshMessage{Kind: "task_event", Tags: tags}
			if got := matchesStatusTransition(trig, msg); got != c.wantMatch {
				t.Fatalf("matchesStatusTransition = %v, want %v", got, c.wantMatch)
			}
		})
	}
}

// TestMatchesStatusTransition_RejectsSpoofedTag is the regression for the
// tag-spoof bug: a non-status event (e.g. task_event:created) that carries
// a "status_to:review" tag — as it would if a task were literally labelled
// "status_to:review" by a user — must NOT satisfy a transition trigger,
// because the canonical task_event:status_changed marker is absent.
func TestMatchesStatusTransition_RejectsSpoofedTag(t *testing.T) {
	trig := &store.WorkerMeshTrigger{StatusToMatch: "review"}
	// A created-event message with an injected status_to:review tag but NO
	// status_changed marker (the spoof).
	spoof := &store.MeshMessage{
		Kind: "task_event",
		Tags: "task_event:created,task_id:t1,workspace:ws1,status_to:review",
	}
	if matchesStatusTransition(trig, spoof) {
		t.Fatal("spoofed status_to: tag on a non-status_changed event must NOT match a transition trigger")
	}
}

// TestMatchesTrigger_StatusANDedWithKind proves the status gate is AND'd
// with the rest of the match set — the exact thing the OR-semantics
// TagMatch could not express (status_changed AND to=review).
func TestMatchesTrigger_StatusANDedWithKind(t *testing.T) {
	trig := &store.WorkerMeshTrigger{
		KindMatch:     "task_event",
		StatusToMatch: "review",
	}
	land := &store.MeshMessage{Kind: "task_event", Tags: statusEvtTags("doing", "review")}
	if !matchesTrigger(trig, land) {
		t.Fatal("epic doing→review should fire the lander trigger")
	}
	// Same kind, wrong transition → no fire.
	other := &store.MeshMessage{Kind: "task_event", Tags: statusEvtTags("open", "doing")}
	if matchesTrigger(trig, other) {
		t.Fatal("open→doing must not fire a to=review trigger")
	}
	// Right transition, wrong kind → no fire (kind gate still applies).
	wrongKind := &store.MeshMessage{Kind: "finding", Tags: statusEvtTags("doing", "review")}
	if matchesTrigger(trig, wrongKind) {
		t.Fatal("kind mismatch must veto even on a matching transition")
	}
}
