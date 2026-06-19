package runner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// recordingAuditor is the in-package fake mirroring the black-box
// fakeAuditor used in runner_test.go. Lives here so output_audit_test
// can stay in the runner package and call dispatchChannel directly.
type recordingAuditor struct {
	mu      sync.Mutex
	records []*store.AuditRecord
}

func (r *recordingAuditor) Record(_ context.Context, rec *store.AuditRecord) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.records = append(r.records, rec)
	return nil
}

func (r *recordingAuditor) snapshot() []*store.AuditRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*store.AuditRecord, len(r.records))
	copy(out, r.records)
	return out
}

func newAuditTestRunner(t *testing.T, a Auditor, client *http.Client) *Runner {
	t.Helper()
	r := &Runner{
		clock:      RealClock{},
		auditor:    a,
		httpClient: client,
	}
	return r
}

func sampleOutputCtxForAudit(client *http.Client) outputContext {
	return outputContext{
		workerID:     "wrk_audit",
		workerName:   "audit-test",
		runID:        "run_aud",
		status:       StatusSuccess,
		output:       "result",
		startedAt:    time.Date(2026, 5, 21, 9, 0, 0, 0, time.UTC),
		finishedAt:   time.Date(2026, 5, 21, 9, 0, 1, 0, time.UTC),
		durationMS:   1000,
		inputTokens:  10,
		outputTokens: 5,
		costUSD:      0.0001,
		httpClient:   client,
	}
}

// expectedHash returns the destination_hash the runner should produce
// for the given input string — kept as a separate helper so test
// failures point to whichever side is wrong.
func expectedHash(destination string) string {
	if destination == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(destination))
	return hex.EncodeToString(sum[:4])
}

// assertNoPlaintext is the regression guard for the destination-leak
// vector: every field of every audit record must NOT contain the
// raw destination string. If we ever accidentally start logging the
// URL, this fires.
func assertNoPlaintext(t *testing.T, recs []*store.AuditRecord, plaintext string) {
	t.Helper()
	if plaintext == "" {
		return
	}
	for i, r := range recs {
		blob, _ := json.Marshal(r)
		if strings.Contains(string(blob), plaintext) {
			t.Errorf("audit record %d leaks plaintext destination %q: %s", i, plaintext, blob)
		}
	}
}

func findOutputAudit(t *testing.T, a *recordingAuditor) *store.AuditRecord {
	t.Helper()
	for _, r := range a.snapshot() {
		if r.ToolName == auditEventOutputEmitted {
			return r
		}
	}
	t.Fatalf("no worker_output.emitted record found")
	return nil
}

func TestDestinationHash_StableAndShort(t *testing.T) {
	t.Parallel()
	url := "https://hooks.example.com/leak-vector"
	got := destinationHash(url)
	if len(got) != 8 {
		t.Fatalf("len = %d, want 8 hex chars", len(got))
	}
	// Sanity: same input ⇒ same hash; different input ⇒ different hash.
	if destinationHash(url) != got {
		t.Errorf("hash is non-deterministic")
	}
	if destinationHash(url+"x") == got {
		t.Errorf("hash collision on near-input")
	}
	if destinationHash("") != "" {
		t.Errorf("empty input must yield empty hash, got %q", destinationHash(""))
	}
}

func TestDispatchChannel_Webhook_EmitsDestinationHash(t *testing.T) {
	t.Parallel()
	plantedURL := "https://hooks.example.com/webhook-secret"
	client := mockClient(func(_ *http.Request) (*http.Response, error) {
		return statusResponse(200, ""), nil
	})
	a := &recordingAuditor{}
	r := newAuditTestRunner(t, a, client)

	ch := outputChannel{Type: "webhook", URL: plantedURL}
	octx := sampleOutputCtxForAudit(client)
	r.dispatchChannel(context.Background(), octx, ch)

	rec := findOutputAudit(t, a)
	p := decodeOutputAuditParams(t, rec)
	gotHash, _ := p["destination_hash"].(string)
	if gotHash != expectedHash(plantedURL) {
		t.Errorf("destination_hash = %q, want %q", gotHash, expectedHash(plantedURL))
	}
	if p["channel_type"] != "webhook" {
		t.Errorf("channel_type = %v", p["channel_type"])
	}
	if rec.Status != "ok" {
		t.Errorf("Status = %q, want ok", rec.Status)
	}
	assertNoPlaintext(t, a.snapshot(), plantedURL)
}

func TestDispatchChannel_SlackWebhook_PrefersChannelName(t *testing.T) {
	t.Parallel()
	plantedURL := "https://hooks.slack.com/T000/B000/secret-token"
	plantedChan := "#workers-alerts"
	client := mockClient(func(_ *http.Request) (*http.Response, error) {
		return statusResponse(200, ""), nil
	})
	a := &recordingAuditor{}
	r := newAuditTestRunner(t, a, client)

	ch := outputChannel{Type: "slack_webhook", URL: plantedURL, Channel: plantedChan}
	r.dispatchChannel(context.Background(), sampleOutputCtxForAudit(client), ch)

	rec := findOutputAudit(t, a)
	p := decodeOutputAuditParams(t, rec)
	gotHash, _ := p["destination_hash"].(string)
	if gotHash != expectedHash(plantedChan) {
		t.Errorf("destination_hash = %q, want hash of channel name %q", gotHash, plantedChan)
	}
	// The URL must not leak — Slack incoming-webhook URLs contain
	// the team/bot secret and a plaintext copy in audit would be a
	// catastrophic regression.
	assertNoPlaintext(t, a.snapshot(), plantedURL)
	assertNoPlaintext(t, a.snapshot(), plantedChan)
}

func TestDispatchChannel_ClickUp_HashesListID(t *testing.T) {
	t.Parallel()
	plantedList := "list_9001"
	client := mockClient(func(_ *http.Request) (*http.Response, error) {
		return statusResponse(200, "{}"), nil
	})
	a := &recordingAuditor{}
	r := newAuditTestRunner(t, a, client)
	octx := sampleOutputCtxForAudit(client)
	octx.secrets = &constSecrets{value: "tok"}
	// Empty SecretScopeID will cause emit to error; that's fine — the
	// audit row still must carry destination_hash. We're testing that
	// the hash computation is independent of the channel's success.
	ch := outputChannel{Type: "clickup_task", ListID: plantedList, SecretScopeID: "scope"}
	r.dispatchChannel(context.Background(), octx, ch)

	rec := findOutputAudit(t, a)
	p := decodeOutputAuditParams(t, rec)
	gotHash, _ := p["destination_hash"].(string)
	if gotHash != expectedHash(plantedList) {
		t.Errorf("destination_hash = %q, want %q", gotHash, expectedHash(plantedList))
	}
	assertNoPlaintext(t, a.snapshot(), plantedList)
}

func TestDispatchChannel_GitHub_HashesOwnerRepo(t *testing.T) {
	t.Parallel()
	plantedRepo := "example/private-mirror"
	client := mockClient(func(_ *http.Request) (*http.Response, error) {
		return statusResponse(201, "{}"), nil
	})
	a := &recordingAuditor{}
	r := newAuditTestRunner(t, a, client)
	octx := sampleOutputCtxForAudit(client)
	octx.secrets = &constSecrets{value: "tok"}

	ch := outputChannel{Type: "github_issue", Repo: plantedRepo, SecretScopeID: "scope"}
	r.dispatchChannel(context.Background(), octx, ch)

	rec := findOutputAudit(t, a)
	p := decodeOutputAuditParams(t, rec)
	gotHash, _ := p["destination_hash"].(string)
	if gotHash != expectedHash(plantedRepo) {
		t.Errorf("destination_hash = %q, want %q", gotHash, expectedHash(plantedRepo))
	}
	assertNoPlaintext(t, a.snapshot(), plantedRepo)
}

func TestDispatchChannel_File_EmptyDestinationHash(t *testing.T) {
	t.Parallel()
	// File channel has no addressable remote → destination_hash empty.
	// This is the documented "no destination known at emit time"
	// signal; tests assert it explicitly so a future refactor that
	// accidentally hashes the file path doesn't sneak by.
	a := &recordingAuditor{}
	r := newAuditTestRunner(t, a, nil)
	r.outputsDir = "" // forces emit error, but we only care about audit
	ch := outputChannel{Type: "file", Path: "out.txt"}
	r.dispatchChannel(context.Background(), sampleOutputCtxForAudit(nil), ch)
	rec := findOutputAudit(t, a)
	p := decodeOutputAuditParams(t, rec)
	if got, _ := p["destination_hash"].(string); got != "" {
		t.Errorf("destination_hash = %q for file channel, want empty", got)
	}
}

func decodeOutputAuditParams(t *testing.T, rec *store.AuditRecord) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(rec.ParamsRedacted, &m); err != nil {
		t.Fatalf("decode params: %v / %s", err, rec.ParamsRedacted)
	}
	return m
}

// constSecrets returns a fixed value for every key — enough to satisfy
// the secret-resolver in the ClickUp/GitHub paths so we can drive the
// audit-emission path without standing up a real secret store.
type constSecrets struct{ value string }

func (c *constSecrets) Get(_ context.Context, _, _ string) ([]byte, error) {
	return []byte(c.value), nil
}
