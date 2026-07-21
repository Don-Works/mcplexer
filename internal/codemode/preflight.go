package codemode

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/dop251/goja/ast"
	"github.com/dop251/goja/file"
	"github.com/dop251/goja/parser"
)

// Preflight checks code before sandbox execution. It uses goja's parser so
// syntax failures match the runtime, then blocks dynamic-code constructors
// that make agent mistakes harder to inspect and reason about.
func Preflight(code string) []LintWarning {
	fs := new(file.FileSet)
	program, err := parser.ParseFile(fs, "execute_code", code, 0)
	if err != nil {
		return []LintWarning{syntaxIssue(err)}
	}
	return findDisallowedDynamicCode(program, fs)
}

func FormatPreflightIssues(issues []LintWarning) string {
	if len(issues) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("preflight failed before execution; no tool calls were dispatched")
	for _, issue := range issues {
		if issue.Column > 0 {
			fmt.Fprintf(&b, "\n[%s] line %d:%d: %s",
				issue.Severity, issue.Line, issue.Column, issue.Message)
		} else {
			fmt.Fprintf(&b, "\n[%s] line %d: %s",
				issue.Severity, issue.Line, issue.Message)
		}
	}
	return b.String()
}

func syntaxIssue(err error) LintWarning {
	line, column := 1, 0
	message := err.Error()
	switch e := err.(type) {
	case parser.ErrorList:
		if len(e) > 0 {
			line = e[0].Position.Line
			column = e[0].Position.Column
			message = e[0].Message
		}
	case *parser.Error:
		line = e.Position.Line
		column = e.Position.Column
		message = e.Message
	}
	return LintWarning{
		Line:     line,
		Column:   column,
		Message:  "syntax error: " + message,
		Severity: "error",
	}
}

func findDisallowedDynamicCode(program *ast.Program, fs *file.FileSet) []LintWarning {
	var issues []LintWarning
	seen := make(map[string]struct{})
	walkAST(program, func(node ast.Node) bool {
		var name string
		switch n := node.(type) {
		case *ast.CallExpression:
			name = disallowedCallName(n.Callee)
		case *ast.NewExpression:
			name = disallowedCallName(n.Callee)
		}
		if name == "" {
			return true
		}

		pos := fs.Position(node.Idx0())
		key := fmt.Sprintf("%d:%d:%s", pos.Line, pos.Column, name)
		if _, ok := seen[key]; ok {
			return true
		}
		seen[key] = struct{}{}

		issues = append(issues, LintWarning{
			Line:     pos.Line,
			Column:   pos.Column,
			Message:  fmt.Sprintf("%s is not allowed in code mode; call registered tools directly instead", name),
			Severity: "error",
		})
		return true
	})
	return issues
}

func disallowedCallName(expr ast.Expression) string {
	switch n := expr.(type) {
	case *ast.Identifier:
		return disallowedIdentifier(n.Name.String())
	case *ast.DotExpression:
		left, ok := n.Left.(*ast.Identifier)
		if !ok || left.Name.String() != "globalThis" {
			return ""
		}
		return disallowedIdentifier("globalThis." + n.Identifier.Name.String())
	case *ast.Optional:
		return disallowedCallName(n.Expression)
	case *ast.OptionalChain:
		return disallowedCallName(n.Expression)
	default:
		return ""
	}
}

func disallowedIdentifier(name string) string {
	switch name {
	case "eval", "Function", "import", "require",
		"globalThis.eval", "globalThis.Function", "globalThis.import", "globalThis.require":
		return name
	default:
		return ""
	}
}

func walkAST(node ast.Node, visit func(ast.Node) bool) {
	if node == nil || !visit(node) {
		return
	}
	v := reflect.ValueOf(node)
	if v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return
		}
		v = v.Elem()
	}
	walkASTValue(v, visit)
}

func walkASTValue(v reflect.Value, visit func(ast.Node) bool) {
	if !v.IsValid() {
		return
	}
	switch v.Kind() {
	case reflect.Interface, reflect.Pointer:
		if v.IsNil() {
			return
		}
		if node, ok := v.Interface().(ast.Node); ok {
			walkAST(node, visit)
			return
		}
		walkASTValue(v.Elem(), visit)
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			field := v.Field(i)
			if field.CanInterface() {
				walkASTValue(field, visit)
			}
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < v.Len(); i++ {
			walkASTValue(v.Index(i), visit)
		}
	}
}
