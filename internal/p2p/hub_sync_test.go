package p2p

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

type fakeHubIndexProvider struct {
	entries []HubIndexEntry
	err     error
}

func (f *fakeHubIndexProvider) ListIndexEntries(_ context.Context) ([]HubIndexEntry, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.entries, nil
}

type fakeConflictDetector struct {
	toPull    []HubIndexEntry
	skipped   []HubIndexEntry
	conflicts []HubIndexEntry
	err       error
}

func (f *fakeConflictDetector) ClassifyEntries(
	_ context.Context, remote []HubIndexEntry,
) ([]HubIndexEntry, []HubIndexEntry, []HubIndexEntry, error) {
	if f.err != nil {
		return nil, nil, nil, f.err
	}
	return f.toPull, f.skipped, f.conflicts, nil
}

func TestHubIndexProviderReturnsEntries(t *testing.T) {
	prov := &fakeHubIndexProvider{
		entries: []HubIndexEntry{
			{Name: "deploy-fly", Version: 3, ContentHash: "abc123", Description: "Use when deploying to Fly.io"},
			{Name: "pdf-extract", Version: 1, ContentHash: "def456", Description: "Use when extracting text from PDFs"},
		},
	}
	entries, err := prov.ListIndexEntries(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	if entries[0].Name != "deploy-fly" {
		t.Errorf("entry[0].Name = %q, want %q", entries[0].Name, "deploy-fly")
	}
	if entries[1].ContentHash != "def456" {
		t.Errorf("entry[1].ContentHash = %q, want %q", entries[1].ContentHash, "def456")
	}
}

func TestHubIndexProviderError(t *testing.T) {
	prov := &fakeHubIndexProvider{err: errors.New("db down")}
	_, err := prov.ListIndexEntries(context.Background())
	if err == nil || err.Error() != "db down" {
		t.Fatalf("expected 'db down' error, got %v", err)
	}
}

func TestConflictDetectorClassifies(t *testing.T) {
	det := &fakeConflictDetector{
		toPull:    []HubIndexEntry{{Name: "new-skill", Version: 1, ContentHash: "aaa"}},
		skipped:   []HubIndexEntry{{Name: "existing-skill", Version: 2, ContentHash: "bbb"}},
		conflicts: []HubIndexEntry{{Name: "diverged", Version: 3, ContentHash: "ccc"}},
	}
	remote := []HubIndexEntry{
		{Name: "new-skill", Version: 1, ContentHash: "aaa"},
		{Name: "existing-skill", Version: 2, ContentHash: "bbb"},
		{Name: "diverged", Version: 3, ContentHash: "ccc"},
	}
	toPull, skipped, conflicts, err := det.ClassifyEntries(context.Background(), remote)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(toPull) != 1 || toPull[0].Name != "new-skill" {
		t.Errorf("toPull = %+v, want [new-skill]", toPull)
	}
	if len(skipped) != 1 || skipped[0].Name != "existing-skill" {
		t.Errorf("skipped = %+v, want [existing-skill]", skipped)
	}
	if len(conflicts) != 1 || conflicts[0].Name != "diverged" {
		t.Errorf("conflicts = %+v, want [diverged]", conflicts)
	}
}

func TestConflictDetectorError(t *testing.T) {
	det := &fakeConflictDetector{err: errors.New("store unavailable")}
	_, _, _, err := det.ClassifyEntries(context.Background(), nil)
	if err == nil || err.Error() != "store unavailable" {
		t.Fatalf("expected 'store unavailable', got %v", err)
	}
}

func TestHubSyncServiceNotImplemented(t *testing.T) {
	svc := NewHubSyncService(nil, nil)
	_, err := svc.SyncFromPeer(context.Background(), "peer-x")
	if !errors.Is(err, ErrHubSyncNotImplemented) {
		t.Fatalf("expected ErrHubSyncNotImplemented, got %v", err)
	}
}

func TestHubSyncResultFields(t *testing.T) {
	r := &HubSyncResult{
		Pulled:    []HubIndexEntry{{Name: "a"}},
		Skipped:   []HubIndexEntry{{Name: "b"}},
		Conflicts: []HubIndexEntry{{Name: "c"}},
	}
	if len(r.Pulled) != 1 || r.Pulled[0].Name != "a" {
		t.Errorf("Pulled mismatch")
	}
	if len(r.Skipped) != 1 || r.Skipped[0].Name != "b" {
		t.Errorf("Skipped mismatch")
	}
	if len(r.Conflicts) != 1 || r.Conflicts[0].Name != "c" {
		t.Errorf("Conflicts mismatch")
	}
}

func TestHubIndexEntryJSONRoundTrip(t *testing.T) {
	entry := HubIndexEntry{
		Name:        "test-skill",
		Version:     5,
		ContentHash: "sha256:abcdef1234567890",
		Description: "Use when testing hub sync",
		Author:      "agent-1",
		BundleSHA:   "bsha256",
	}
	data, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got HubIndexEntry
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Name != entry.Name || got.Version != entry.Version || got.ContentHash != entry.ContentHash {
		t.Errorf("round-trip mismatch: %+v vs %+v", entry, got)
	}
	if got.Author != entry.Author || got.BundleSHA != entry.BundleSHA {
		t.Errorf("optional field mismatch: got author=%q bundle_sha=%q", got.Author, got.BundleSHA)
	}
}
