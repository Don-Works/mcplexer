package index

import (
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/don-works/mcplexer/internal/store"
)

// goModule reads the module path from go.mod at root, or "" when absent (all
// Go imports then resolve as external).
func goModule(root string) string {
	data, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "module ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "module "))
		}
	}
	return ""
}

// resolveGoImport turns a Go import path into an edge. An import under the
// module prefix resolves to its root-relative package directory (ToPath);
// anything else is external (ToModule).
func resolveGoImport(module, importPath, ws string) store.CodeIndexEdge {
	e := store.CodeIndexEdge{WorkspaceID: ws, Kind: "import"}
	if module != "" && (importPath == module || strings.HasPrefix(importPath, module+"/")) {
		dir := strings.TrimPrefix(strings.TrimPrefix(importPath, module), "/")
		if dir == "" {
			dir = "."
		}
		e.ToPath = dir
		return e
	}
	e.ToModule = importPath
	return e
}

// tsEdge is a resolved TS import edge plus whether the specifier was a path
// alias (@/… or ~/…), which build.go tallies for the alias-coverage warning.
type tsEdge struct {
	edge  store.CodeIndexEdge
	alias bool
}

// tsCandidateExts is the resolution order for a relative TS/JS import with no
// explicit extension.
var tsCandidateExts = []string{
	".ts", ".tsx", ".js", ".jsx", "/index.ts", "/index.tsx", "/index.js", "/index.jsx",
}

// resolveTSImport resolves a TS/JS import specifier against the in-memory set
// of enumerated files (never the filesystem — symlink-safe, R2). Relative
// specifiers resolve to a file in the tree; bare modules and unresolved path
// aliases are recorded external.
func resolveTSImport(fromRel, spec string, enumSet map[string]bool, ws string) tsEdge {
	e := store.CodeIndexEdge{WorkspaceID: ws, Kind: "import"}
	if !strings.HasPrefix(spec, ".") {
		e.ToModule = spec
		return tsEdge{edge: e, alias: strings.HasPrefix(spec, "@/") || strings.HasPrefix(spec, "~/")}
	}
	base := path.Join(path.Dir(fromRel), spec)
	if base == ".." || strings.HasPrefix(base, "../") {
		e.ToModule = spec // escapes the tree — keep external
		return tsEdge{edge: e}
	}
	if enumSet[base] {
		e.ToPath = base
		return tsEdge{edge: e}
	}
	for _, ext := range tsCandidateExts {
		if enumSet[base+ext] {
			e.ToPath = base + ext
			return tsEdge{edge: e}
		}
	}
	e.ToModule = spec // unresolved relative specifier
	return tsEdge{edge: e}
}
