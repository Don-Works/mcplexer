package assist

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/don-works/mcplexer/internal/models"
	"github.com/don-works/mcplexer/internal/store"
)

// fakeProfileStore is a minimal store.ModelProfileStore for resolution tests.
type fakeProfileStore struct {
	profiles []store.ModelProfile
	byName   map[string]store.ModelProfile
}

func (f *fakeProfileStore) ListModelProfiles(context.Context) ([]store.ModelProfile, error) {
	return f.profiles, nil
}
func (f *fakeProfileStore) GetModelProfile(context.Context, string) (store.ModelProfile, error) {
	return store.ModelProfile{}, store.ErrNotFound
}
func (f *fakeProfileStore) GetModelProfileByName(_ context.Context, name string) (store.ModelProfile, error) {
	if p, ok := f.byName[name]; ok {
		return p, nil
	}
	return store.ModelProfile{}, store.ErrNotFound
}
func (f *fakeProfileStore) CreateModelProfile(context.Context, *store.ModelProfile) error { return nil }
func (f *fakeProfileStore) UpdateModelProfile(context.Context, *store.ModelProfile) error { return nil }
func (f *fakeProfileStore) DeleteModelProfile(context.Context, string) error              { return nil }

// fakeAdapter returns a canned reply and records the last request.
type fakeAdapter struct {
	reply string
	last  models.SendRequest
}

func (a *fakeAdapter) Send(_ context.Context, req models.SendRequest) (*models.SendResponse, error) {
	a.last = req
	return &models.SendResponse{Text: a.reply, StopReason: models.StopEndTurn}, nil
}

// newTestAssistant wires an Assistant whose factory always returns fa.
func newTestAssistant(s store.ModelProfileStore, fa *fakeAdapter) *Assistant {
	a := New(s, nil, nil)
	a.factory = func(models.Config) (models.ModelAdapter, error) { return fa, nil }
	return a
}

func oneProfileStore() *fakeProfileStore {
	p := store.ModelProfile{
		ID:          "p1",
		Name:        "openai_compat",
		Provider:    models.ProviderOpenAICompat,
		EndpointURL: "http://localhost:1234/v1/chat/completions",
		KnownModels: []string{"local-model"},
	}
	return &fakeProfileStore{profiles: []store.ModelProfile{p}, byName: map[string]store.ModelProfile{p.Name: p}}
}

func TestComplete(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name      string
		reply     string
		req       CompleteRequest
		wantText  string
		wantNoErr bool
	}{
		{
			name:      "plain continuation",
			reply:     " so the timer rearms",
			req:       CompleteRequest{Context: "Fix: recompute next_run_at", Field: "description"},
			wantText:  " so the timer rearms",
			wantNoErr: true,
		},
		{
			name:      "strips surrounding quotes",
			reply:     `"and re-arms the cron"`,
			req:       CompleteRequest{Context: "The fix ", Field: "description"},
			wantText:  "and re-arms the cron",
			wantNoErr: true,
		},
		{
			name:      "drops mid-word echo",
			reply:     "recompute the next run",
			req:       CompleteRequest{Context: "recom", Field: "title"},
			wantText:  "pute the next run",
			wantNoErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fa := &fakeAdapter{reply: tt.reply}
			a := newTestAssistant(oneProfileStore(), fa)
			got, profile, err := a.Complete(ctx, tt.req)
			if (err == nil) != tt.wantNoErr {
				t.Fatalf("err=%v wantNoErr=%v", err, tt.wantNoErr)
			}
			if got != tt.wantText {
				t.Errorf("text = %q want %q", got, tt.wantText)
			}
			if profile != "openai_compat" {
				t.Errorf("profile = %q want openai_compat", profile)
			}
		})
	}
}

func TestComplete_NoProfile(t *testing.T) {
	ctx := context.Background()
	empty := &fakeProfileStore{byName: map[string]store.ModelProfile{}}
	a := newTestAssistant(empty, &fakeAdapter{reply: "x"})
	_, _, err := a.Complete(ctx, CompleteRequest{Context: "hi"})
	if !errors.Is(err, ErrNoProfile) {
		t.Fatalf("want ErrNoProfile, got %v", err)
	}
}

func TestComplete_NamedProfileMissing(t *testing.T) {
	ctx := context.Background()
	a := newTestAssistant(oneProfileStore(), &fakeAdapter{reply: "x"})
	_, _, err := a.Complete(ctx, CompleteRequest{Context: "hi", ModelProfile: "does-not-exist"})
	if !errors.Is(err, ErrNoProfile) {
		t.Fatalf("want ErrNoProfile for missing named profile, got %v", err)
	}
}

func TestComplete_ProfileWithNoModelIsUnusable(t *testing.T) {
	ctx := context.Background()
	p := store.ModelProfile{ID: "p", Name: "no-model", Provider: models.ProviderOpenAICompat, KnownModels: nil}
	s := &fakeProfileStore{profiles: []store.ModelProfile{p}, byName: map[string]store.ModelProfile{p.Name: p}}
	a := newTestAssistant(s, &fakeAdapter{reply: "x"})
	_, _, err := a.Complete(ctx, CompleteRequest{Context: "hi"})
	if !errors.Is(err, ErrNoProfile) {
		t.Fatalf("want ErrNoProfile for model-less profile, got %v", err)
	}
}

// gatedProfileStore returns a single profile with the given provider/known
// model so we can drive the adapter-construction-error path.
func gatedProfileStore(prof store.ModelProfile) *fakeProfileStore {
	return &fakeProfileStore{
		profiles: []store.ModelProfile{prof},
		byName:   map[string]store.ModelProfile{prof.Name: prof},
	}
}

// newAssistantWithFactory wires an Assistant whose factory is fully caller
// controlled (so a test can simulate models.NewAdapter rejecting a resolved
// but undrivable profile).
func newAssistantWithFactory(s store.ModelProfileStore, factory adapterFactory) *Assistant {
	a := New(s, nil, nil)
	a.factory = factory
	return a
}

// TestComplete_UnusableProfileDegrades locks issue #1: a RESOLVED-but-
// undrivable profile (gated CLI without the env opt-in, or a key/endpoint
// that didn't load) must degrade to ErrNoProfile — NOT surface the raw
// adapter-construction error (which the HTTP layer maps to 502). The
// silent-degrade contract (DESIGN §3.4) requires absence, not a nag.
func TestComplete_UnusableProfileDegrades(t *testing.T) {
	ctx := context.Background()
	prof := store.ModelProfile{
		ID: "p", Name: "gated", Provider: models.ProviderClaudeCLI,
		KnownModels: []string{"claude-sonnet"},
	}
	factoryErrs := []error{
		models.ErrClaudeCLINotAllowed,
		models.ErrOpenCodeCLINotAllowed,
		models.ErrMissingAPIKey,
		models.ErrMissingEndpoint,
		models.ErrMissingModelID,
	}
	for _, fe := range factoryErrs {
		t.Run(fe.Error(), func(t *testing.T) {
			factory := func(models.Config) (models.ModelAdapter, error) { return nil, fe }
			a := newAssistantWithFactory(gatedProfileStore(prof), factory)

			_, _, err := a.Complete(ctx, CompleteRequest{Context: "hi", ModelProfile: "gated"})
			if !errors.Is(err, ErrNoProfile) {
				t.Errorf("Complete: factory err %v -> got %v, want ErrNoProfile", fe, err)
			}

			_, _, err = a.MemoryCandidates(ctx, MemoryCandidateRequest{Body: "a decision because reasons", ModelProfile: "gated"})
			if !errors.Is(err, ErrNoProfile) {
				t.Errorf("MemoryCandidates: factory err %v -> got %v, want ErrNoProfile", fe, err)
			}
		})
	}
}

// TestComplete_SendErrorIsHardError locks the other side of issue #1/#3: a
// genuine Send() RUNTIME failure (profile resolved, adapter built, model call
// failed) must NOT be swallowed as ErrNoProfile — it surfaces verbatim so the
// HTTP layer can 502. This keeps the construction-error and runtime-error
// paths distinct.
func TestComplete_SendErrorIsHardError(t *testing.T) {
	ctx := context.Background()
	sendErr := errors.New("upstream 503")
	a := newAssistantWithFactory(oneProfileStore(), func(models.Config) (models.ModelAdapter, error) {
		return &erroringAdapter{err: sendErr}, nil
	})

	_, _, err := a.Complete(ctx, CompleteRequest{Context: "hi"})
	if !errors.Is(err, sendErr) {
		t.Errorf("Complete: want raw send err, got %v", err)
	}
	if errors.Is(err, ErrNoProfile) {
		t.Errorf("Complete: send error must NOT degrade to ErrNoProfile")
	}

	_, _, err = a.MemoryCandidates(ctx, MemoryCandidateRequest{Body: "x because y"})
	if !errors.Is(err, sendErr) {
		t.Errorf("MemoryCandidates: want raw send err, got %v", err)
	}
	if errors.Is(err, ErrNoProfile) {
		t.Errorf("MemoryCandidates: send error must NOT degrade to ErrNoProfile")
	}
}

// erroringAdapter constructs fine but fails at Send time.
type erroringAdapter struct{ err error }

func (a *erroringAdapter) Send(context.Context, models.SendRequest) (*models.SendResponse, error) {
	return nil, a.err
}

// TestPickProfile_SkipsUnusableInAutoSelect locks issue #2: auto-select (empty
// model_profile) must fall THROUGH a first-listed profile whose adapter cannot
// be constructed (gated claude_cli) to a later, usable profile — instead of
// returning the dead one and 502ing every call.
func TestPickProfile_SkipsUnusableInAutoSelect(t *testing.T) {
	ctx := context.Background()
	gated := store.ModelProfile{
		ID: "p1", Name: "gated-cli", Provider: models.ProviderClaudeCLI,
		KnownModels: []string{"claude-sonnet"},
	}
	usable := store.ModelProfile{
		ID: "p2", Name: "local", Provider: models.ProviderOpenAICompat,
		EndpointURL: "http://localhost:1234/v1", KnownModels: []string{"local-model"},
	}
	s := &fakeProfileStore{
		profiles: []store.ModelProfile{gated, usable},
		byName: map[string]store.ModelProfile{
			gated.Name: gated, usable.Name: usable,
		},
	}
	// Factory rejects the gated CLI profile (no opt-in) but accepts the
	// openai_compat one — mirroring models.NewAdapter under no env opt-in.
	factory := func(cfg models.Config) (models.ModelAdapter, error) {
		if cfg.Provider == models.ProviderClaudeCLI {
			return nil, models.ErrClaudeCLINotAllowed
		}
		return &fakeAdapter{reply: "ok"}, nil
	}
	a := newAssistantWithFactory(s, factory)

	_, name, err := a.resolveConfig(ctx, "")
	if err != nil {
		t.Fatalf("resolveConfig auto-select: unexpected err %v", err)
	}
	if name != "local" {
		t.Errorf("auto-select picked %q, want fall-through to usable %q", name, "local")
	}

	// And a full Complete call against auto-select succeeds via the usable one.
	got, profile, err := a.Complete(ctx, CompleteRequest{Context: "hi"})
	if err != nil {
		t.Fatalf("Complete auto-select: unexpected err %v", err)
	}
	if profile != "local" || got != "ok" {
		t.Errorf("Complete auto-select: got %q via %q", got, profile)
	}
}

// TestPickProfile_AutoSelectAllUnusableDegrades: when EVERY candidate is
// undrivable, auto-select reports no usable profile -> ErrNoProfile (204), not
// a hard error.
func TestPickProfile_AutoSelectAllUnusableDegrades(t *testing.T) {
	ctx := context.Background()
	gated := store.ModelProfile{
		ID: "p1", Name: "gated-cli", Provider: models.ProviderClaudeCLI,
		KnownModels: []string{"claude-sonnet"},
	}
	s := gatedProfileStore(gated)
	factory := func(models.Config) (models.ModelAdapter, error) {
		return nil, models.ErrClaudeCLINotAllowed
	}
	a := newAssistantWithFactory(s, factory)

	if _, _, err := a.Complete(ctx, CompleteRequest{Context: "hi"}); !errors.Is(err, ErrNoProfile) {
		t.Errorf("all-unusable auto-select -> got %v, want ErrNoProfile", err)
	}
}

func TestResolveConfig_PassesModelAndEndpoint(t *testing.T) {
	ctx := context.Background()
	a := New(oneProfileStore(), nil, nil)
	cfg, name, err := a.resolveConfig(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ModelID != "local-model" || cfg.Provider != models.ProviderOpenAICompat {
		t.Errorf("cfg = %+v", cfg)
	}
	if cfg.EndpointURL == "" {
		t.Errorf("endpoint not propagated")
	}
	if name != "openai_compat" {
		t.Errorf("name = %q", name)
	}
}

func TestCleanCompletion(t *testing.T) {
	tests := []struct {
		raw, before, want string
	}{
		{"  hello world  ", "", "hello world"},
		{"`code`", "", "code"},
		{`"quoted"`, "", "quoted"},
		{"pute", "re-com", "pute"},   // mid-word echo trimmed
		{"world", "hello ", "world"}, // trailing space => no trim
		{"", "anything", ""},         // empty stays empty
	}
	for _, tt := range tests {
		if got := cleanCompletion(tt.raw, tt.before); got != tt.want {
			t.Errorf("cleanCompletion(%q,%q) = %q want %q", tt.raw, tt.before, got, tt.want)
		}
	}
}

func TestMemoryCandidates(t *testing.T) {
	ctx := context.Background()
	reply := `[
  {"text":"Never deploy from a dirty tree because the build no longer maps to a git ref.","kind":"note","tags":["ops"],"signal":"decision-with-rationale"},
  {"text":"Re-arm worker cron jobs","kind":"note","signal":"title-restatement"},
  {"text":"Never deploy from a dirty tree because the build no longer maps to a git ref.","kind":"note","signal":"decision-with-rationale"}
]`
	fa := &fakeAdapter{reply: reply}
	a := newTestAssistant(oneProfileStore(), fa)
	cands, profile, err := a.MemoryCandidates(ctx, MemoryCandidateRequest{Body: "wrote a decision down"})
	if err != nil {
		t.Fatal(err)
	}
	if profile != "openai_compat" {
		t.Errorf("profile = %q", profile)
	}
	// title-restatement dropped + duplicate deduped => exactly 1 keeper.
	if len(cands) != 1 {
		t.Fatalf("want 1 candidate, got %d: %+v", len(cands), cands)
	}
	c := cands[0]
	if c.Signal != SignalDecisionWithRationale {
		t.Errorf("signal = %q", c.Signal)
	}
	if c.ContentHash == "" {
		t.Errorf("content hash not stamped")
	}
	if !reflect.DeepEqual(c.Tags, []string{"ops"}) {
		t.Errorf("tags = %v", c.Tags)
	}
}

func TestMemoryCandidates_EmptyBodySkips(t *testing.T) {
	ctx := context.Background()
	a := newTestAssistant(oneProfileStore(), &fakeAdapter{reply: "[]"})
	cands, _, err := a.MemoryCandidates(ctx, MemoryCandidateRequest{Body: "   "})
	if err != nil || cands != nil {
		t.Fatalf("empty body should skip: cands=%v err=%v", cands, err)
	}
}

func TestParseCandidates_ToleratesProse(t *testing.T) {
	got := parseCandidates("Here you go:\n```json\n[{\"text\":\"x because y\",\"kind\":\"note\",\"signal\":\"decision-with-rationale\"}]\n```")
	if len(got) != 1 || got[0].Text != "x because y" {
		t.Fatalf("parse = %+v", got)
	}
}

func TestContentHashStable(t *testing.T) {
	if contentHash(" a ") != contentHash("a") {
		t.Errorf("hash not whitespace-stable")
	}
	if contentHash("a") == contentHash("b") {
		t.Errorf("distinct text collided")
	}
}
