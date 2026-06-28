package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/don-works/mcplexer/internal/assist"
	"github.com/don-works/mcplexer/internal/models"
	"github.com/don-works/mcplexer/internal/store"
)

func TestChunkByWord(t *testing.T) {
	tests := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"hello", []string{"hello"}},
		{"hello world", []string{"hello", " world"}},
		{" leading", []string{" leading"}},
		{"a\nb", []string{"a", "\nb"}},
	}
	for _, tt := range tests {
		got := chunkByWord(tt.in)
		// Lossless: joining the chunks reproduces the input.
		if strings.Join(got, "") != tt.in {
			t.Errorf("chunkByWord(%q) join = %q, not lossless", tt.in, strings.Join(got, ""))
		}
		if len(got) != len(tt.want) {
			t.Errorf("chunkByWord(%q) = %v want %v", tt.in, got, tt.want)
		}
	}
}

func TestSSEData_EscapesNewlines(t *testing.T) {
	if got := sseData("a\nb\r"); got != "a\\nb" {
		t.Errorf("sseData = %q", got)
	}
}

// emptyProfileStore is a Store with no model profiles, so the real
// assist.Assistant resolves to ErrNoProfile without ever calling a model.
func newAssistHandler(t *testing.T, enabled bool) *assistHandler {
	t.Helper()
	db := newBrainTestStore(t)
	return &assistHandler{
		assistant: assist.New(db, nil, nil),
		store:     db,
		enabled:   enabled,
	}
}

func TestComplete_DisabledIs503(t *testing.T) {
	h := newAssistHandler(t, false)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/assist/complete", h.complete)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/v1/assist/complete", "application/json",
		strings.NewReader(`{"context":"hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d want 503", resp.StatusCode)
	}
}

func TestComplete_NoProfileIs204(t *testing.T) {
	h := newAssistHandler(t, true)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/assist/complete", h.complete)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/v1/assist/complete", "application/json",
		strings.NewReader(`{"context":"continue this"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d want 204 (silent degrade, no profile)", resp.StatusCode)
	}
}

func TestComplete_MissingContextIs400(t *testing.T) {
	h := newAssistHandler(t, true)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/assist/complete", h.complete)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/v1/assist/complete", "application/json",
		strings.NewReader(`{"context":"  "}`))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d want 400", resp.StatusCode)
	}
}

func TestMemoryCandidates_NoProfileIs204(t *testing.T) {
	h := newAssistHandler(t, true)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/assist/memory-candidates", h.memoryCandidates)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/v1/assist/memory-candidates", "application/json",
		strings.NewReader(`{"body":"a decision because reasons"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d want 204", resp.StatusCode)
	}
}

// TestComplete_UnusableProfileIs204 is the api-layer regression for the
// silent-degrade contract: when the ONLY model profile is a gated claude_cli
// and the env opt-in (MCPLEXER_ALLOW_CLAUDE_CLI) is NOT set, the real
// models.NewAdapter returns ErrClaudeCLINotAllowed. The handler must map that
// to 204 (ghost text simply absent) — NOT 502 on every keystroke. Before the
// fix this returned 502 BadGateway.
func TestComplete_UnusableProfileIs204(t *testing.T) {
	db := newBrainTestStore(t)
	seedGatedCLIProfile(t, db)
	h := &assistHandler{assistant: assist.New(db, nil, nil), store: db, enabled: true}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/assist/complete", h.complete)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/v1/assist/complete", "application/json",
		strings.NewReader(`{"context":"continue this"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d want 204 (gated CLI must degrade silently, not 502)", resp.StatusCode)
	}
}

// TestMemoryCandidates_UnusableProfileIs204 mirrors the above for the
// proactive-memory endpoint: a gated-CLI-only store must 204, not 502.
func TestMemoryCandidates_UnusableProfileIs204(t *testing.T) {
	db := newBrainTestStore(t)
	seedGatedCLIProfile(t, db)
	h := &assistHandler{assistant: assist.New(db, nil, nil), store: db, enabled: true}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/assist/memory-candidates", h.memoryCandidates)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/v1/assist/memory-candidates", "application/json",
		strings.NewReader(`{"body":"a decision because reasons"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d want 204 (gated CLI must degrade silently, not 502)", resp.StatusCode)
	}
}

func TestComplete_NewerUnusableCLIProfilesAre204(t *testing.T) {
	tests := []struct {
		provider string
		model    string
		env      string
	}{
		{models.ProviderGrokCLI, "grok-build", "MCPLEXER_ALLOW_GROK_CLI"},
		{models.ProviderMiMoCLI, "xiaomi/mimo-v2.5", "MCPLEXER_ALLOW_MIMO_CLI"},
		{models.ProviderGeminiCLI, "gemini-2.5-pro", "MCPLEXER_ALLOW_GEMINI_CLI"},
		{models.ProviderCodexCLI, "o3", "MCPLEXER_ALLOW_CODEX_CLI"},
		{models.ProviderPiCLI, "qwen-local", "MCPLEXER_ALLOW_PI_CLI"},
	}
	for _, tt := range tests {
		t.Run(tt.provider, func(t *testing.T) {
			db := newBrainTestStore(t)
			seedGatedProviderProfile(t, db, tt.provider, tt.model, tt.env)
			h := &assistHandler{assistant: assist.New(db, nil, nil), store: db, enabled: true}

			mux := http.NewServeMux()
			mux.HandleFunc("POST /api/v1/assist/complete", h.complete)
			srv := httptest.NewServer(mux)
			defer srv.Close()

			resp, err := http.Post(srv.URL+"/api/v1/assist/complete", "application/json",
				strings.NewReader(`{"context":"continue this"}`))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != http.StatusNoContent {
				t.Fatalf("status = %d want 204 (gated %s must degrade silently, not 502)", resp.StatusCode, tt.provider)
			}
		})
	}
}

// seedGatedCLIProfile inserts a single claude_cli model profile. With
// MCPLEXER_ALLOW_CLAUDE_CLI unset (the test default), models.NewAdapter
// rejects it with ErrClaudeCLINotAllowed — exercising the real
// adapter-construction-error -> silent-degrade path end-to-end.
func seedGatedCLIProfile(t *testing.T, db store.ModelProfileStore) {
	t.Helper()
	seedGatedProviderProfile(t, db, models.ProviderClaudeCLI, "claude-sonnet", "MCPLEXER_ALLOW_CLAUDE_CLI")
}

func seedGatedProviderProfile(t *testing.T, db store.ModelProfileStore, provider, model, env string) {
	t.Helper()
	t.Setenv(env, "")
	p := &store.ModelProfile{
		ID:          "gated-" + provider,
		Name:        "gated-" + provider,
		Provider:    provider,
		KnownModels: []string{model},
	}
	if err := db.CreateModelProfile(context.Background(), p); err != nil {
		t.Fatalf("seed gated cli profile: %v", err)
	}
}

// TestGuidance_DisabledIs503 confirms guidance honours the brain gate.
func TestGuidance_DisabledIs503(t *testing.T) {
	h := newAssistHandler(t, false)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/assist/guidance", h.guidance)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/v1/assist/guidance", "application/json",
		strings.NewReader(`{"body":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d want 503", resp.StatusCode)
	}
}

// TestGuidance_NoProfileStill200 confirms guidance NEVER 204s on no model: the
// deterministic nudges still come back as a 200 JSON list (DESIGN §4.4) — the
// model-backed link-memory nudge is simply omitted.
func TestGuidance_NoProfileStill200(t *testing.T) {
	h := newAssistHandler(t, true) // store has no model profile
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/assist/guidance", h.guidance)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/v1/assist/guidance", "application/json",
		strings.NewReader(`{"status":"doing","body":"re-arm the cron scheduler"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d want 200 (deterministic nudges, no 204)", resp.StatusCode)
	}
	var body struct {
		Nudges  []assist.Nudge `json:"nudges"`
		Profile string         `json:"profile"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	// doing + "scheduler" => missing-criteria + auto-tag, no model profile.
	if len(body.Nudges) != 2 {
		t.Fatalf("got %d nudges, want 2 deterministic", len(body.Nudges))
	}
	if body.Profile != "" {
		t.Errorf("profile = %q want empty (no model)", body.Profile)
	}
}

// TestFilterSuppressed verifies the sticky-suppression filter drops a
// matching content-hash and the suppress-all marker, and fails open on a
// blank record id.
func TestFilterSuppressed(t *testing.T) {
	ctx := context.Background()
	db := newBrainTestStore(t)
	h := &assistHandler{store: db, enabled: true}

	if err := db.SuppressCandidate(ctx, "rec1", "hash-a"); err != nil {
		t.Fatal(err)
	}
	in := []assist.Candidate{
		{Text: "a", ContentHash: "hash-a"},
		{Text: "b", ContentHash: "hash-b"},
	}
	r := httptest.NewRequest("POST", "/x", nil)

	got := h.filterSuppressed(r, "rec1", in)
	if len(got) != 1 || got[0].ContentHash != "hash-b" {
		t.Fatalf("suppressed hash not filtered: %+v", got)
	}

	// Blank record id => no filtering (unsaved record can't be suppressed).
	if got := h.filterSuppressed(r, "", in); len(got) != 2 {
		t.Fatalf("blank record id should skip filter, got %d", len(got))
	}

	// suppress-all marker drops everything for the record.
	if err := db.SuppressCandidate(ctx, "rec2", ""); err != nil {
		t.Fatal(err)
	}
	if got := h.filterSuppressed(r, "rec2", in); len(got) != 0 {
		t.Fatalf("suppress-all should drop all, got %d", len(got))
	}
}
