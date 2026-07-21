package gateway

import (
	"encoding/json"
	"testing"
)

// The 2026-07 quality-first contract: schema minification removes ONLY inert
// keys ($schema, $id, $comment, title). Property descriptions, defaults,
// examples, enum, additionalProperties, and numeric constraints are all
// load-bearing for correct tool calls and must survive.
func TestMinifyToolSchemas(t *testing.T) {
	tests := []struct {
		name     string
		schema   string
		wantKeys []string // keys that must exist in the first property
		noKeys   []string // keys that must NOT exist in the first property
	}{
		{
			name: "preserves property descriptions",
			schema: `{
				"type": "object",
				"properties": {
					"query": {
						"type": "string",
						"description": "The search query"
					}
				},
				"required": ["query"]
			}`,
			wantKeys: []string{"type", "description"},
		},
		{
			name: "preserves enum, default, and description",
			schema: `{
				"type": "object",
				"properties": {
					"mode": {
						"type": "string",
						"enum": ["fast", "slow"],
						"description": "The mode to use",
						"default": "fast"
					}
				},
				"required": ["mode"]
			}`,
			wantKeys: []string{"type", "enum", "description", "default"},
		},
		{
			name: "handles nested objects, keeps nested descriptions",
			schema: `{
				"type": "object",
				"properties": {
					"config": {
						"type": "object",
						"description": "Configuration object",
						"properties": {
							"name": {
								"type": "string",
								"description": "The name"
							}
						}
					}
				}
			}`,
			wantKeys: []string{"type", "properties", "description"},
		},
		{
			name: "preserves constraints, strips only title",
			schema: `{
				"type": "object",
				"properties": {
					"count": {
						"type": "integer",
						"minimum": 0,
						"maximum": 100,
						"description": "Item count",
						"title": "Count"
					}
				}
			}`,
			wantKeys: []string{"type", "minimum", "maximum", "description"},
			noKeys:   []string{"title"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tools := []Tool{{
				Name:        "test_tool",
				Description: "A test tool",
				InputSchema: json.RawMessage(tt.schema),
			}}

			result := minifyToolSchemas(tools)
			if len(result) != 1 {
				t.Fatalf("got %d tools, want 1", len(result))
			}

			if result[0].Description != "A test tool" {
				t.Errorf("tool description = %q, want %q", result[0].Description, "A test tool")
			}

			var schema struct {
				Properties map[string]map[string]json.RawMessage `json:"properties"`
				Required   []string                              `json:"required"`
			}
			if err := json.Unmarshal(result[0].InputSchema, &schema); err != nil {
				t.Fatalf("unmarshal result: %v", err)
			}

			for _, prop := range schema.Properties {
				for _, key := range tt.wantKeys {
					if _, ok := prop[key]; !ok {
						t.Errorf("missing key %q in property", key)
					}
				}
				for _, key := range tt.noKeys {
					if _, ok := prop[key]; ok {
						t.Errorf("unexpected key %q in property", key)
					}
				}
				break // only check first property
			}
		})
	}
}

func TestMinifySchema_InvalidJSON(t *testing.T) {
	raw := json.RawMessage(`not valid json`)
	result := minifySchema(raw)
	if string(result) != string(raw) {
		t.Errorf("invalid JSON should be returned unchanged, got %s", result)
	}
}

func TestMinifySchema_NoiseKeysStripped(t *testing.T) {
	raw := json.RawMessage(`{
		"$schema": "https://json-schema.org/draft/2020-12/schema",
		"$id": "https://example.com/schema",
		"type": "object",
		"title": "MySchema",
		"description": "Top-level description",
		"additionalProperties": false,
		"properties": {}
	}`)
	result := minifySchema(raw)
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(result, &obj); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"$schema", "$id", "title"} {
		if _, ok := obj[key]; ok {
			t.Errorf("expected noise key %q to be stripped", key)
		}
	}
	// Semantic keys survive: strict-mode marker and top-level description.
	for _, key := range []string{"type", "description", "additionalProperties"} {
		if _, ok := obj[key]; !ok {
			t.Errorf("semantic key %q must be preserved", key)
		}
	}
	if string(obj["additionalProperties"]) != "false" {
		t.Errorf("boolean additionalProperties must round-trip exactly, got %s",
			obj["additionalProperties"])
	}
}

func TestMinifySchema_ArrayItemsKeepSemantics(t *testing.T) {
	raw := json.RawMessage(`{
		"type": "object",
		"properties": {
			"tags": {
				"type": "array",
				"description": "List of tags",
				"items": {
					"type": "string",
					"description": "A tag",
					"title": "Tag",
					"examples": ["foo"]
				}
			}
		}
	}`)
	result := minifySchema(raw)
	var schema struct {
		Properties map[string]struct {
			Description string                     `json:"description"`
			Items       map[string]json.RawMessage `json:"items"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(result, &schema); err != nil {
		t.Fatal(err)
	}
	tags := schema.Properties["tags"]
	if tags.Description != "List of tags" {
		t.Error("array property description must be preserved")
	}
	items := tags.Items
	for _, key := range []string{"type", "description", "examples"} {
		if _, ok := items[key]; !ok {
			t.Errorf("items %q must be preserved", key)
		}
	}
	if _, ok := items["title"]; ok {
		t.Error("items title (noise) should be stripped")
	}
}

func TestMinifySchema_Combinators(t *testing.T) {
	raw := json.RawMessage(`{
		"type": "object",
		"properties": {
			"value": {
				"oneOf": [
					{"type": "string", "title": "S", "description": "as string"},
					{"type": "integer", "title": "I", "description": "as int"}
				]
			}
		}
	}`)
	result := minifySchema(raw)
	var schema struct {
		Properties map[string]struct {
			OneOf []map[string]json.RawMessage `json:"oneOf"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(result, &schema); err != nil {
		t.Fatal(err)
	}
	branches := schema.Properties["value"].OneOf
	if len(branches) != 2 {
		t.Fatalf("oneOf branches = %d, want 2", len(branches))
	}
	for i, b := range branches {
		if _, ok := b["description"]; !ok {
			t.Errorf("oneOf[%d] description must be preserved", i)
		}
		if _, ok := b["title"]; ok {
			t.Errorf("oneOf[%d] title should be stripped", i)
		}
	}
}
