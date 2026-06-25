package codemode

import (
	"encoding/json"
	"html"
	"regexp"
	"strings"

	"github.com/dop251/goja"
)

const (
	untrustedContentPrefix = "<untrusted-content"
	untrustedContentClose  = "</untrusted-content>"
	untrustedMetaProperty  = "__mcplexer_untrusted_content"
)

var untrustedAttrRE = regexp.MustCompile(`\b(source|trust)="([^"]*)"`)

type untrustedStructuredValue struct {
	Value  any
	Source string
	Trust  string
}

func parseUntrustedStructuredText(text string) (untrustedStructuredValue, bool) {
	body, source, trust, ok := unwrapSingleUntrustedEnvelope(text)
	if !ok {
		return untrustedStructuredValue{}, false
	}

	var parsed any
	if err := json.Unmarshal([]byte(html.UnescapeString(body)), &parsed); err != nil {
		return untrustedStructuredValue{}, false
	}
	switch parsed.(type) {
	case map[string]any, []any:
		if trust == "" {
			trust = "low"
		}
		return untrustedStructuredValue{Value: parsed, Source: source, Trust: trust}, true
	default:
		return untrustedStructuredValue{}, false
	}
}

func unwrapSingleUntrustedEnvelope(text string) (body, source, trust string, ok bool) {
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, untrustedContentPrefix) {
		return "", "", "", false
	}
	rest := trimmed[len(untrustedContentPrefix):]
	if rest == "" {
		return "", "", "", false
	}
	next := rest[0]
	if next != ' ' && next != '>' && next != '\t' && next != '\n' {
		return "", "", "", false
	}
	openEnd := strings.IndexByte(trimmed, '>')
	if openEnd < 0 || !strings.HasSuffix(trimmed, untrustedContentClose) {
		return "", "", "", false
	}
	openTag := trimmed[:openEnd+1]
	body = trimmed[openEnd+1 : len(trimmed)-len(untrustedContentClose)]
	source, trust = parseUntrustedAttrs(openTag)
	return body, source, trust, true
}

func parseUntrustedAttrs(openTag string) (source, trust string) {
	for _, match := range untrustedAttrRE.FindAllStringSubmatch(openTag, -1) {
		if len(match) != 3 {
			continue
		}
		switch match[1] {
		case "source":
			source = html.UnescapeString(match[2])
		case "trust":
			trust = strings.ToLower(strings.TrimSpace(html.UnescapeString(match[2])))
		}
	}
	if trust == "" {
		trust = "low"
	}
	return source, trust
}

func toolValueToGoja(vm *goja.Runtime, val any) goja.Value {
	if untrusted, ok := val.(untrustedStructuredValue); ok {
		v := nativeJSONValue(vm, untrusted.Value)
		markUntrustedValue(vm, v, untrusted.Source, untrusted.Trust)
		return v
	}
	return vm.ToValue(val)
}

func nativeJSONValue(vm *goja.Runtime, val any) goja.Value {
	data, err := json.Marshal(val)
	if err != nil {
		return vm.ToValue(val)
	}
	v, err := vm.RunString("(" + string(data) + ")")
	if err != nil {
		return vm.ToValue(val)
	}
	return v
}

func markUntrustedValue(vm *goja.Runtime, v goja.Value, source, trust string) {
	if v == nil || goja.IsUndefined(v) || goja.IsNull(v) {
		return
	}
	if trust == "" {
		trust = "low"
	}
	meta := map[string]any{"source": source, "trust": trust}
	_ = v.ToObject(vm).DefineDataProperty(
		untrustedMetaProperty,
		vm.ToValue(meta),
		goja.FLAG_FALSE,
		goja.FLAG_FALSE,
		goja.FLAG_FALSE,
	)
}

func untrustedMetaForValue(vm *goja.Runtime, v goja.Value) (source, trust string, ok bool) {
	if v == nil || goja.IsUndefined(v) || goja.IsNull(v) {
		return "", "", false
	}
	metaVal := v.ToObject(vm).Get(untrustedMetaProperty)
	if metaVal == nil || goja.IsUndefined(metaVal) || goja.IsNull(metaVal) {
		return "", "", false
	}
	meta, _ := metaVal.Export().(map[string]any)
	source, _ = meta["source"].(string)
	trust, _ = meta["trust"].(string)
	if trust == "" {
		trust = "low"
	}
	return source, trust, true
}

func envelopeUntrustedForPrint(source, trust, body string) string {
	if trust == "" {
		trust = "low"
	}
	var b strings.Builder
	b.Grow(len(body) + len(source) + 64)
	b.WriteString(`<untrusted-content source="`)
	b.WriteString(html.EscapeString(source))
	b.WriteString(`" trust="`)
	b.WriteString(html.EscapeString(trust))
	b.WriteString(`">`)
	b.WriteString(escapeEnvelopeBody(body))
	b.WriteString(`</untrusted-content>`)
	return b.String()
}

func escapeEnvelopeBody(s string) string {
	if !strings.ContainsAny(s, "&<>") {
		return s
	}
	replacer := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return replacer.Replace(s)
}
