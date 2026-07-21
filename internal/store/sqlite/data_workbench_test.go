package sqlite

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

func TestDataWorkbenchIngestQuerySearchDrop(t *testing.T) {
	ctx := context.Background()
	db, err := New(ctx, ":memory:")
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	rows := []store.DataItem{
		{PayloadJSON: json.RawMessage(`{"state":"open","tier":"p0","title":"urgent customer outage"}`)},
		{PayloadJSON: json.RawMessage(`{"state":"done","tier":"p1","title":"routine cleanup"}`)},
		{PayloadJSON: json.RawMessage(`{"state":"open","tier":"p1","title":"follow up"}`)},
	}
	exp := time.Now().UTC().Add(time.Hour)
	coll := &store.DataCollection{
		WorkspaceID:  "ws-a",
		Name:         "issues",
		Kind:         store.DataWorkbenchKindTable,
		TagsJSON:     json.RawMessage(`["run-1"]`),
		SchemaJSON:   json.RawMessage(`{"columns":{"state":"string"}}`),
		TTLExpiresAt: &exp,
	}
	if err := db.IngestDataCollection(ctx, coll, rows); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if coll.RowCount != 3 {
		t.Fatalf("row count = %d, want 3", coll.RowCount)
	}

	list, err := db.ListDataCollections(ctx, store.DataCollectionFilter{
		WorkspaceID: "ws-a", Tags: []string{"run-1"},
	})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 || list[0].Name != "issues" {
		t.Fatalf("list = %+v, want issues", list)
	}

	got, err := db.QueryDataCollection(ctx, store.DataQuery{
		WorkspaceID: "ws-a",
		Name:        "issues",
		SQL:         "SELECT state, COUNT(*) AS c FROM issues GROUP BY state ORDER BY state",
	})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(got) != 2 || got[1]["state"] != "open" || got[1]["c"] != int64(2) {
		t.Fatalf("query rows = %#v, want open count 2", got)
	}

	hits, err := db.SearchDataCollection(ctx, store.DataSearch{
		WorkspaceID: "ws-a", Name: "issues", Query: "urgent", Limit: 5,
	})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) != 1 || hits[0].Ordinal != 0 {
		t.Fatalf("hits = %+v, want first row", hits)
	}

	if err := db.DropDataCollection(ctx, "ws-a", "issues"); err != nil {
		t.Fatalf("drop: %v", err)
	}
	if _, err := db.GetDataCollection(ctx, "ws-a", "issues"); err != store.ErrNotFound {
		t.Fatalf("get after drop err = %v, want ErrNotFound", err)
	}
}

func TestDataWorkbenchQueryRejectsWrites(t *testing.T) {
	ctx := context.Background()
	db, err := New(ctx, ":memory:")
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	err = db.IngestDataCollection(ctx, &store.DataCollection{
		WorkspaceID: "ws-a", Name: "rows", Kind: store.DataWorkbenchKindTable,
	}, []store.DataItem{{PayloadJSON: json.RawMessage(`{"a":1}`)}})
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	_, err = db.QueryDataCollection(ctx, store.DataQuery{
		WorkspaceID: "ws-a", Name: "rows", SQL: "DELETE FROM data",
	})
	if err == nil {
		t.Fatal("expected write query rejection")
	}
}
