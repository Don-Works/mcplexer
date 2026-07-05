package index

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"

	"github.com/don-works/mcplexer/internal/store"
)

const sigCap = 200
const docCap = 240

// extractGo parses a Go source file and returns its package, doc summary,
// symbols, and import specifiers. A parse error degrades to whatever the
// partial AST yields plus a ParseError note — the build never fails on it.
func extractGo(rel string, src []byte) *Extraction {
	ex := &Extraction{Language: "go", LineCount: countLines(src)}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, rel, src, parser.ParseComments|parser.SkipObjectResolution)
	if f == nil {
		if err != nil {
			ex.ParseError = err.Error()
		}
		return ex
	}
	ex.Package = f.Name.Name
	if f.Doc != nil {
		ex.DocSummary = firstSentence(f.Doc.Text(), docCap)
	}
	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			ex.Symbols = append(ex.Symbols, goFuncSymbol(d, fset, src))
		case *ast.GenDecl:
			ex.Symbols = append(ex.Symbols, goGenSymbols(d, fset, src)...)
		}
	}
	for _, imp := range f.Imports {
		ex.Imports = append(ex.Imports, strings.Trim(imp.Path.Value, `"`))
	}
	if err != nil {
		ex.ParseError = err.Error()
	}
	return ex
}

// goFuncSymbol builds a func/method symbol. The signature is the source from
// the declaration start up to the body's opening brace (or the end for a
// bodyless decl), whitespace-collapsed.
func goFuncSymbol(d *ast.FuncDecl, fset *token.FileSet, src []byte) store.CodeIndexSymbol {
	kind, receiver := "func", ""
	if d.Recv != nil && len(d.Recv.List) > 0 {
		kind = "method"
		receiver = receiverType(d.Recv.List[0].Type)
	}
	sigEnd := d.End()
	if d.Body != nil {
		sigEnd = d.Body.Lbrace
	}
	return store.CodeIndexSymbol{
		Name:      d.Name.Name,
		Kind:      kind,
		Receiver:  receiver,
		Signature: sourceSlice(src, fset, d.Pos(), sigEnd, sigCap),
		Doc:       docSummary(d.Doc),
		StartLine: fset.Position(d.Pos()).Line,
		EndLine:   fset.Position(d.End()).Line,
		Exported:  ast.IsExported(d.Name.Name),
	}
}

// goGenSymbols expands a type/const/var GenDecl into one symbol per name.
func goGenSymbols(d *ast.GenDecl, fset *token.FileSet, src []byte) []store.CodeIndexSymbol {
	var out []store.CodeIndexSymbol
	for _, spec := range d.Specs {
		switch s := spec.(type) {
		case *ast.TypeSpec:
			out = append(out, typeSymbol(d, s, fset, src))
		case *ast.ValueSpec:
			out = append(out, valueSymbols(d, s, fset, src)...)
		}
	}
	return out
}

// typeSymbol builds a type/interface symbol.
func typeSymbol(d *ast.GenDecl, s *ast.TypeSpec, fset *token.FileSet, src []byte) store.CodeIndexSymbol {
	kind := "type"
	if _, ok := s.Type.(*ast.InterfaceType); ok {
		kind = "interface"
	}
	return store.CodeIndexSymbol{
		Name:      s.Name.Name,
		Kind:      kind,
		Signature: sourceSlice(src, fset, s.Pos(), s.End(), sigCap),
		Doc:       docSummary(specDoc(d, s.Doc)),
		StartLine: fset.Position(s.Pos()).Line,
		EndLine:   fset.Position(s.End()).Line,
		Exported:  ast.IsExported(s.Name.Name),
	}
}

// valueSymbols builds one const/var symbol per name in a ValueSpec.
func valueSymbols(d *ast.GenDecl, s *ast.ValueSpec, fset *token.FileSet, src []byte) []store.CodeIndexSymbol {
	kind := "var"
	if d.Tok == token.CONST {
		kind = "const"
	}
	sig := sourceSlice(src, fset, s.Pos(), s.End(), sigCap)
	doc := docSummary(specDoc(d, s.Doc))
	line := fset.Position(s.Pos()).Line
	end := fset.Position(s.End()).Line
	out := make([]store.CodeIndexSymbol, 0, len(s.Names))
	for _, name := range s.Names {
		out = append(out, store.CodeIndexSymbol{
			Name: name.Name, Kind: kind, Signature: sig, Doc: doc,
			StartLine: line, EndLine: end, Exported: ast.IsExported(name.Name),
		})
	}
	return out
}

// receiverType renders a method receiver type name, stripping pointer stars and
// generic type parameters ("*Foo[T]" -> "Foo").
func receiverType(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.StarExpr:
		return receiverType(t.X)
	case *ast.IndexExpr:
		return receiverType(t.X)
	case *ast.IndexListExpr:
		return receiverType(t.X)
	case *ast.SelectorExpr:
		return t.Sel.Name
	case *ast.Ident:
		return t.Name
	}
	return ""
}

// sourceSlice returns the whitespace-collapsed source between two positions,
// capped at max runes. An out-of-range span yields "".
func sourceSlice(src []byte, fset *token.FileSet, from, to token.Pos, max int) string {
	fo := fset.Position(from).Offset
	toO := fset.Position(to).Offset
	if fo < 0 || toO > len(src) || fo >= toO {
		return ""
	}
	return collapseWS(string(src[fo:toO]), max)
}

// docSummary is firstSentence over a comment group's text.
func docSummary(cg *ast.CommentGroup) string {
	if cg == nil {
		return ""
	}
	return firstSentence(cg.Text(), docCap)
}

// specDoc prefers the spec's own doc, falling back to the GenDecl's doc for a
// single-spec (unparenthesized) declaration.
func specDoc(d *ast.GenDecl, own *ast.CommentGroup) *ast.CommentGroup {
	if own != nil {
		return own
	}
	if !d.Lparen.IsValid() {
		return d.Doc
	}
	return nil
}
