package runner

import (
	"context"
	"testing"
)

// capturingMesh records every MeshOutbound it's handed so tests can assert
// what the runner emitted.
type capturingMesh struct{ sent []MeshOutbound }

func (m *capturingMesh) Send(_ context.Context, msg MeshOutbound) (string, error) {
	m.sent = append(m.sent, msg)
	return "mid", nil
}

// TestEmitOutputsEmptyTextResolvesReplyPlaceholder covers the stuck-bubble
// guard: when the model produces no text (e.g. an opencode truncation the
// adapter retry couldn't recover), a conversational reply channel (mesh +
// reply_to_trigger) must still emit a visible fallback threaded to the
// trigger, so the user's "💭" placeholder resolves instead of hanging.
func TestEmitOutputsEmptyTextResolvesReplyPlaceholder(t *testing.T) {
	mesh := &capturingMesh{}
	r := &Runner{clock: RealClock{}, auditor: &recordingAuditor{}, mesh: mesh}
	octx := outputContext{
		workerID:         "w",
		runID:            "r",
		status:           StatusSuccess,
		output:           "", // model produced nothing usable
		triggerMessageID: "src-123",
		mesh:             mesh,
	}
	channels := `[{"type":"mesh","reply_to_trigger":true,"notify_user":true}]`

	ids := r.emitOutputs(context.Background(), octx, channels)

	if len(mesh.sent) != 1 {
		t.Fatalf("expected 1 fallback mesh emission, got %d", len(mesh.sent))
	}
	got := mesh.sent[0]
	if got.Content != emptyReplyFallbackText {
		t.Errorf("fallback content = %q, want the empty-reply fallback", got.Content)
	}
	if got.ReplyTo != "src-123" {
		t.Errorf("fallback ReplyTo = %q, want src-123 (must thread to the trigger)", got.ReplyTo)
	}
	if !got.NotifyUser {
		t.Error("fallback must set NotifyUser so it actually reaches the user")
	}
	if len(ids) != 1 {
		t.Errorf("expected 1 mesh id returned, got %d", len(ids))
	}
}

// TestEmitOutputsEmptyTextSilentForNonReplyChannels ensures the fallback is
// surgical: an empty run on a worker WITHOUT a conversational reply channel
// (e.g. a file/webhook sink) must emit nothing — no junk "couldn't reply"
// artifact written to a file or posted to a webhook.
func TestEmitOutputsEmptyTextSilentForNonReplyChannels(t *testing.T) {
	mesh := &capturingMesh{}
	r := &Runner{clock: RealClock{}, auditor: &recordingAuditor{}, mesh: mesh}
	octx := outputContext{workerID: "w", runID: "r", status: StatusSuccess, output: "", mesh: mesh}
	channels := `[{"type":"file","path":"/tmp/should-not-write","mode":"append"}]`

	ids := r.emitOutputs(context.Background(), octx, channels)

	if len(ids) != 0 || len(mesh.sent) != 0 {
		t.Errorf("empty output with no reply channel must emit nothing; ids=%d mesh=%d", len(ids), len(mesh.sent))
	}
}

// TestEmitOutputsEmptyTextNoTriggerStaysSilent ensures a manual run-now /
// schedule run (a reply channel exists, but there's no triggering message
// to reply to) does NOT post a spurious "couldn't reply" notice to the chat.
func TestEmitOutputsEmptyTextNoTriggerStaysSilent(t *testing.T) {
	mesh := &capturingMesh{}
	r := &Runner{clock: RealClock{}, auditor: &recordingAuditor{}, mesh: mesh}
	octx := outputContext{
		workerID: "w",
		runID:    "r",
		status:   StatusSuccess,
		output:   "",
		// triggerMessageID intentionally empty (manual / schedule run)
		mesh: mesh,
	}
	channels := `[{"type":"mesh","reply_to_trigger":true,"notify_user":true}]`

	ids := r.emitOutputs(context.Background(), octx, channels)

	if len(ids) != 0 || len(mesh.sent) != 0 {
		t.Errorf("no trigger → no fallback should be emitted; ids=%d mesh=%d", len(ids), len(mesh.sent))
	}
}

// TestEmitOutputsNonEmptyUnchanged guards that the restructured early-return
// didn't regress the normal path: a non-empty reply still dispatches verbatim.
func TestEmitOutputsNonEmptyUnchanged(t *testing.T) {
	mesh := &capturingMesh{}
	r := &Runner{clock: RealClock{}, auditor: &recordingAuditor{}, mesh: mesh}
	octx := outputContext{
		workerID:         "w",
		runID:            "r",
		status:           StatusSuccess,
		output:           "real answer",
		triggerMessageID: "src-9",
		mesh:             mesh,
	}
	channels := `[{"type":"mesh","reply_to_trigger":true,"notify_user":true}]`

	r.emitOutputs(context.Background(), octx, channels)

	if len(mesh.sent) != 1 {
		t.Fatalf("expected 1 emission, got %d", len(mesh.sent))
	}
	if mesh.sent[0].Content != "real answer" {
		t.Errorf("content = %q, want verbatim 'real answer'", mesh.sent[0].Content)
	}
}
