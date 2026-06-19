package codemode

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/dop251/goja"
)

// helpDescMax caps the per-tool description shown by help('namespace') so a
// verbose downstream description can't blow up a small model's context.
const helpDescMax = 100

// helpGlobalHelpers is the fixed list of non-namespaced sandbox helpers that
// help() advertises alongside the tool namespaces. These are registered
// directly on the VM in Execute (print/parallel/compact/...), carry no
// namespace prefix, and so never appear in the grouped tool set — without
// this list a model exploring via help() would never learn they exist.
var helpGlobalHelpers = []string{
	"print(...values)",
	"parallel([{tool, args}, ...])",
	"compact(value)",
	"sleep(ms)",
	"atob(b64) / btoa(str)",
	"help(), help('namespace')",
}

// makeHelpFunc returns the in-sandbox help() introspection function. With no
// argument it prints the directory of available namespaces (each with its
// tool count) plus the global helpers; with a namespace argument it prints
// that namespace's tools as copy-pasteable call signatures with a one-line
// description each.
//
// Output is written straight to the capture buffer (the same path as print)
// rather than returned, so a bare `help()` statement surfaces something — a
// small/local model that types `help()` to orient itself must never get a
// silent empty result because it forgot to wrap the call in print().
func makeHelpFunc(mu *sync.Mutex, output *outputCapture, groups map[string][]toolEntry) func(goja.FunctionCall) goja.Value {
	// Pre-sort namespace names once at registration time; groups is never
	// mutated after Execute builds it, so this stays valid for every call.
	nsNames := make([]string, 0, len(groups))
	for ns := range groups {
		nsNames = append(nsNames, ns)
	}
	sort.Strings(nsNames)

	return func(call goja.FunctionCall) goja.Value {
		arg := ""
		if len(call.Arguments) > 0 {
			a := call.Arguments[0]
			if a != nil && !goja.IsUndefined(a) && !goja.IsNull(a) {
				arg = strings.TrimSpace(a.String())
			}
		}

		text := buildHelpIndex(nsNames, groups)
		if arg != "" {
			text = buildHelpNamespace(arg, nsNames, groups)
		}

		mu.Lock()
		output.WriteString(text)
		if !strings.HasSuffix(text, "\n") {
			output.writeByte('\n')
		}
		mu.Unlock()
		return goja.Undefined()
	}
}

// buildHelpIndex renders the no-argument help() directory: every namespace
// with its tool count, the global helpers, and a pointer to the per-namespace
// form. The header restates that calls are synchronous so a model reaching
// for help() also gets the no-await reminder for free.
func buildHelpIndex(nsNames []string, groups map[string][]toolEntry) string {
	var b strings.Builder
	if len(nsNames) == 0 {
		b.WriteString("No tool namespaces are registered in this sandbox.\n")
	} else {
		fmt.Fprintf(&b, "Available namespaces (%d) — call as namespace.tool(args), synchronously (no await):\n", len(nsNames))
		for _, ns := range nsNames {
			n := len(groups[ns])
			fmt.Fprintf(&b, "  %s (%d tool%s)\n", ns, n, plural(n))
		}
	}
	b.WriteString("\nGlobal helpers: ")
	b.WriteString(strings.Join(helpGlobalHelpers, ", "))

	example := "memory"
	if len(nsNames) > 0 {
		example = nsNames[0]
	}
	fmt.Fprintf(&b, "\n\nCall help('namespace') for a namespace's tools + signatures, e.g. help('%s'). "+
		"Search across everything with mcpx.search_tools({queries:[...]}).", example)
	return b.String()
}

// buildHelpNamespace renders help('ns'): each tool in the namespace as a
// copy-pasteable call signature (reusing synthesizeExample, the same helper
// that builds inline error examples) with a one-line description. An unknown
// namespace name falls back to a did-you-mean over the real namespaces so a
// typo still teaches the model the correct name instead of dead-ending.
func buildHelpNamespace(arg string, nsNames []string, groups map[string][]toolEntry) string {
	entries, ok := groups[arg]
	if !ok {
		var b strings.Builder
		fmt.Fprintf(&b, "No namespace %q.", arg)
		if sug := DidYouMean(arg, nsNames, 3); len(sug) > 0 {
			b.WriteString(" Did you mean: " + strings.Join(sug, ", ") + "?")
		}
		b.WriteString("\nAvailable namespaces: " + strings.Join(truncateNamespaceList(nsNames, 20), ", "))
		if len(nsNames) > 20 {
			fmt.Fprintf(&b, " (+%d more)", len(nsNames)-20)
		}
		return b.String()
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s — %d tool%s:\n", arg, len(entries), plural(len(entries)))
	for _, e := range entries {
		full := arg + "__" + e.name
		sig := synthesizeExample(full, e.schema)
		if sig == "" {
			sig = renderJSCallName(full) + "()"
		}
		fmt.Fprintf(&b, "  %s\n", sig)
		if desc := firstLine(e.description); desc != "" {
			fmt.Fprintf(&b, "      %s\n", truncateForHelp(desc, helpDescMax))
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// plural returns "s" unless n == 1, for "1 tool" / "3 tools" rendering.
func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// firstLine returns the first non-empty line of s, trimmed. Tool descriptions
// are often multi-paragraph; help() only wants the lead sentence.
func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if line, _, found := strings.Cut(s, "\n"); found {
		return strings.TrimSpace(line)
	}
	return s
}

// truncateForHelp caps a description at max bytes without splitting a
// multi-byte rune, appending an ellipsis when it cuts.
func truncateForHelp(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return safeUTF8Prefix(s, max) + "…"
}
