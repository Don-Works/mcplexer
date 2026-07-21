package admin

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

type mockFileClaimStore struct {
	claims   []store.FileClaim
	inserts  int
	releases []string
	listErr  error
}

func (m *mockFileClaimStore) InsertFileClaim(_ context.Context, c *store.FileClaim) error {
	m.inserts++
	m.claims = append(m.claims, *c)
	return nil
}

func (m *mockFileClaimStore) ListFileClaims(_ context.Context, f store.FileClaimFilter) ([]store.FileClaim, error) {
	if m.listErr != nil {
		return nil, m.listErr
	}
	now := f.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	var out []store.FileClaim
	for _, c := range m.claims {
		if f.ActiveOnly && (c.ReleasedAt != nil || !c.ExpiresAt.After(now)) {
			continue
		}
		if f.Repo != "" && c.Repo != f.Repo {
			continue
		}
		out = append(out, c)
	}
	return out, nil
}

func (m *mockFileClaimStore) ReleaseFileClaim(_ context.Context, claimID string, _ time.Time) error {
	m.releases = append(m.releases, claimID)
	now := time.Now().UTC()
	for i := range m.claims {
		if m.claims[i].ClaimID == claimID && m.claims[i].ReleasedAt == nil {
			m.claims[i].ReleasedAt = &now
			return nil
		}
	}
	return store.ErrNotFound
}

func coordSvc(fcs FileClaimStore) *Service {
	return &Service{fileClaimStore: fcs, clock: realClock{}}
}

func TestCheckFileClaimOverlap(t *testing.T) {
	future := time.Now().Add(time.Hour)
	cases := []struct {
		name     string
		claims   []store.FileClaim
		paths    []string
		repo     string
		wantHits int
		wantIn   string
	}{
		{name: "no claims", paths: []string{"a.go", "b.go"}},
		{
			name: "own claim ignored",
			claims: []store.FileClaim{
				{ClaimID: "fc-del-1", ClaimerDisplayName: "delegation del-1", Paths: []string{"a.go"}, ExpiresAt: future},
			},
			paths: []string{"a.go"},
		},
		{
			name: "overlap detected with holder and intent",
			claims: []store.FileClaim{
				{ClaimID: "fc-del-other", ClaimerDisplayName: "delegation del-other", Paths: []string{"a.go", "b.go"}, Intent: "refactor auth", ExpiresAt: future},
			},
			paths:    []string{"a.go", "c.go"},
			wantHits: 1,
			wantIn:   "refactor auth",
		},
		{
			name: "expired claim ignored",
			claims: []store.FileClaim{
				{ClaimID: "fc-del-old", Paths: []string{"a.go"}, ExpiresAt: time.Now().Add(-time.Hour)},
			},
			paths: []string{"a.go"},
		},
		{
			name: "cross-repo claim filtered out",
			claims: []store.FileClaim{
				{ClaimID: "fc-del-x", Repo: "/repo/other", Paths: []string{"a.go"}, ExpiresAt: future},
			},
			paths: []string{"a.go"},
			repo:  "/repo/mine",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := coordSvc(&mockFileClaimStore{claims: tc.claims})
			warnings := svc.checkFileClaimOverlap(context.Background(), tc.paths, "del-1", tc.repo)
			if len(warnings) != tc.wantHits {
				t.Fatalf("warnings = %v, want %d", warnings, tc.wantHits)
			}
			if tc.wantIn != "" && !strings.Contains(warnings[0], tc.wantIn) {
				t.Fatalf("warning %q missing %q", warnings[0], tc.wantIn)
			}
		})
	}
}

func TestCheckFileClaimOverlapListErrorIsBestEffort(t *testing.T) {
	svc := coordSvc(&mockFileClaimStore{listErr: context.DeadlineExceeded})
	if w := svc.checkFileClaimOverlap(context.Background(), []string{"a.go"}, "del-1", ""); w != nil {
		t.Fatalf("list error must yield no warnings, got %v", w)
	}
}

func TestClaimDelegationFilesScopesAndExpires(t *testing.T) {
	fcs := &mockFileClaimStore{}
	svc := coordSvc(fcs)
	in := &DelegationInput{
		Objective:           strings.Repeat("x", 300),
		TouchesFiles:        []string{"internal/a.go"},
		MaxWallClockSeconds: 600,
	}
	svc.claimDelegationFiles(context.Background(), "del-9", in, "/repo/mine")
	if fcs.inserts != 1 {
		t.Fatalf("inserts = %d, want 1", fcs.inserts)
	}
	c := fcs.claims[0]
	if c.ClaimID != "fc-del-9" || c.Repo != "/repo/mine" {
		t.Fatalf("claim = %+v", c)
	}
	if c.ClaimerDisplayName != "delegation del-9" {
		t.Fatalf("display name = %q", c.ClaimerDisplayName)
	}
	if len(c.Intent) != 200 {
		t.Fatalf("intent must truncate to 200 chars, got %d", len(c.Intent))
	}
	ttl := c.ExpiresAt.Sub(c.ClaimedAt)
	want := 600*time.Second + 10*time.Minute
	if ttl != want {
		t.Fatalf("ttl = %v, want wall-clock+slack %v", ttl, want)
	}
}

func TestReleaseDelegationFileClaimsByDeterministicID(t *testing.T) {
	fcs := &mockFileClaimStore{claims: []store.FileClaim{
		{ClaimID: "fc-del-9", Paths: []string{"a.go"}, ExpiresAt: time.Now().Add(time.Hour)},
	}}
	svc := coordSvc(fcs)
	svc.releaseDelegationFileClaims(context.Background(), "del-9")
	if len(fcs.releases) != 1 || fcs.releases[0] != "fc-del-9" {
		t.Fatalf("releases = %v", fcs.releases)
	}
	// Idempotent: a second release (ErrNotFound) must not panic or log-spam.
	svc.releaseDelegationFileClaims(context.Background(), "del-9")
}

// TestDelegationClaimRepoAlwaysScoped is the regression test for grok's
// live finding: an empty scope key makes claim and check disagree
// (FileClaimFilter.Repo=="" means "all repos"). The resolver must return
// a non-empty, deterministic key so a stored claim and a later check use
// the same scope.
func TestDelegationClaimRepoAlwaysScoped(t *testing.T) {
	// No workspace lister: falls back to the synthetic workspace key.
	svc := &Service{clock: realClock{}}
	if got := svc.delegationClaimRepo(context.Background(), "ws-1"); got != "ws:ws-1" {
		t.Fatalf("no-lister scope = %q, want ws:ws-1", got)
	}
	// Empty workspace id: no coordination possible.
	if got := svc.delegationClaimRepo(context.Background(), ""); got != "" {
		t.Fatalf("empty workspace scope = %q, want empty", got)
	}
}

func TestClaimAndCheckAgreeOnEmptyRootPath(t *testing.T) {
	// A workspace with empty RootPath must still produce matching claim +
	// check scopes so overlap is detected, not lost.
	fcs := &mockFileClaimStore{}
	svc := &Service{fileClaimStore: fcs, clock: realClock{}}
	repo := svc.delegationClaimRepo(context.Background(), "ws-x") // "ws:ws-x"

	svc.claimDelegationFiles(context.Background(), "del-a",
		&DelegationInput{Objective: "edit", TouchesFiles: []string{"a.go"}, MaxWallClockSeconds: 60}, repo)
	if fcs.claims[0].Repo != "ws:ws-x" {
		t.Fatalf("claim repo = %q, want ws:ws-x", fcs.claims[0].Repo)
	}
	// A different delegation in the same workspace sees the overlap.
	warnings := svc.checkFileClaimOverlap(context.Background(), []string{"a.go"}, "del-b", repo)
	if len(warnings) != 1 {
		t.Fatalf("same-workspace overlap not detected: %v", warnings)
	}
	// A delegation in a DIFFERENT workspace does not.
	other := svc.delegationClaimRepo(context.Background(), "ws-y")
	if w := svc.checkFileClaimOverlap(context.Background(), []string{"a.go"}, "del-c", other); len(w) != 0 {
		t.Fatalf("cross-workspace false positive: %v", w)
	}
}

func TestCoordinationNilStoreIsNoop(t *testing.T) {
	svc := &Service{clock: realClock{}}
	if w := svc.checkFileClaimOverlap(context.Background(), []string{"a.go"}, "del-1", ""); w != nil {
		t.Fatalf("nil store must warn nothing, got %v", w)
	}
	svc.claimDelegationFiles(context.Background(), "del-1", &DelegationInput{TouchesFiles: []string{"a.go"}}, "")
	svc.releaseDelegationFileClaims(context.Background(), "del-1")
}
