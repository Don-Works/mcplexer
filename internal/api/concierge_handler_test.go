// concierge_handler_test.go — HTTP-level tests for the concierge A/B
// leaderboard and lesson-pin REST surfaces. Spins up a real
// sqlite-backed concierge.Service + memory.Service so each test
// exercises the same code path the integration harness hits.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/don-works/mcplexer/internal/concierge"
	"github.com/don-works/mcplexer/internal/memory"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

// newConciergeTestServer wires a sqlite-backed concierge.Service +
// memory.Service into an httptest.Server. Returns the server + the
// underlying handles so tests can seed signals or assert that the right
// memory rows were written.
func newConciergeTestServer(t *testing.T) (
	*httptest.Server, *sqlite.DB, *concierge.Service, *memory.Service,
) {
	t.Helper()
	ctx := context.Background()
	db, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "concierge.db"))
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	csvc := concierge.NewService(db, nil)
	msvc := memory.NewService(db, nil, nil)
	r := NewRouter(RouterDeps{
		APIToken:     "",
		Store:        db,
		ConciergeSvc: csvc,
		MemorySvc:    msvc,
	})
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return srv, db, csvc, msvc
}

func TestConciergeArmsHandlerLeaderboard(t *testing.T) {
	srv, _, csvc, _ := newConciergeTestServer(t)
	ctx := context.Background()

	// Seed signals across two prompt versions on the same worker so we
	// can assert the leaderboard ordering. v2 should win on confirmations.
	type seed struct {
		version int
		label   string
		count   int
	}
	seeds := []seed{
		{1, store.ChatTurnLabelConfirmation, 1},
		{1, store.ChatTurnLabelCorrection, 2},
		{1, store.ChatTurnLabelNeutral, 1},
		{2, store.ChatTurnLabelConfirmation, 4},
		{2, store.ChatTurnLabelCorrection, 1},
		{2, store.ChatTurnLabelRedirect, 1},
	}
	for _, s := range seeds {
		for i := 0; i < s.count; i++ {
			_, err := csvc.Record(ctx, concierge.RecordOptions{
				WorkerID:      "wkr-test",
				Channel:       "telegram",
				PromptVersion: s.version,
				UserMessage:   "seed",
				Label:         s.label,
			})
			if err != nil {
				t.Fatalf("Record(v=%d, label=%s): %v", s.version, s.label, err)
			}
		}
	}

	// Hit the endpoint and decode the leaderboard.
	resp, err := http.Get(srv.URL + "/api/v1/concierge/ab/arms?worker_id=wkr-test")
	if err != nil {
		t.Fatalf("GET arms: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d want 200", resp.StatusCode)
	}
	var body struct {
		Arms []struct {
			ID         string `json:"id"`
			Label      string `json:"label"`
			Wins       int    `json:"wins"`
			Losses     int    `json:"losses"`
			Draws      int    `json:"draws"`
			LastUsedAt string `json:"last_used_at"`
		} `json:"arms"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Arms) != 2 {
		t.Fatalf("expected 2 arms, got %d (%+v)", len(body.Arms), body.Arms)
	}
	// v2 wins on confirmations (4 vs 1), so it sorts first.
	if body.Arms[0].Label != "v2" {
		t.Errorf("expected v2 to lead the leaderboard, got %q", body.Arms[0].Label)
	}
	if body.Arms[0].Wins != 4 {
		t.Errorf("v2 wins = %d, want 4", body.Arms[0].Wins)
	}
	if body.Arms[0].Losses != 1 {
		t.Errorf("v2 losses = %d, want 1", body.Arms[0].Losses)
	}
	if body.Arms[0].Draws != 1 {
		t.Errorf("v2 draws (redirect) = %d, want 1", body.Arms[0].Draws)
	}
	if body.Arms[0].ID != "wkr-test:v2" {
		t.Errorf("v2 id = %q, want wkr-test:v2", body.Arms[0].ID)
	}
	if body.Arms[0].LastUsedAt == "" {
		t.Error("v2 last_used_at was empty; expected RFC3339 timestamp")
	}

	// v1 trails — 1 confirmation, 2 corrections, 1 neutral.
	if body.Arms[1].Label != "v1" {
		t.Errorf("expected v1 second, got %q", body.Arms[1].Label)
	}
	if body.Arms[1].Wins != 1 || body.Arms[1].Losses != 2 || body.Arms[1].Draws != 1 {
		t.Errorf("v1 row mismatch: %+v", body.Arms[1])
	}
}

func TestConciergeArmsHandlerEmpty(t *testing.T) {
	srv, _, _, _ := newConciergeTestServer(t)

	resp, err := http.Get(srv.URL + "/api/v1/concierge/ab/arms")
	if err != nil {
		t.Fatalf("GET arms: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d want 200", resp.StatusCode)
	}
	var body struct {
		Arms []armRow `json:"arms"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Arms) != 0 {
		t.Errorf("expected empty arms, got %d", len(body.Arms))
	}
}

func TestConciergePinLessonHandler(t *testing.T) {
	srv, db, _, _ := newConciergeTestServer(t)
	ctx := context.Background()

	reqBody := map[string]string{
		"topic":            "deploys",
		"lesson":           "always confirm the target environment before kicking a deploy",
		"evidence_summary": "user got frustrated when deploy hit staging instead of prod",
	}
	bts, _ := json.Marshal(reqBody)
	resp, err := http.Post(
		srv.URL+"/api/v1/concierge/lessons/pin",
		"application/json",
		bytes.NewReader(bts),
	)
	if err != nil {
		t.Fatalf("POST pin: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status=%d want 201", resp.StatusCode)
	}
	var out struct {
		MemoryID string `json:"memory_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.MemoryID == "" {
		t.Fatal("memory_id was empty")
	}

	// Verify the memory landed with the right shape.
	got, err := db.GetMemory(ctx, out.MemoryID)
	if err != nil {
		t.Fatalf("GetMemory(%s): %v", out.MemoryID, err)
	}
	if got.Kind != store.MemoryKindFact {
		t.Errorf("kind = %q, want fact", got.Kind)
	}
	if got.Content != reqBody["lesson"] {
		t.Errorf("content = %q, want %q", got.Content, reqBody["lesson"])
	}
	if !got.Pinned {
		t.Error("expected Pinned=true")
	}
	if got.Name != "concierge.lesson:deploys" {
		t.Errorf("name = %q, want concierge.lesson:deploys", got.Name)
	}
	// Tags JSON should include "concierge_lesson" so the integration test's
	// tag filter (and RecentLessonsFor) both surface it.
	if !bytes.Contains([]byte(got.TagsJSON), []byte(`"concierge_lesson"`)) {
		t.Errorf("tags missing concierge_lesson: %s", got.TagsJSON)
	}
	if !bytes.Contains([]byte(got.TagsJSON), []byte(`"concierge"`)) {
		t.Errorf("tags missing concierge: %s", got.TagsJSON)
	}
	if !bytes.Contains([]byte(got.TagsJSON), []byte(`"lesson"`)) {
		t.Errorf("tags missing lesson: %s", got.TagsJSON)
	}
}

func TestConciergePinLessonRejectsMissingLesson(t *testing.T) {
	srv, _, _, _ := newConciergeTestServer(t)

	bts, _ := json.Marshal(map[string]string{
		"topic": "deploys",
		// no "lesson" field
	})
	resp, err := http.Post(
		srv.URL+"/api/v1/concierge/lessons/pin",
		"application/json",
		bytes.NewReader(bts),
	)
	if err != nil {
		t.Fatalf("POST pin: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status=%d want 400", resp.StatusCode)
	}
}

func TestConciergePinLessonRejectsBadJSON(t *testing.T) {
	srv, _, _, _ := newConciergeTestServer(t)

	resp, err := http.Post(
		srv.URL+"/api/v1/concierge/lessons/pin",
		"application/json",
		bytes.NewReader([]byte("not json")),
	)
	if err != nil {
		t.Fatalf("POST pin: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status=%d want 400", resp.StatusCode)
	}
}
