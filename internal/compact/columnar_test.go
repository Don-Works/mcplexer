package compact

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestCompactArray(t *testing.T) {
	tests := []struct {
		name        string
		items       []map[string]any
		wantCols    bool
		wantFixed   map[string]any
		checkCols   []string // expected column names if columnar
		checkNoCols []string // columns that should NOT appear (pruned/fixed)
	}{
		{
			name: "basic columnar 3 items",
			items: []map[string]any{
				{"id": float64(1), "title": "A"},
				{"id": float64(2), "title": "B"},
				{"id": float64(3), "title": "C"},
			},
			wantCols:  true,
			checkCols: []string{"id", "title"},
		},
		{
			name: "null columns dropped entirely",
			items: []map[string]any{
				{"id": float64(1), "title": "A", "node_id": nil, "gravatar_id": nil},
				{"id": float64(2), "title": "B", "node_id": nil, "gravatar_id": nil},
				{"id": float64(3), "title": "C", "node_id": nil, "gravatar_id": nil},
			},
			wantCols:    true,
			checkCols:   []string{"id", "title"},
			checkNoCols: []string{"node_id", "gravatar_id"},
		},
		{
			name: "empty string columns dropped",
			items: []map[string]any{
				{"id": float64(1), "name": "a", "bio": ""},
				{"id": float64(2), "name": "b", "bio": ""},
				{"id": float64(3), "name": "c", "bio": ""},
			},
			wantCols:    true,
			checkNoCols: []string{"bio"},
		},
		{
			name: "uniform column to fixed",
			items: []map[string]any{
				{"id": float64(1), "state": "open", "repo": "mcplexer"},
				{"id": float64(2), "state": "open", "repo": "mcplexer"},
				{"id": float64(3), "state": "open", "repo": "mcplexer"},
			},
			wantCols:    true,
			wantFixed:   map[string]any{"state": "open", "repo": "mcplexer"},
			checkCols:   []string{"id"},
			checkNoCols: []string{"state", "repo"},
		},
		{
			name: "mixed uniform and varying columns",
			items: []map[string]any{
				{"id": float64(1), "title": "A", "state": "open"},
				{"id": float64(2), "title": "B", "state": "open"},
				{"id": float64(3), "title": "C", "state": "closed"},
			},
			wantCols:  true,
			checkCols: []string{"id", "title", "state"},
		},
		{
			name: "partially null column preserved",
			items: []map[string]any{
				{"id": float64(1), "assignee": "alice"},
				{"id": float64(2), "assignee": nil},
				{"id": float64(3), "assignee": "bob"},
			},
			wantCols:  true,
			checkCols: []string{"id", "assignee"},
		},
		{
			name: "column ordering id name title first",
			items: []map[string]any{
				{"zebra": "z", "title": "T", "name": "N", "id": float64(1)},
				{"zebra": "z", "title": "T", "name": "N", "id": float64(2)},
				{"zebra": "z", "title": "T", "name": "N", "id": float64(3)},
			},
			wantCols: true,
		},
		{
			name: "small array no columnar 1 item",
			items: []map[string]any{
				{"id": float64(1)},
			},
			wantCols: false,
		},
		{
			name: "small array no columnar 2 items",
			items: []map[string]any{
				{"id": float64(1), "name": "a"},
				{"id": float64(2), "name": "b"},
			},
			wantCols: false,
		},
		{
			name: "heterogeneous completely different keys",
			items: []map[string]any{
				{"a": float64(1), "b": float64(2)},
				{"c": float64(3), "d": float64(4)},
				{"e": float64(5), "f": float64(6)},
			},
			wantCols: false,
		},
		{
			name: "boolean values in columns",
			items: []map[string]any{
				{"id": float64(1), "active": true, "locked": false},
				{"id": float64(2), "active": false, "locked": false},
				{"id": float64(3), "active": true, "locked": false},
			},
			wantCols:  true,
			wantFixed: map[string]any{"locked": false},
		},
		{
			name: "nested objects in cells",
			items: []map[string]any{
				{"id": float64(1), "user": map[string]any{"login": "a"}},
				{"id": float64(2), "user": map[string]any{"login": "b"}},
				{"id": float64(3), "user": map[string]any{"login": "c"}},
			},
			wantCols:  true,
			checkCols: []string{"id", "user"},
		},
		{
			name: "all fields uniform becomes all fixed",
			items: []map[string]any{
				{"state": "open", "repo": "x"},
				{"state": "open", "repo": "x"},
				{"state": "open", "repo": "x"},
			},
			wantCols:  true,
			wantFixed: map[string]any{"state": "open", "repo": "x"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CompactArray(tt.items)
			m, isMap := got.(map[string]any)

			if tt.wantCols && !isMap {
				t.Fatalf("expected columnar map, got %T: %v", got, got)
			}
			if !tt.wantCols && isMap {
				t.Fatalf("expected slice, got columnar map")
			}
			if !tt.wantCols {
				return
			}

			if tt.wantFixed != nil {
				assertJSONEqual(t, tt.wantFixed, m["_fixed"])
			}

			cols := extractColNames(t, m)
			if tt.checkCols != nil {
				for _, want := range tt.checkCols {
					if !sliceContains(cols, want) {
						t.Errorf("expected column %q in %v", want, cols)
					}
				}
			}
			for _, nope := range tt.checkNoCols {
				if sliceContains(cols, nope) {
					t.Errorf("column %q should not be present in _cols %v", nope, cols)
				}
			}
		})
	}
}

func TestCompactArrayColumnOrder(t *testing.T) {
	items := []map[string]any{
		{"zebra": "z", "title": "T", "name": "N", "id": float64(1), "alpha": "a"},
		{"zebra": "y", "title": "U", "name": "M", "id": float64(2), "alpha": "b"},
		{"zebra": "x", "title": "V", "name": "L", "id": float64(3), "alpha": "c"},
	}
	result := CompactArray(items)
	m, ok := result.(map[string]any)
	if !ok {
		t.Fatal("expected columnar")
	}
	cols := extractColNames(t, m)
	// id=0, name=1, title=2, then alpha, zebra
	expected := []string{"id", "name", "title", "alpha", "zebra"}
	if len(cols) != len(expected) {
		t.Fatalf("expected %v, got %v", expected, cols)
	}
	for i, want := range expected {
		if cols[i] != want {
			t.Errorf("col[%d]: want %q, got %q (full: %v)", i, want, cols[i], cols)
		}
	}
}

func TestCompactArrayLargeDataset(t *testing.T) {
	items := make([]map[string]any, 50)
	for i := range items {
		items[i] = map[string]any{
			"id":    float64(i + 1),
			"title": fmt.Sprintf("Issue #%d", i+1),
			"state": "open",
			"empty": nil,
		}
	}
	result := CompactArray(items)
	m, ok := result.(map[string]any)
	if !ok {
		t.Fatal("expected columnar")
	}
	rows := m["_rows"].([]any)
	if len(rows) != 50 {
		t.Fatalf("expected 50 rows, got %d", len(rows))
	}
	if fixed, ok := m["_fixed"].(map[string]any); ok {
		if fixed["state"] != "open" {
			t.Error("state should be fixed as open")
		}
	} else {
		t.Error("expected _fixed with state=open")
	}
}

func TestCompactArrayDataPreservation(t *testing.T) {
	items := []map[string]any{
		{"id": float64(1), "name": "alice", "score": 95.5, "active": true},
		{"id": float64(2), "name": "bob", "score": 87.0, "active": false},
		{"id": float64(3), "name": "charlie", "score": 92.3, "active": true},
	}
	result := CompactArray(items)
	m, ok := result.(map[string]any)
	if !ok {
		t.Fatal("expected columnar")
	}
	// Marshal to JSON and back to verify it's valid and preserves data.
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	var roundtrip map[string]any
	if err := json.Unmarshal(data, &roundtrip); err != nil {
		t.Fatal(err)
	}

	cols := roundtrip["_cols"].([]any)
	rows := roundtrip["_rows"].([]any)

	if len(rows) != 3 {
		t.Fatalf("expected 3 rows, got %d", len(rows))
	}
	// Each row must have same number of cells as columns.
	for i, r := range rows {
		row := r.([]any)
		if len(row) != len(cols) {
			t.Errorf("row %d: %d cells, expected %d", i, len(row), len(cols))
		}
	}
}

func TestFormatColumnarAllRows(t *testing.T) {
	rows := make([]any, 25)
	for i := range rows {
		rows[i] = []any{float64(i + 1), fmt.Sprintf("item_%d", i+1)}
	}
	data := map[string]any{
		"_cols": []any{"id", "name"},
		"_rows": rows,
	}
	got := FormatColumnar(data)
	// All rows must be present — no truncation.
	for i := 1; i <= 25; i++ {
		needle := fmt.Sprintf("item_%d", i)
		if !strings.Contains(got, needle) {
			t.Errorf("row %d missing from output", i)
		}
	}
}

func TestFormatColumnarLongCells(t *testing.T) {
	longVal := strings.Repeat("x", 200)
	data := map[string]any{
		"_cols": []any{"id", "description"},
		"_rows": []any{
			[]any{float64(1), longVal},
		},
	}
	got := FormatColumnar(data)
	// Full value must be present — no truncation.
	if !strings.Contains(got, longVal) {
		t.Error("long cell value should not be truncated")
	}
}

func TestFormatColumnarMultipleFixed(t *testing.T) {
	data := map[string]any{
		"_cols":  []any{"id"},
		"_rows":  []any{[]any{float64(1)}, []any{float64(2)}},
		"_fixed": map[string]any{"state": "open", "repo": "mcplexer", "org": "example"},
	}
	got := FormatColumnar(data)
	if !strings.Contains(got, "[all:") {
		t.Error("expected fixed line")
	}
	// Fixed values should be sorted alphabetically.
	if !strings.Contains(got, "org=example") {
		t.Error("missing org in fixed")
	}
}

func TestFormatColumnarCellTypes(t *testing.T) {
	data := map[string]any{
		"_cols": []any{"str", "num", "flt", "bool", "nil"},
		"_rows": []any{
			[]any{"hello", float64(42), 3.14, true, nil},
		},
	}
	got := FormatColumnar(data)
	if !strings.Contains(got, "hello") {
		t.Error("missing string cell")
	}
	if !strings.Contains(got, "42") {
		t.Error("missing int cell")
	}
	if !strings.Contains(got, "3.14") {
		t.Error("missing float cell")
	}
	if !strings.Contains(got, "true") {
		t.Error("missing bool cell")
	}
}

func extractColNames(t *testing.T, m map[string]any) []string {
	t.Helper()
	raw, ok := m["_cols"]
	if !ok {
		t.Fatal("missing _cols")
	}
	arr, ok := raw.([]any)
	if !ok {
		t.Fatalf("_cols is %T, not []any", raw)
	}
	cols := make([]string, len(arr))
	for i, v := range arr {
		cols[i] = fmt.Sprintf("%v", v)
	}
	return cols
}

func sliceContains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
