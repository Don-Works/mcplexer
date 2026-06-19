package gateway

import (
	"encoding/json"
	"testing"
)

func TestMinifyToolSchemas(t *testing.T) {
	tests := []struct {
		name     string
		schema   string
		wantKeys []string // keys that must exist in the first property
		noKeys   []string // keys that must NOT exist in the first property
	}{
		{
			name: "strips property descriptions",
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
			wantKeys: []string{"type"},
			noKeys:   []string{"description"},
		},
		{
			name: "strips additionalProperties and $schema",
			schema: `{
				"$schema": "https://json-schema.org/draft/2020-12/schema",
				"type": "object",
				"additionalProperties": false,
				"properties": {
					"id": {"type": "string", "description": "unique ID"}
				}
			}`,
			wantKeys: []string{"type"},
			noKeys:   []string{"description"},
		},
		{
			name: "preserves enum and required",
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
			wantKeys: []string{"type", "enum"},
			noKeys:   []string{"description", "default"},
		},
		{
			name: "handles nested objects",
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
			wantKeys: []string{"type", "properties"},
			noKeys:   []string{"description"},
		},
		{
			name: "preserves constraint fields",
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
			wantKeys: []string{"type", "minimum", "maximum"},
			noKeys:   []string{"description", "title"},
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

			// Tool description should be preserved (it's on the tool, not in the schema).
			if result[0].Description != "A test tool" {
				t.Errorf("tool description = %q, want %q", result[0].Description, "A test tool")
			}

			// Parse the minified schema and check the first property.
			var schema struct {
				Properties map[string]map[string]json.RawMessage `json:"properties"`
				Required   []string                              `json:"required"`
			}
			if err := json.Unmarshal(result[0].InputSchema, &schema); err != nil {
				t.Fatalf("unmarshal result: %v", err)
			}

			// Check first property.
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

func TestMinifySchema_TopLevelFieldsStripped(t *testing.T) {
	raw := json.RawMessage(`{
		"$schema": "https://json-schema.org/draft/2020-12/schema",
		"type": "object",
		"title": "MySchema",
		"additionalProperties": false,
		"properties": {}
	}`)
	result := minifySchema(raw)
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(result, &obj); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"$schema", "title", "additionalProperties"} {
		if _, ok := obj[key]; ok {
			t.Errorf("expected top-level key %q to be stripped", key)
		}
	}
	if _, ok := obj["type"]; !ok {
		t.Error("type field should be preserved")
	}
}

func TestMinifySchema_ArrayItems(t *testing.T) {
	raw := json.RawMessage(`{
		"type": "object",
		"properties": {
			"tags": {
				"type": "array",
				"description": "List of tags",
				"items": {
					"type": "string",
					"description": "A tag",
					"examples": ["foo"]
				}
			}
		}
	}`)
	result := minifySchema(raw)
	var schema struct {
		Properties map[string]struct {
			Items map[string]json.RawMessage `json:"items"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(result, &schema); err != nil {
		t.Fatal(err)
	}
	items := schema.Properties["tags"].Items
	if _, ok := items["description"]; ok {
		t.Error("items description should be stripped")
	}
	if _, ok := items["examples"]; ok {
		t.Error("items examples should be stripped")
	}
	if _, ok := items["type"]; !ok {
		t.Error("items type should be preserved")
	}
}
