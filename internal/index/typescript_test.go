package index

import (
	"testing"
)

func TestExtractTSSymbols(t *testing.T) {
	ex := extractTS("sample.ts", readFixture(t, "testdata/ts/sample.ts"), "typescript")
	cases := []struct {
		name     string
		kind     string
		exported bool
	}{
		{"UserId", "type", true},
		{"User", "interface", true},
		{"Role", "enum", true},
		{"loadUser", "func", true},
		{"fetchUser", "func", true},
		{"MAX_USERS", "const", true},
		{"internalHelper", "func", false},
		{"lazy", "func", false},
	}
	for _, c := range cases {
		sym, ok := symByName(ex.Symbols, c.name)
		if !ok {
			t.Errorf("symbol %q not extracted", c.name)
			continue
		}
		if sym.Kind != c.kind {
			t.Errorf("%s kind = %q, want %q", c.name, sym.Kind, c.kind)
		}
		if sym.Exported != c.exported {
			t.Errorf("%s exported = %v, want %v", c.name, sym.Exported, c.exported)
		}
	}
}

func TestExtractTSImports(t *testing.T) {
	ex := extractTS("sample.ts", readFixture(t, "testdata/ts/sample.ts"), "typescript")
	got := map[string]bool{}
	for _, imp := range ex.Imports {
		got[imp] = true
	}
	for _, want := range []string{
		"react", "./config", "./util/helper", "@/aliased/module", "./legacy", "./lazy/mod",
	} {
		if !got[want] {
			t.Errorf("missing import %q (got %v)", want, ex.Imports)
		}
	}
}

func TestExtractTSXComponents(t *testing.T) {
	ex := extractTS("Widget.tsx", readFixture(t, "testdata/ts/Widget.tsx"), "typescript")
	cases := []struct {
		name     string
		kind     string
		exported bool
	}{
		{"Widget", "component", true},
		{"Panel", "component", true},
		{"LegacyWidget", "class", true},
		{"lowercaseHelper", "func", false},
	}
	for _, c := range cases {
		sym, ok := symByName(ex.Symbols, c.name)
		if !ok {
			t.Errorf("symbol %q not extracted", c.name)
			continue
		}
		if sym.Kind != c.kind {
			t.Errorf("%s kind = %q, want %q", c.name, sym.Kind, c.kind)
		}
		if sym.Exported != c.exported {
			t.Errorf("%s exported = %v, want %v", c.name, sym.Exported, c.exported)
		}
	}
}

func TestExtractTSRelativeAndAlias(t *testing.T) {
	// Alias specifiers stay external; relative specifiers resolve against the
	// in-memory file set.
	enum := map[string]bool{"web/src/config.ts": true, "web/src/util/helper.ts": true}
	aliasEdge := resolveTSImport("web/src/sample.ts", "@/aliased/module", enum, "ws")
	if aliasEdge.edge.ToPath != "" || !aliasEdge.alias {
		t.Errorf("alias import should be external+flagged, got %+v", aliasEdge)
	}
	rel := resolveTSImport("web/src/sample.ts", "./config", enum, "ws")
	if rel.edge.ToPath != "web/src/config.ts" {
		t.Errorf("relative import resolved to %q, want web/src/config.ts", rel.edge.ToPath)
	}
	idx := map[string]bool{"web/src/util/index.ts": true}
	resolved := resolveTSImport("web/src/app.ts", "./util", idx, "ws")
	if resolved.edge.ToPath != "web/src/util/index.ts" {
		t.Errorf("index resolution = %q, want web/src/util/index.ts", resolved.edge.ToPath)
	}
}
