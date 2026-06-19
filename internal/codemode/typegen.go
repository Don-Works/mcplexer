package codemode

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// ToolDef holds the minimal tool definition needed for type generation.
type ToolDef struct {
	Name        string          // namespaced, e.g. "github__list_issues"
	Description string          // tool description, rendered as JSDoc on the function
	InputSchema json.RawMessage // JSON Schema for arguments
	Examples    []string        // optional code snippet examples (max 2, shown as @example in JSDoc)
}

// toolEntry holds a parsed tool within a namespace.
type toolEntry struct {
	name        string          // original name without namespace
	description string          // tool description for JSDoc
	schema      json.RawMessage // JSON Schema
	examples    []string        // optional code examples
}

// GenerateTypeScript converts aggregated MCP tool schemas into TypeScript
// declarations. Tools are grouped by namespace (split on "__") and each
// namespace becomes a `declare namespace` block.
func GenerateTypeScript(tools []ToolDef) string {
	groups := groupByNamespace(tools)

	var b strings.Builder
	b.WriteString("// Auto-generated MCPlexer Code API\n")
	b.WriteString("// Tool functions are synchronous — no await needed.\n\n")

	// Sort namespaces for deterministic output.
	nsNames := make([]string, 0, len(groups))
	for ns := range groups {
		nsNames = append(nsNames, ns)
	}
	sort.Strings(nsNames)

	for _, ns := range nsNames {
		entries := groups[ns]
		fmt.Fprintf(&b, "declare namespace %s {\n", ns)
		for i, entry := range entries {
			writeToolDeclaration(&b, entry)
			if i < len(entries)-1 {
				b.WriteByte('\n')
			}
		}
		b.WriteString("}\n\n")
	}

	// Global helpers.
	b.WriteString("/** Print output that will be returned to the caller. */\n")
	b.WriteString("declare function print(value: any): void;\n")
	b.WriteString("/** Execute up to 20 tool calls concurrently. Returns parsed results in the same order; null = failed call. */\n")
	b.WriteString("declare function parallel(calls: {tool: string, args: Record<string, any>}[]): (any | null)[];\n")
	b.WriteString("/** Prune nulls/empties and compress arrays into columnar tables. Use on large results before print(). */\n")
	b.WriteString("declare function compact(value: any): any;\n")

	return b.String()
}

// groupByNamespace splits tools on "__" into namespace → entries map.
func groupByNamespace(tools []ToolDef) map[string][]toolEntry {
	groups := make(map[string][]toolEntry)
	for _, t := range tools {
		ns, name, ok := strings.Cut(t.Name, "__")
		if !ok {
			ns = "_global"
			name = t.Name
		}
		groups[ns] = append(groups[ns], toolEntry{
			name:        name,
			description: t.Description,
			schema:      t.InputSchema,
			examples:    t.Examples,
		})
	}
	// Sort entries within each namespace.
	for ns := range groups {
		sort.Slice(groups[ns], func(i, j int) bool {
			return groups[ns][i].name < groups[ns][j].name
		})
	}
	return groups
}

// writeToolDeclaration writes a single function declaration with its
// parameter interface (if it has properties). Descriptions and examples
// are rendered as JSDoc annotations on the function and its parameters.
func writeToolDeclaration(b *strings.Builder, entry toolEntry) {
	paramType := schemaToParamType(entry.schema)

	if paramType.hasInterface {
		fmt.Fprintf(b, "  interface %sParams {\n", toPascalCase(entry.name))
		for _, field := range paramType.fields {
			if field.description != "" {
				fmt.Fprintf(b, "    /** %s */\n", field.description)
			}
			optional := "?"
			if field.required {
				optional = ""
			}
			fmt.Fprintf(b, "    %s%s: %s;\n", field.name, optional, field.tsType)
		}
		b.WriteString("  }\n")
	}

	// Emit JSDoc block with description and examples on the function.
	hasDoc := entry.description != "" || len(entry.examples) > 0
	if hasDoc {
		b.WriteString("  /**\n")
		if entry.description != "" {
			fmt.Fprintf(b, "   * %s\n", entry.description)
		}
		limit := min(len(entry.examples), 2)
		for _, ex := range entry.examples[:limit] {
			fmt.Fprintf(b, "   * @example %s\n", ex)
		}
		b.WriteString("   */\n")
	}

	if paramType.hasInterface {
		fmt.Fprintf(b, "  function %s(params: %sParams): any;\n",
			entry.name, toPascalCase(entry.name))
	} else {
		fmt.Fprintf(b, "  function %s(): any;\n", entry.name)
	}
}

// paramType describes the generated TypeScript parameter type.
type paramType struct {
	hasInterface bool
	fields       []fieldDef
}

// fieldDef describes a single field in a parameter interface.
type fieldDef struct {
	name        string
	description string
	tsType      string
	required    bool
}

// schemaToParamType parses a JSON Schema and returns a paramType.
func schemaToParamType(schema json.RawMessage) paramType {
	if len(schema) == 0 {
		return paramType{}
	}

	var s jsonSchema
	if err := json.Unmarshal(schema, &s); err != nil {
		return paramType{}
	}

	if len(s.Properties) == 0 {
		return paramType{}
	}

	reqSet := make(map[string]bool, len(s.Required))
	for _, r := range s.Required {
		reqSet[r] = true
	}

	// Sort property names for deterministic output.
	propNames := make([]string, 0, len(s.Properties))
	for name := range s.Properties {
		propNames = append(propNames, name)
	}
	sort.Strings(propNames)

	fields := make([]fieldDef, 0, len(propNames))
	for _, name := range propNames {
		propRaw := s.Properties[name]
		var propSchema jsonSchema
		if err := json.Unmarshal(propRaw, &propSchema); err != nil {
			continue
		}
		fields = append(fields, fieldDef{
			name:        name,
			description: propSchema.Description,
			tsType:      jsonSchemaToTS(propRaw),
			required:    reqSet[name],
		})
	}

	return paramType{hasInterface: true, fields: fields}
}

// jsonSchema represents the subset of JSON Schema we parse.
type jsonSchema struct {
	Type        json.RawMessage            `json:"type"`
	Description string                     `json:"description"`
	Properties  map[string]json.RawMessage `json:"properties"`
	Required    []string                   `json:"required"`
	Items       json.RawMessage            `json:"items"`
	Enum        []json.RawMessage          `json:"enum"`
	OneOf       []json.RawMessage          `json:"oneOf"`
	AnyOf       []json.RawMessage          `json:"anyOf"`
}

// jsonSchemaToTS converts a JSON Schema property to a TypeScript type string.
func jsonSchemaToTS(raw json.RawMessage) string {
	var s jsonSchema
	if err := json.Unmarshal(raw, &s); err != nil {
		return "any"
	}

	// Handle enum.
	if len(s.Enum) > 0 {
		parts := make([]string, len(s.Enum))
		for i, v := range s.Enum {
			parts[i] = string(v)
		}
		return strings.Join(parts, " | ")
	}

	// Handle oneOf/anyOf.
	if len(s.OneOf) > 0 {
		return unionTypes(s.OneOf)
	}
	if len(s.AnyOf) > 0 {
		return unionTypes(s.AnyOf)
	}

	// Parse type field — can be a string or array.
	typeName := parseTypeField(s.Type)

	switch typeName {
	case "string":
		return "string"
	case "number", "integer":
		return "number"
	case "boolean":
		return "boolean"
	case "null":
		return "null"
	case "array":
		if len(s.Items) > 0 {
			itemType := jsonSchemaToTS(s.Items)
			return itemType + "[]"
		}
		return "any[]"
	case "object":
		if len(s.Properties) > 0 {
			return objectTypeInline(s)
		}
		return "Record<string, any>"
	default:
		return "any"
	}
}

// parseTypeField extracts a type string from the JSON Schema "type" field,
// which can be either a string or an array of strings.
func parseTypeField(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var single string
	if err := json.Unmarshal(raw, &single); err == nil {
		return single
	}
	var arr []string
	if err := json.Unmarshal(raw, &arr); err == nil {
		// Filter out "null" and return the first real type.
		for _, t := range arr {
			if t != "null" {
				return t
			}
		}
		if len(arr) > 0 {
			return arr[0]
		}
	}
	return ""
}

// unionTypes builds a TypeScript union from oneOf/anyOf entries.
func unionTypes(schemas []json.RawMessage) string {
	parts := make([]string, len(schemas))
	for i, s := range schemas {
		parts[i] = jsonSchemaToTS(s)
	}
	return strings.Join(parts, " | ")
}

// objectTypeInline renders a nested object type inline: { key: Type; ... }
func objectTypeInline(s jsonSchema) string {
	reqSet := make(map[string]bool, len(s.Required))
	for _, r := range s.Required {
		reqSet[r] = true
	}

	propNames := make([]string, 0, len(s.Properties))
	for name := range s.Properties {
		propNames = append(propNames, name)
	}
	sort.Strings(propNames)

	parts := make([]string, len(propNames))
	for i, name := range propNames {
		opt := "?"
		if reqSet[name] {
			opt = ""
		}
		parts[i] = fmt.Sprintf("%s%s: %s", name, opt, jsonSchemaToTS(s.Properties[name]))
	}

	return "{ " + strings.Join(parts, "; ") + " }"
}

// toPascalCase converts snake_case to PascalCase.
func toPascalCase(s string) string {
	parts := strings.Split(s, "_")
	for i, p := range parts {
		if len(p) > 0 {
			parts[i] = strings.ToUpper(p[:1]) + p[1:]
		}
	}
	return strings.Join(parts, "")
}
