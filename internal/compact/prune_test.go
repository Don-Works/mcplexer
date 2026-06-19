package compact

import (
	"encoding/json"
	"testing"
)

func TestPruneObject(t *testing.T) {
	tests := []struct {
		name string
		in   map[string]any
		want map[string]any
	}{
		{
			name: "removes null",
			in:   map[string]any{"id": float64(1), "deleted_at": nil},
			want: map[string]any{"id": float64(1)},
		},
		{
			name: "removes empty string",
			in:   map[string]any{"name": "ok", "gravatar_id": ""},
			want: map[string]any{"name": "ok"},
		},
		{
			name: "removes empty array",
			in:   map[string]any{"id": float64(1), "labels": []any{}},
			want: map[string]any{"id": float64(1)},
		},
		{
			name: "removes empty object",
			in:   map[string]any{"id": float64(1), "permissions": map[string]any{}},
			want: map[string]any{"id": float64(1)},
		},
		{
			name: "preserves false",
			in:   map[string]any{"locked": false, "draft": false},
			want: map[string]any{"locked": false, "draft": false},
		},
		{
			name: "preserves zero",
			in:   map[string]any{"comments": float64(0), "score": float64(0)},
			want: map[string]any{"comments": float64(0), "score": float64(0)},
		},
		{
			name: "preserves negative numbers",
			in:   map[string]any{"offset": float64(-1), "delta": float64(-0.5)},
			want: map[string]any{"offset": float64(-1), "delta": float64(-0.5)},
		},
		{
			name: "preserves non-empty string",
			in:   map[string]any{"state": "open", "empty": ""},
			want: map[string]any{"state": "open"},
		},
		{
			name: "preserves non-empty array",
			in:   map[string]any{"tags": []any{"bug", "urgent"}, "empty": []any{}},
			want: map[string]any{"tags": []any{"bug", "urgent"}},
		},
		{
			name: "recursive nested object",
			in: map[string]any{
				"user": map[string]any{
					"login": "octocat", "avatar_url": "https://...",
					"gravatar_id": "", "node_id": nil,
				},
			},
			want: map[string]any{
				"user": map[string]any{
					"login": "octocat", "avatar_url": "https://...",
				},
			},
		},
		{
			name: "deeply nested 3 levels",
			in: map[string]any{
				"a": map[string]any{
					"b": map[string]any{
						"c": map[string]any{
							"value": float64(42), "junk": nil,
						},
						"empty": "",
					},
					"keep": "yes",
				},
			},
			want: map[string]any{
				"a": map[string]any{
					"b": map[string]any{
						"c": map[string]any{"value": float64(42)},
					},
					"keep": "yes",
				},
			},
		},
		{
			name: "nested object becomes empty after pruning",
			in: map[string]any{
				"meta": map[string]any{
					"x": nil, "y": "", "z": []any{},
				},
				"id": float64(1),
			},
			want: map[string]any{"id": float64(1)},
		},
		{
			name: "arrays of objects pruned recursively",
			in: map[string]any{
				"items": []any{
					map[string]any{"id": float64(1), "junk": nil},
					map[string]any{"id": float64(2), "junk": nil},
				},
			},
			want: map[string]any{
				"items": []any{
					map[string]any{"id": float64(1)},
					map[string]any{"id": float64(2)},
				},
			},
		},
		{
			name: "array with mixed types preserves primitives",
			in: map[string]any{
				"data": []any{float64(1), "hello", nil, true},
			},
			want: map[string]any{
				"data": []any{float64(1), "hello", nil, true},
			},
		},
		{
			name: "preserves unicode strings",
			in:   map[string]any{"name": "\u4f60\u597d\u4e16\u754c", "empty": ""},
			want: map[string]any{"name": "\u4f60\u597d\u4e16\u754c"},
		},
		{
			name: "preserves large numbers",
			in:   map[string]any{"big": float64(9999999999), "zero": float64(0)},
			want: map[string]any{"big": float64(9999999999), "zero": float64(0)},
		},
		{
			name: "preserves float precision",
			in:   map[string]any{"pi": 3.14159, "empty": ""},
			want: map[string]any{"pi": 3.14159},
		},
		{
			name: "empty input",
			in:   map[string]any{},
			want: map[string]any{},
		},
		{
			name: "all fields empty",
			in:   map[string]any{"a": nil, "b": "", "c": []any{}, "d": map[string]any{}},
			want: map[string]any{},
		},
		{
			name: "preserves true boolean",
			in:   map[string]any{"admin": true, "verified": true},
			want: map[string]any{"admin": true, "verified": true},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := PruneObject(tt.in)
			assertJSONEqual(t, tt.want, got)
		})
	}
}

func TestPruneObjectFromJSON(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantKeys []string
		dropKeys []string
	}{
		{
			name:     "github user object",
			input:    `{"login":"octocat","id":1,"node_id":"MDQ6","avatar_url":"https://a","gravatar_id":"","url":"https://api","html_url":"https://g","type":"User","site_admin":false}`,
			wantKeys: []string{"login", "id", "node_id", "avatar_url", "url", "html_url", "type", "site_admin"},
			dropKeys: []string{"gravatar_id"},
		},
		{
			name:     "github reactions all zero",
			input:    `{"url":"https://...","total_count":0,"+1":0,"-1":0,"laugh":0,"hooray":0,"confused":0,"heart":0,"rocket":0,"eyes":0}`,
			wantKeys: []string{"url", "total_count", "+1", "-1", "laugh", "hooray", "confused", "heart", "rocket", "eyes"},
			dropKeys: nil,
		},
		{
			name:     "slack message with empty optional fields",
			input:    `{"type":"message","ts":"1234.5678","user":"U123","text":"hello","thread_ts":"","reply_count":0,"attachments":[],"blocks":[]}`,
			wantKeys: []string{"type", "ts", "user", "text", "reply_count"},
			dropKeys: []string{"thread_ts", "attachments", "blocks"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var obj map[string]any
			if err := json.Unmarshal([]byte(tt.input), &obj); err != nil {
				t.Fatal(err)
			}
			got := PruneObject(obj)
			for _, k := range tt.wantKeys {
				if _, ok := got[k]; !ok {
					t.Errorf("expected key %q to be preserved", k)
				}
			}
			for _, k := range tt.dropKeys {
				if _, ok := got[k]; ok {
					t.Errorf("expected key %q to be pruned", k)
				}
			}
		})
	}
}

func TestPruneForSandbox(t *testing.T) {
	tests := []struct {
		name     string
		in       map[string]any
		wantKeys []string
		dropKeys []string
	}{
		{
			name: "strips pagination keys",
			in: map[string]any{
				"items":           []any{"a", "b"},
				"next_cursor":     "abc123",
				"has_more":        true,
				"total_count":     float64(42),
				"next_page_token": "token",
			},
			wantKeys: []string{"items"},
			dropKeys: []string{"next_cursor", "has_more", "total_count", "next_page_token"},
		},
		{
			name: "strips nulls and empties like PruneObject",
			in: map[string]any{
				"id":    float64(1),
				"empty": "",
				"nil":   nil,
			},
			wantKeys: []string{"id"},
			dropKeys: []string{"empty", "nil"},
		},
		{
			name: "recursive pagination stripping",
			in: map[string]any{
				"data": map[string]any{
					"results":     []any{"x"},
					"page_info":   map[string]any{"cursor": "abc"},
					"next_cursor": "xyz",
				},
			},
			wantKeys: []string{"data"},
		},
		{
			name: "preserves non-pagination data",
			in: map[string]any{
				"id":     float64(1),
				"name":   "test",
				"active": true,
				"count":  float64(0),
				"page":   float64(3), // this is a pagination key
			},
			wantKeys: []string{"id", "name", "active", "count"},
			dropKeys: []string{"page"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := PruneForSandbox(tt.in)
			for _, k := range tt.wantKeys {
				if _, ok := got[k]; !ok {
					t.Errorf("expected key %q to be preserved", k)
				}
			}
			for _, k := range tt.dropKeys {
				if _, ok := got[k]; ok {
					t.Errorf("expected key %q to be pruned", k)
				}
			}
		})
	}
}
