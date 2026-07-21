// memory_entities_handler_test.go — HTTP-level coverage for the entity
// surface added in migration 076. Uses the same sqlite-backed test
// server as memory_handler_test.go so the path mirrors prod.
package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"testing"
)

// postJSONEntity sends a JSON body and returns the raw response. Tests assert
// status code + body shape themselves.
func postJSONEntity(t *testing.T, url string, body any) *http.Response {
	t.Helper()
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(body); err != nil {
		t.Fatalf("encode: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, url, &buf)
	if err != nil {
		t.Fatalf("new req: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	return resp
}

func deleteJSON(t *testing.T, url string, body any) *http.Response {
	t.Helper()
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(body); err != nil {
		t.Fatalf("encode: %v", err)
	}
	req, err := http.NewRequest(http.MethodDelete, url, &buf)
	if err != nil {
		t.Fatalf("new req: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE %s: %v", url, err)
	}
	return resp
}

func TestMemoryCreateWithEntities(t *testing.T) {
	srv, _, _ := newMemoryTestServer(t)
	body := map[string]any{
		"name":    "decision-1",
		"content": "we picked vec0 for the vector store",
		"kind":    "note",
		"entities": []map[string]any{
			{"kind": "task", "id": "01KSHKEDJJ"},
			{"kind": "person", "id": "maintainer@example.com"},
		},
	}
	resp := postJSONEntity(t, srv.URL+"/api/v1/memory", body)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("create status=%d body=%s", resp.StatusCode, raw)
	}
	var created map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatalf("decode created: %v", err)
	}
	id, _ := created["id"].(string)
	if id == "" {
		t.Fatalf("created has no id: %+v", created)
	}
	// GET entities for that memory.
	r2, err := http.Get(srv.URL + "/api/v1/memory/" + id + "/entities")
	if err != nil {
		t.Fatalf("get entities: %v", err)
	}
	defer func() { _ = r2.Body.Close() }()
	if r2.StatusCode != http.StatusOK {
		t.Fatalf("get entities status=%d", r2.StatusCode)
	}
	var links []map[string]any
	if err := json.NewDecoder(r2.Body).Decode(&links); err != nil {
		t.Fatalf("decode links: %v", err)
	}
	if len(links) != 2 {
		t.Fatalf("expected 2 links, got %d: %+v", len(links), links)
	}
}

func TestMemoryEntitiesLinkUnlinkIdempotent(t *testing.T) {
	srv, _, _ := newMemoryTestServer(t)
	create := postJSONEntity(t, srv.URL+"/api/v1/memory", map[string]any{
		"name": "n1", "content": "entity link memory body",
	})
	defer func() { _ = create.Body.Close() }()
	var created map[string]any
	_ = json.NewDecoder(create.Body).Decode(&created)
	id, _ := created["id"].(string)

	url := srv.URL + "/api/v1/memory/" + id + "/entities"
	link := map[string]any{"kind": "task", "id": "T1"}

	// Two links — second is idempotent.
	for i := 0; i < 2; i++ {
		r := postJSONEntity(t, url, link)
		_ = r.Body.Close()
		if r.StatusCode != http.StatusNoContent {
			t.Fatalf("link %d: status=%d", i, r.StatusCode)
		}
	}
	r3, _ := http.Get(url)
	var links []map[string]any
	_ = json.NewDecoder(r3.Body).Decode(&links)
	_ = r3.Body.Close()
	if len(links) != 1 {
		t.Fatalf("idempotent: want 1 link, got %d", len(links))
	}

	// Unlink (role omitted → broad delete).
	r4 := deleteJSON(t, url, link)
	_ = r4.Body.Close()
	if r4.StatusCode != http.StatusNoContent {
		t.Fatalf("unlink status=%d", r4.StatusCode)
	}
	r5, _ := http.Get(url)
	var after []map[string]any
	_ = json.NewDecoder(r5.Body).Decode(&after)
	_ = r5.Body.Close()
	if len(after) != 0 {
		t.Fatalf("after unlink: want 0, got %d", len(after))
	}
}

func TestMemorySearchByEntities(t *testing.T) {
	srv, _, _ := newMemoryTestServer(t)
	// Two memories, one tagged with task:T + person:alice; one with just task:T.
	idAB := mustCreateFromURL(t, srv.URL, "n-AB", "about both",
		[]map[string]any{
			{"kind": "task", "id": "T"},
			{"kind": "person", "id": "alice@x"},
		})
	idA := mustCreateFromURL(t, srv.URL, "n-A", "about just task",
		[]map[string]any{{"kind": "task", "id": "T"}})

	// AND filter — only n-AB.
	hits := mustSearchMemory(t, srv.URL+"/api/v1/memory/search", map[string]any{
		"entities": []map[string]any{
			{"kind": "task", "id": "T"},
			{"kind": "person", "id": "alice@x"},
		},
	})
	if len(hits) != 1 {
		t.Fatalf("AND: want 1, got %d", len(hits))
	}
	gotID, _ := getNested(hits[0], "entry", "id").(string)
	if gotID == "" {
		gotID, _ = hits[0]["id"].(string)
	}
	if gotID != idAB {
		t.Fatalf("AND: want %s, got %s", idAB, gotID)
	}
	// OR filter — both.
	hits = mustSearchMemory(t, srv.URL+"/api/v1/memory/search", map[string]any{
		"entities_any": []map[string]any{
			{"kind": "task", "id": "T"},
		},
	})
	if len(hits) != 2 {
		t.Fatalf("OR: want 2, got %d (idA=%s idAB=%s)", len(hits), idA, idAB)
	}
}

func TestListEntitiesAggregates(t *testing.T) {
	srv, _, _ := newMemoryTestServer(t)
	mustCreateFromURL(t, srv.URL, "m1", "entity aggregate body one", []map[string]any{
		{"kind": "task", "id": "HOT"},
		{"kind": "person", "id": "alice"},
	})
	mustCreateFromURL(t, srv.URL, "m2", "entity aggregate body two", []map[string]any{
		{"kind": "task", "id": "HOT"},
	})
	mustCreateFromURL(t, srv.URL, "m3", "entity aggregate body three", []map[string]any{
		{"kind": "task", "id": "COLD"},
	})
	resp, err := http.Get(srv.URL + "/api/v1/memory/entities")
	if err != nil {
		t.Fatalf("list entities: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	var rows []map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&rows)
	if len(rows) < 3 {
		t.Fatalf("expected 3+ entities, got %d: %+v", len(rows), rows)
	}
	// Top entry must be task:hot with count 2.
	top := rows[0]
	if top["kind"] != "task" || top["id"] != "hot" {
		t.Fatalf("top entity = %+v, want task:hot", top)
	}
	if n, _ := top["memory_count"].(float64); int(n) != 2 {
		t.Fatalf("top memory_count = %v, want 2", top["memory_count"])
	}
}

func mustCreateFromURL(t *testing.T, base, name, content string,
	entities []map[string]any,
) string {
	t.Helper()
	body := map[string]any{
		"name":     name,
		"content":  content,
		"entities": entities,
	}
	var buf bytes.Buffer
	_ = json.NewEncoder(&buf).Encode(body)
	resp, err := http.Post(base+"/api/v1/memory", "application/json", &buf)
	if err != nil {
		t.Fatalf("create %s: %v", name, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("create %s: status=%d body=%s", name, resp.StatusCode, raw)
	}
	var got map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&got)
	id, _ := got["id"].(string)
	if id == "" {
		t.Fatalf("no id for %s: %+v", name, got)
	}
	return id
}

func getNested(m map[string]any, path ...string) any {
	var cur any = m
	for _, k := range path {
		mm, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = mm[k]
	}
	return cur
}
