package index

import (
	"os"
	"strings"
	"testing"

	"github.com/don-works/mcplexer/internal/store"
)

func readFixture(t *testing.T, rel string) []byte {
	t.Helper()
	data, err := os.ReadFile(rel)
	if err != nil {
		t.Fatalf("read fixture %s: %v", rel, err)
	}
	return data
}

func symByName(syms []store.CodeIndexSymbol, name string) (store.CodeIndexSymbol, bool) {
	for _, s := range syms {
		if s.Name == name {
			return s, true
		}
	}
	return store.CodeIndexSymbol{}, false
}

func TestExtractGoSymbols(t *testing.T) {
	ex := extractGo("sample.go", readFixture(t, "testdata/golang/sample.go"))
	if ex.ParseError != "" {
		t.Fatalf("unexpected parse error: %s", ex.ParseError)
	}
	if ex.Package != "sample" {
		t.Errorf("package = %q, want sample", ex.Package)
	}
	if !strings.Contains(ex.DocSummary, "fixture") {
		t.Errorf("doc summary = %q, want it to mention the fixture", ex.DocSummary)
	}
	if ex.LineCount == 0 {
		t.Error("line count should be > 0")
	}

	cases := []struct {
		name     string
		kind     string
		receiver string
		exported bool
	}{
		{"HandleKVSet", "func", "", true},
		{"Get", "method", "Store", true},
		{"Name", "method", "Handler", true},
		{"describe", "func", "", false},
		{"Store", "type", "", true},
		{"Handler", "type", "", true},
		{"Reader", "interface", "", true},
		{"MaxItems", "const", "", true},
		{"StatusOpen", "const", "", true},
		{"statusDone", "const", "", false},
		{"DefaultName", "var", "", true},
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
		if sym.Receiver != c.receiver {
			t.Errorf("%s receiver = %q, want %q", c.name, sym.Receiver, c.receiver)
		}
		if sym.Exported != c.exported {
			t.Errorf("%s exported = %v, want %v", c.name, sym.Exported, c.exported)
		}
	}
}

func TestExtractGoSignatureAndDoc(t *testing.T) {
	ex := extractGo("sample.go", readFixture(t, "testdata/golang/sample.go"))
	sym, ok := symByName(ex.Symbols, "HandleKVSet")
	if !ok {
		t.Fatal("HandleKVSet not found")
	}
	wantSig := "func HandleKVSet(ctx context.Context, key, value string) (bool, error)"
	if sym.Signature != wantSig {
		t.Errorf("signature = %q, want %q", sym.Signature, wantSig)
	}
	if !strings.Contains(sym.Doc, "stores a value") {
		t.Errorf("doc = %q, want it to describe the function", sym.Doc)
	}
	if strings.Contains(sym.Signature, "{") {
		t.Errorf("signature should stop before the body brace: %q", sym.Signature)
	}
}

func TestExtractGoImports(t *testing.T) {
	ex := extractGo("sample.go", readFixture(t, "testdata/golang/sample.go"))
	want := map[string]bool{"context": true, "fmt": true}
	got := map[string]bool{}
	for _, imp := range ex.Imports {
		got[imp] = true
	}
	for w := range want {
		if !got[w] {
			t.Errorf("missing import %q (got %v)", w, ex.Imports)
		}
	}
}

func TestExtractGoParseErrorDegrades(t *testing.T) {
	ex := extractGo("broken.go", readFixture(t, "testdata/golang/broken.go.txt"))
	if ex.ParseError == "" {
		t.Error("expected a parse error for the broken fixture")
	}
	if ex.Package != "broken" {
		t.Errorf("package = %q, want broken even on parse error", ex.Package)
	}
	if ex.LineCount == 0 {
		t.Error("line count should still be recorded on parse error")
	}
	if _, ok := symByName(ex.Symbols, "Valid"); !ok {
		t.Error("the valid decl before the error should still be extracted")
	}
}
