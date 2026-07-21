package index

import (
	"regexp"
	"strings"
	"unicode"

	"github.com/don-works/mcplexer/internal/store"
)

// Line-anchored import/export specifier patterns (§7.3). require() and dynamic
// import() may appear mid-line, so they are matched with FindAll.
var (
	reImportFrom = regexp.MustCompile(`^\s*import\s+(?:type\s+)?(?:[\w${},*\s]+\s+from\s+)?['"]([^'"]+)['"]`)
	reExportFrom = regexp.MustCompile(`^\s*export\s+(?:[\w${},*\s]+\s+)?from\s+['"]([^'"]+)['"]`)
	reRequire    = regexp.MustCompile(`\brequire\(\s*['"]([^'"]+)['"]\s*\)`)
	reDynImport  = regexp.MustCompile(`\bimport\(\s*['"]([^'"]+)['"]\s*\)`)
)

// Line-anchored symbol declaration patterns.
var (
	reFuncExport  = regexp.MustCompile(`^export\s+(?:default\s+)?(?:async\s+)?function\s+(\w+)`)
	reClassExport = regexp.MustCompile(`^export\s+(?:default\s+)?(?:abstract\s+)?class\s+(\w+)`)
	reTypeExport  = regexp.MustCompile(`^export\s+(?:declare\s+)?(type|interface|enum)\s+(\w+)`)
	reConstExport = regexp.MustCompile(`^export\s+(?:default\s+)?(const|let|var)\s+(\w+)`)
	reFuncPlain   = regexp.MustCompile(`^(?:async\s+)?function\s+(\w+)`)
	reConstArrow  = regexp.MustCompile(`^const\s+(\w+)\s*(?::[^=]+)?=\s*(?:async\s*)?(?:\(|function|<)`)
	reArrowRHS    = regexp.MustCompile(`=\s*(?:async\s*)?(?:\(|function|<)`)
)

// extractTS extracts imports and top-level symbols from a TS/JS source file via
// line-anchored regexes. It never fails: unmatched lines are ignored.
func extractTS(rel string, src []byte, lang string) *Extraction {
	ex := &Extraction{Language: lang, LineCount: countLines(src)}
	isJSX := strings.HasSuffix(rel, ".tsx") || strings.HasSuffix(rel, ".jsx")
	for i, line := range strings.Split(string(src), "\n") {
		collectTSImports(ex, line)
		if sym, ok := tsSymbol(line, i+1, isJSX); ok {
			ex.Symbols = append(ex.Symbols, sym)
		}
	}
	return ex
}

// collectTSImports appends every import/require/dynamic-import specifier on one
// line to ex.Imports.
func collectTSImports(ex *Extraction, line string) {
	for _, re := range []*regexp.Regexp{reImportFrom, reExportFrom} {
		if m := re.FindStringSubmatch(line); m != nil {
			ex.Imports = append(ex.Imports, m[1])
		}
	}
	for _, re := range []*regexp.Regexp{reRequire, reDynImport} {
		for _, m := range re.FindAllStringSubmatch(line, -1) {
			ex.Imports = append(ex.Imports, m[1])
		}
	}
}

// tsSymbol matches one declaration on a line, returning its symbol. The first
// pattern to match (in priority order) wins, so a line yields at most one
// symbol.
func tsSymbol(line string, lineNo int, isJSX bool) (store.CodeIndexSymbol, bool) {
	sig := collapseWS(line, sigCap)
	if m := reFuncExport.FindStringSubmatch(line); m != nil {
		return tsSym(m[1], funcKind(m[1], isJSX), sig, lineNo, true), true
	}
	if m := reClassExport.FindStringSubmatch(line); m != nil {
		return tsSym(m[1], "class", sig, lineNo, true), true
	}
	if m := reTypeExport.FindStringSubmatch(line); m != nil {
		return tsSym(m[2], m[1], sig, lineNo, true), true
	}
	if m := reConstExport.FindStringSubmatch(line); m != nil {
		return tsSym(m[2], constKind(m[1], line, isJSX), sig, lineNo, true), true
	}
	if m := reFuncPlain.FindStringSubmatch(line); m != nil {
		return tsSym(m[1], funcKind(m[1], isJSX), sig, lineNo, false), true
	}
	if m := reConstArrow.FindStringSubmatch(line); m != nil {
		return tsSym(m[1], funcKind(m[1], isJSX), sig, lineNo, false), true
	}
	return store.CodeIndexSymbol{}, false
}

// tsSym constructs a symbol; TS has no reliable end line, so EndLine stays 0.
func tsSym(name, kind, sig string, line int, exported bool) store.CodeIndexSymbol {
	return store.CodeIndexSymbol{
		Name: name, Kind: kind, Signature: sig, StartLine: line, Exported: exported,
	}
}

// funcKind labels a function as a React "component" when its name is
// capitalized and the file is JSX/TSX (§7.3); otherwise "func".
func funcKind(name string, isJSX bool) string {
	if isJSX && isCapitalized(name) {
		return "component"
	}
	return "func"
}

// constKind classifies an exported const/let/var: an arrow/function RHS is a
// func (or component in JSX), otherwise the keyword itself (const|var).
func constKind(keyword, line string, isJSX bool) string {
	if reArrowRHS.MatchString(line) {
		return funcKind(constName(line), isJSX)
	}
	if keyword == "const" {
		return "const"
	}
	return "var"
}

// constName extracts the identifier from an `export const NAME` line for the
// component-capitalization check.
func constName(line string) string {
	if m := reConstExport.FindStringSubmatch(line); m != nil {
		return m[2]
	}
	return ""
}

func isCapitalized(name string) bool {
	if name == "" {
		return false
	}
	return unicode.IsUpper([]rune(name)[0])
}
