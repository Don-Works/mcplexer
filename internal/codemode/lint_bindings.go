package codemode

import (
	"github.com/dop251/goja/ast"
	"github.com/dop251/goja/file"
	"github.com/dop251/goja/parser"
)

// collectLocalBindings uses the same parser as the sandbox so member calls on
// parameters, loop variables, catch bindings, and destructured locals are not
// mistaken for namespace.tool invocations.
func collectLocalBindings(code string) map[string]struct{} {
	program, err := parser.ParseFile(new(file.FileSet), "execute_code", code, 0)
	if err != nil {
		return collectRegexLocalBindings(code)
	}
	out := make(map[string]struct{})
	walkAST(program, func(node ast.Node) bool {
		switch n := node.(type) {
		case *ast.VariableStatement:
			collectBindings(n.List, out)
		case *ast.VariableDeclaration:
			collectBindings(n.List, out)
		case *ast.LexicalDeclaration:
			collectBindings(n.List, out)
		case *ast.FunctionLiteral:
			collectFunctionBindings(n.Name, n.ParameterList, out)
		case *ast.ArrowFunctionLiteral:
			collectFunctionBindings(nil, n.ParameterList, out)
		case *ast.CatchStatement:
			collectBindingTarget(n.Parameter, out)
		case *ast.ForIntoVar:
			collectBindings([]*ast.Binding{n.Binding}, out)
		case *ast.ForLoopInitializerLexicalDecl:
			collectBindings(n.LexicalDeclaration.List, out)
		case *ast.ForLoopInitializerVarDeclList:
			collectBindings(n.List, out)
		}
		return true
	})
	return out
}

func collectRegexLocalBindings(code string) map[string]struct{} {
	matches := reLocalBinding.FindAllStringSubmatch(code, -1)
	out := make(map[string]struct{}, len(matches))
	for _, match := range matches {
		if len(match) > 1 {
			out[match[1]] = struct{}{}
		}
	}
	return out
}

func collectFunctionBindings(name *ast.Identifier, params *ast.ParameterList, out map[string]struct{}) {
	if name != nil {
		out[name.Name.String()] = struct{}{}
	}
	if params == nil {
		return
	}
	collectBindings(params.List, out)
	collectBindingExpression(params.Rest, out)
}

func collectBindings(bindings []*ast.Binding, out map[string]struct{}) {
	for _, binding := range bindings {
		if binding != nil {
			collectBindingTarget(binding.Target, out)
		}
	}
}

func collectBindingTarget(target ast.BindingTarget, out map[string]struct{}) {
	if target != nil {
		collectBindingExpression(target, out)
	}
}

func collectBindingExpression(expr ast.Expression, out map[string]struct{}) {
	switch n := expr.(type) {
	case *ast.Identifier:
		out[n.Name.String()] = struct{}{}
	case *ast.ArrayPattern:
		for _, item := range n.Elements {
			collectBindingExpression(item, out)
		}
		collectBindingExpression(n.Rest, out)
	case *ast.ObjectPattern:
		for _, prop := range n.Properties {
			switch p := prop.(type) {
			case *ast.PropertyShort:
				out[p.Name.Name.String()] = struct{}{}
			case *ast.PropertyKeyed:
				collectBindingExpression(p.Value, out)
			case *ast.SpreadElement:
				collectBindingExpression(p.Expression, out)
			}
		}
		collectBindingExpression(n.Rest, out)
	case *ast.AssignExpression:
		collectBindingExpression(n.Left, out)
	case *ast.SpreadElement:
		collectBindingExpression(n.Expression, out)
	}
}
