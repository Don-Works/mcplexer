//go:build p2p

package p2p

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestReadIndexResponseParsesEntries(t *testing.T) {
	entries := []HubIndexEntry{
		{Name: "deploy-fly", Version: 3, ContentHash: "abc123"},
		{Name: "pdf-extract", Version: 1, ContentHash: "def456"},
	}
	resp := HubIndexResponse{Entries: entries}
	data, _ := json.Marshal(resp)
	data = append(data, '\n')
	br := bufio.NewReader(bytes.NewReader(data))
	got, err := readIndexResponse(br)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(got))
	}
	if got[0].Name != "deploy-fly" {
		t.Errorf("entry[0].Name = %q, want %q", got[0].Name, "deploy-fly")
	}
	if got[1].ContentHash != "def456" {
		t.Errorf("entry[1].ContentHash = %q, want %q", got[1].ContentHash, "def456")
	}
}

func TestReadIndexResponseEmptyIndex(t *testing.T) {
	resp := HubIndexResponse{Entries: nil}
	data, _ := json.Marshal(resp)
	data = append(data, '\n')
	br := bufio.NewReader(bytes.NewReader(data))
	got, err := readIndexResponse(br)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected 0 entries, got %d", len(got))
	}
}

func TestReadIndexResponseErrorLine(t *testing.T) {
	errJSON := `{"type":"error","code":"denied","message":"scope required"}` + "\n"
	br := bufio.NewReader(strings.NewReader(errJSON))
	_, err := readIndexResponse(br)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "denied") {
		t.Errorf("error should mention denied: %v", err)
	}
}

func TestReadIndexResponseBadJSON(t *testing.T) {
	br := bufio.NewReader(strings.NewReader("not-json\n"))
	_, err := readIndexResponse(br)
	if err == nil {
		t.Fatal("expected error for bad JSON")
	}
}

func TestHubIndexRequestType(t *testing.T) {
	req := HubIndexRequest{Type: "index"}
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !bytes.Contains(data, []byte(`"type":"index"`)) {
		t.Errorf("expected type:index in %q", data)
	}
}

func TestHubIndexResponseLargeCatalog(t *testing.T) {
	entries := make([]HubIndexEntry, 100)
	for i := range entries {
		entries[i] = HubIndexEntry{
			Name:        fmt.Sprintf("skill-%03d", i),
			Version:     i + 1,
			ContentHash: fmt.Sprintf("hash-%03d", i),
		}
	}
	resp := HubIndexResponse{Entries: entries}
	data, _ := json.Marshal(resp)
	data = append(data, '\n')
	br := bufio.NewReader(bytes.NewReader(data))
	got, err := readIndexResponse(br)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 100 {
		t.Fatalf("expected 100 entries, got %d", len(got))
	}
	if got[99].Name != "skill-099" {
		t.Errorf("last entry = %q, want %q", got[99].Name, "skill-099")
	}
}

func TestReadSearchResponse(t *testing.T) {
	raw := `{"hits":[{"name":"deploy-fly","version":3,"score":0.75,"content_hash":"sha256:abc","description":"Use when deploying to Fly.io","scope":"global"}]}` + "\n"
	hits, err := readSearchResponse(bufio.NewReader(strings.NewReader(raw)))
	if err != nil {
		t.Fatalf("read search response: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("expected 1 hit, got %d", len(hits))
	}
	if hits[0].Name != "deploy-fly" || hits[0].Version != 3 {
		t.Fatalf("unexpected hit: %+v", hits[0])
	}
	if hits[0].ContentHash != "sha256:abc" || hits[0].Scope != "global" {
		t.Fatalf("metadata did not round-trip: %+v", hits[0])
	}
}

func TestReadSearchResponseRemoteError(t *testing.T) {
	raw := `{"type":"error","code":"bad_request","message":"q is required"}` + "\n"
	_, err := readSearchResponse(bufio.NewReader(strings.NewReader(raw)))
	if err == nil || !strings.Contains(err.Error(), "q is required") {
		t.Fatalf("expected remote error, got %v", err)
	}
}
