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
	if snapshot.Detail != "Local MiMoCode session usage collected; subscription balance is not exposed by the CLI/API" {
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
	result := mimoAuthResult(store.SourceConfig{}, parsed, time.Now())
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
