package tasks

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/don-works/mcplexer/internal/store"
)

func TestSafeTaskProjectionRedactsAndExcludesLocalOnlyFields(t *testing.T) {
	// Assemble credential-shaped fixtures at runtime so repository scanners do
	// not mistake the deliberately fake redaction inputs for live secrets.
	fakeGitHubToken := "ghp_" + "abcdefghijklmnopqrstuvwxyz123456"
	multiSegmentKey := "sk-" + "live-container-secret-abcdefghijklmnopqrstuvwxyz"
	tagsJSON, err := json.Marshal([]string{"incident", fakeGitHubToken})
	if err != nil {
		t.Fatal(err)
	}
	task := &store.Task{
		ID: "task", WorkspaceID: "workspace", Title: "Deploy token " + fakeGitHubToken + " " + multiSegmentKey,
		Description:       "Authorization: Bearer secret-value-that-must-not-leak",
		Meta:              `{"raw_log":"private","api_key":"plaintext"}`,
		TagsJSON:          json.RawMessage(tagsJSON),
		AssigneeSessionID: "local-session", AssigneePeerID: "private-peer",
		OriginPeerID: "origin-peer", HlcAt: "00000000000000000000000000000001",
	}
	event, err := BuildSafeLocalEventForGossip(task, "self-peer", EgressProfileTaskSafeV1)
	if err != nil {
		t.Fatal(err)
	}
	body := string(event.FieldPatchesJSON)
	for _, secret := range []string{"ghp_", multiSegmentKey, "secret-value", "raw_log", "api_key", "local-session", "private-peer", "origin-peer"} {
		if strings.Contains(body, secret) {
			t.Fatalf("safe projection leaked %q: %s", secret, body)
		}
	}
	for _, expected := range []string{"[REDACTED]", "incident"} {
		if !strings.Contains(body, expected) {
			t.Fatalf("safe projection missing %q: %s", expected, body)
		}
	}
}

func TestSafeTaskProjectionRejectsUnknownProfile(t *testing.T) {
	if _, err := BuildSafeLocalEventForGossip(&store.Task{}, "peer", "allow-everything"); err == nil {
		t.Fatal("unknown egress profile was accepted")
	}
}
