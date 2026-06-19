package addon

import (
	"fmt"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
)

// collectEndpoints walks every path-method combination in document order and
// produces an EndpointSpec per operation.
func collectEndpoints(doc *openapi3.T) []EndpointSpec {
	used := make(map[string]bool)
	var out []EndpointSpec
	for _, path := range doc.Paths.InMatchingOrder() {
		item := doc.Paths.Value(path)
		if item == nil {
			continue
		}
		for _, mo := range methodOps(item) {
			if mo.Op == nil {
				continue
			}
			out = append(out, buildEndpoint(mo.Method, path, item, mo.Op, used))
		}
	}
	return out
}

type methodOp struct {
	Method string
	Op     *openapi3.Operation
}

// methodOps returns the (method, *Operation) pairs on a PathItem in stable
// order. Only verbs the addon executor supports — others are dropped.
func methodOps(item *openapi3.PathItem) []methodOp {
	return []methodOp{
		{"GET", item.Get}, {"POST", item.Post}, {"PUT", item.Put},
		{"PATCH", item.Patch}, {"DELETE", item.Delete},
	}
}

// buildEndpoint converts one operation into an EndpointSpec, deduping the
// generated tool name against a per-spec set.
func buildEndpoint(method, path string, item *openapi3.PathItem, op *openapi3.Operation, used map[string]bool) EndpointSpec {
	name := pickEndpointName(method, path, op, used)
	used[name] = true
	desc := firstNonEmpty(op.Summary, op.Description, fmt.Sprintf("%s %s", method, path))
	params := collectParams(item.Parameters, op.Parameters, op.RequestBody)
	return EndpointSpec{
		Name:        name,
		Description: truncate(desc, 240),
		Method:      method,
		Path:        rewritePathPlaceholders(path, params),
		Params:      params,
	}
}

// rewritePathPlaceholders converts OpenAPI's `{name}` URL placeholders into
// the addon executor's `{{slug}}` form, matching the slugified param name
// written into input_schema. Used by the OpenAPI importer; the same rewrite
// is also applied at YAML-build time via rewriteURLPlaceholders so direct
// mcpx__create_addon calls (which can carry path placeholders typed by an
// agent or the wizard) get the same fix.
func rewritePathPlaceholders(path string, params []ParamSpec) string {
	return rewriteURLPlaceholders(path, params)
}

// rewriteURLPlaceholders walks the path-located params and rewrites every
// `{<orig>}` token in `url` whose slugified form matches the param name into
// `{{<slug>}}`. Without this rewrite the executor's substituteURL never
// finds a match and the literal `{name}` gets sent to the upstream API —
// verified live against petstore3.swagger.io which rejected `/pet/{petId}`
// with HTTP 400, and would have hit api.freeagent.com on `/contacts/{id}`.
// Tokens we don't recognise are left alone — they'll trip substituteURL
// with a clear "missing required url param" error rather than silently
// corrupting the URL.
func rewriteURLPlaceholders(url string, params []ParamSpec) string {
	out := url
	for _, p := range params {
		if p.In != "path" {
			continue
		}
		out = replacePlaceholderForParam(out, p.Name)
	}
	return out
}

// replacePlaceholderForParam scans `path` for `{<orig>}` tokens whose
// slugified form equals `slug`, replacing each with `{{slug}}`. Tokens that
// don't match are left alone — they'll trip substituteURL with a clear
// "missing required url param" error rather than silently corrupting the URL.
func replacePlaceholderForParam(path, slug string) string {
	var b strings.Builder
	b.Grow(len(path))
	i := 0
	for i < len(path) {
		c := path[i]
		if c != '{' {
			b.WriteByte(c)
			i++
			continue
		}
		end := strings.IndexByte(path[i:], '}')
		if end < 0 {
			b.WriteString(path[i:])
			break
		}
		token := path[i+1 : i+end]
		if slugifyParam(token) == slug {
			b.WriteString("{{")
			b.WriteString(slug)
			b.WriteString("}}")
		} else {
			b.WriteString(path[i : i+end+1])
		}
		i += end + 1
	}
	return b.String()
}

// pickEndpointName prefers operationId; falls back to method+path slug; appends
// a numeric suffix if the slug collides with a previously-generated name.
func pickEndpointName(method, path string, op *openapi3.Operation, used map[string]bool) string {
	base := slugifyName(op.OperationID)
	if base == "" {
		base = slugifyName(strings.ToLower(method) + "_" + path)
	}
	if base == "" {
		base = strings.ToLower(method)
	}
	name := base
	for i := 2; used[name]; i++ {
		name = fmt.Sprintf("%s_%d", base, i)
	}
	return name
}

// collectParams flattens path-item params, op params, and the request body into
// one ParamSpec list. Header/cookie params are skipped (out of scope for the
// addon executor's input_schema model).
func collectParams(itemParams openapi3.Parameters, opParams openapi3.Parameters, body *openapi3.RequestBodyRef) []ParamSpec {
	seen := map[string]bool{}
	var out []ParamSpec
	add := func(ps []ParamSpec) {
		for _, p := range ps {
			if p.Name == "" || seen[p.Name] {
				continue
			}
			seen[p.Name] = true
			out = append(out, p)
		}
	}
	add(convertParameters(itemParams))
	add(convertParameters(opParams))
	add(convertRequestBody(body))
	return out
}

// convertParameters maps openapi3.Parameters to ParamSpec, skipping unsupported
// param locations (header, cookie).
func convertParameters(ps openapi3.Parameters) []ParamSpec {
	var out []ParamSpec
	for _, ref := range ps {
		if ref == nil || ref.Value == nil {
			continue
		}
		p := ref.Value
		in := strings.ToLower(p.In)
		if in != "path" && in != "query" {
			continue
		}
		out = append(out, ParamSpec{
			Name:        slugifyParam(p.Name),
			Type:        schemaType(p.Schema),
			In:          in,
			Description: p.Description,
			Required:    p.Required || in == "path",
		})
	}
	return out
}

// convertRequestBody promotes JSON request body properties to top-level
// ParamSpec entries. Only the first JSON content type is honored.
func convertRequestBody(body *openapi3.RequestBodyRef) []ParamSpec {
	if body == nil || body.Value == nil {
		return nil
	}
	mt := body.Value.Content.Get("application/json")
	if mt == nil || mt.Schema == nil || mt.Schema.Value == nil {
		return nil
	}
	s := mt.Schema.Value
	required := map[string]bool{}
	for _, r := range s.Required {
		required[r] = true
	}
	var out []ParamSpec
	for propName, propRef := range s.Properties {
		if propRef == nil {
			continue
		}
		out = append(out, ParamSpec{
			Name:     slugifyParam(propName),
			Type:     schemaType(propRef),
			In:       "body",
			Required: required[propName],
		})
	}
	return out
}

// schemaType picks one of {string, integer, number, boolean} from a SchemaRef,
// defaulting to "string" for unknown/object/array (addon executor stringifies).
func schemaType(ref *openapi3.SchemaRef) string {
	if ref == nil || ref.Value == nil || ref.Value.Type == nil {
		return "string"
	}
	for _, t := range ref.Value.Type.Slice() {
		switch t {
		case "integer":
			return "integer"
		case "number":
			return "number"
		case "boolean":
			return "boolean"
		case "string":
			return "string"
		}
	}
	return "string"
}
