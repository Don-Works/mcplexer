package collectors

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

func TestMiMoCollectorAuthenticatedWithoutFakeAllowance(t *testing.T) {
	collector := &MiMoCollector{
		Run: func(_ context.Context, _ string) ([]byte, error) {
			return []byte(`{"provider":"xiaomi","email":"user@example.com","user_id":"u-123"}`), nil
		},
	}
	result, err := collector.Fetch(context.Background(), store.SourceConfig{
		Provider: store.ProviderMiMo, Kind: store.SourceKindCLI, Label: "MiMo",
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := result.Snapshot
	if snapshot.Status != store.StatusOK || snapshot.AllowanceSource != "auth" || len(snapshot.Windows) != 0 {
		t.Fatalf("snapshot = status:%s windows:%d", snapshot.Status, len(snapshot.Windows))
	}
	if snapshot.Detail != "Local MiMoCode session usage collected; exact remaining balance requires Xiaomi console login" {
		t.Fatalf("detail = %q", snapshot.Detail)
	}
	if strings.Contains(snapshot.Detail, "user@example.com") || strings.Contains(snapshot.Detail, "u-123") {
		t.Fatalf("detail leaked user identity: %q", snapshot.Detail)
	}
}

func TestMiMoCollectorReportsProbeFailure(t *testing.T) {
	collector := &MiMoCollector{
		Run: func(_ context.Context, _ string) ([]byte, error) {
			return nil, context.DeadlineExceeded
		},
	}
	result, err := collector.Fetch(context.Background(), store.SourceConfig{Provider: store.ProviderMiMo})
	if err != nil {
		t.Fatal(err)
	}
	if result.Snapshot.Status != store.StatusPartial || result.Snapshot.Error == "" {
		t.Fatalf("snapshot = %+v", result.Snapshot)
	}
}

func TestParseMiMoWhoamiRejectsNonProviderOutput(t *testing.T) {
	parsed := parseMiMoWhoami([]byte(`{"email":"user@example.com","id":"abc"}`))
	if len(parsed.errors) == 0 {
		t.Fatalf("parsed = %+v", parsed)
	}
}

func TestParseMiMoWhoamiAcceptsHumanOutputWithoutIdentity(t *testing.T) {
	parsed := parseMiMoWhoami([]byte("\x1b[0m\nCurrent user\nProvider: MiMo\nUser ID: 1234567890\n"))
	if parsed.provider != "mimo" || len(parsed.errors) != 0 {
		t.Fatalf("parsed = %+v", parsed)
	}
	result := mimoAuthResult(store.SourceConfig{}, parsed, false, time.Now())
	if strings.Contains(result.Snapshot.Detail, "1234567890") {
		t.Fatalf("detail leaked identity: %q", result.Snapshot.Detail)
	}
}

func TestMiMoCollectorSetsUpdatedAtOnSuccess(t *testing.T) {
	start := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	collector := &MiMoCollector{
		Run: func(_ context.Context, _ string) ([]byte, error) {
			return []byte(`{"provider":"xiaomi"}`), nil
		},
	}
	result, err := collector.Fetch(context.Background(), store.SourceConfig{Provider: store.ProviderMiMo})
	if err != nil {
		t.Fatal(err)
	}
	if result.Snapshot.UpdatedAt == nil {
		t.Fatal("expected updated_at")
	}
	if result.Duration < 0 || start.After(time.Now()) {
		t.Fatalf("duration = %s", result.Duration)
	}
}

func TestMiMoCollectorTokenPlanSetsPlanFromSecret(t *testing.T) {
	collector := &MiMoCollector{
		Run: func(_ context.Context, _ string) ([]byte, error) {
			return []byte(`{"provider":"xiaomi"}`), nil
		},
		Secret: &fakeSecretReader{value: []byte("tp-abc123secret")},
	}
	result, err := collector.Fetch(context.Background(), store.SourceConfig{
		Provider:    store.ProviderMiMo,
		AuthScopeID: "local:mimo",
		SecretKey:   "xiaomi",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Snapshot.Plan != "Token Plan" {
		t.Fatalf("plan = %q, want Token Plan", result.Snapshot.Plan)
	}
}

func TestMiMoCollectorNonTokenPlanDefaultsToMiMoCode(t *testing.T) {
	collector := &MiMoCollector{
		Run: func(_ context.Context, _ string) ([]byte, error) {
			return []byte(`{"provider":"xiaomi"}`), nil
		},
		Secret: &fakeSecretReader{value: []byte("sk-regular-key")},
	}
	result, err := collector.Fetch(context.Background(), store.SourceConfig{
		Provider:    store.ProviderMiMo,
		AuthScopeID: "local:mimo",
		SecretKey:   "xiaomi",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Snapshot.Plan != "MiMoCode" {
		t.Fatalf("plan = %q, want MiMoCode", result.Snapshot.Plan)
	}
}

func TestMiMoCollectorConfiguredPlanOverridesDetection(t *testing.T) {
	collector := &MiMoCollector{
		Run: func(_ context.Context, _ string) ([]byte, error) {
			return []byte(`{"provider":"xiaomi"}`), nil
		},
		Secret: &fakeSecretReader{value: []byte("tp-abc123")},
	}
	result, err := collector.Fetch(context.Background(), store.SourceConfig{
		Provider:    store.ProviderMiMo,
		Plan:        "Custom Plan",
		AuthScopeID: "local:mimo",
		SecretKey:   "xiaomi",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Snapshot.Plan != "Custom Plan" {
		t.Fatalf("plan = %q, want Custom Plan", result.Snapshot.Plan)
	}
}

func TestMiMoCollectorSecretNotLeaked(t *testing.T) {
	collector := &MiMoCollector{
		Run: func(_ context.Context, _ string) ([]byte, error) {
			return []byte(`{"provider":"xiaomi"}`), nil
		},
		Secret: &fakeSecretReader{value: []byte("tp-supersecretkey123")},
	}
	result, err := collector.Fetch(context.Background(), store.SourceConfig{
		Provider:    store.ProviderMiMo,
		AuthScopeID: "local:mimo",
		SecretKey:   "xiaomi",
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := result.Snapshot
	if strings.Contains(snapshot.Detail, "tp-supersecretkey123") {
		t.Fatalf("detail leaked secret: %q", snapshot.Detail)
	}
	if strings.Contains(snapshot.Error, "tp-supersecretkey123") {
		t.Fatalf("error leaked secret: %q", snapshot.Error)
	}
}

func TestMiMoIsTokenPlanCredential(t *testing.T) {
	if !mimoIsTokenPlanCredential("tp-abc123") {
		t.Fatal("expected tp- prefix to be detected")
	}
	if !mimoIsTokenPlanCredential("  tp-abc123  ") {
		t.Fatal("expected surrounding whitespace to be ignored")
	}
	if mimoIsTokenPlanCredential("sk-regular") {
		t.Fatal("expected non-tp- prefix to be rejected")
	}
	if mimoIsTokenPlanCredential("") {
		t.Fatal("expected empty string to be rejected")
	}
}

type fakeSecretReader struct {
	value []byte
	err   error
}

func (f *fakeSecretReader) Get(_ context.Context, _, _ string) ([]byte, error) {
	return f.value, f.err
}
