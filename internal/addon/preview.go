package addon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/netguard"
)

// PreviewRequest carries everything the test/preview pane needs to issue a
// single live HTTP call against an in-progress AddonSpec. The spec is NOT
// persisted — this is a UI-only round trip used to validate the spec.
type PreviewRequest struct {
	Spec        AddonSpec      `json:"spec"`
	Endpoint    string         `json:"endpoint"`      // EndpointSpec.Name
	Args        map[string]any `json:"args"`          // user-filled placeholder values
	AuthScopeID string         `json:"auth_scope_id"` // optional override
}

// PreviewResponse is the redacted before-and-after view of the call. The
// caller renders request + response side-by-side. Sensitive values are
// already replaced with REDACTED before the struct leaves this package.
type PreviewResponse struct {
	Request    PreviewHTTP `json:"request"`
	Response   PreviewHTTP `json:"response"`
	Status     int         `json:"status"`
	DurationMs int64       `json:"duration_ms"`
	Note       string      `json:"note"`
}

// PreviewHTTP captures one half of an HTTP exchange in a redacted form.
type PreviewHTTP struct {
	Method  string            `json:"method,omitempty"`
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"`
}

// redactedValue is the placeholder we substitute for sensitive header /
// JSON-field values in the displayed request and response.
const redactedValue = "REDACTED"

// previewMaxBytes caps the response body the preview pane will display.
const previewMaxBytes = 64 * 1024

// sensitiveHeaders is the set of HTTP header names whose value must never
// reach the preview UI verbatim. Comparison is case-insensitive.
var sensitiveHeaders = map[string]struct{}{
	"authorization": {}, "cookie": {}, "set-cookie": {},
	"x-api-key": {}, "x-auth-token": {}, "proxy-authorization": {},
}

// sensitiveBodyKeyRe matches JSON object keys that smell like a secret.
var sensitiveBodyKeyRe = regexp.MustCompile(`(?i)(token|secret|password|api[_-]?key)`)

// PreviewExecutor runs a single HTTP request from an AddonSpec, returning a
// redacted view of the request + response. It mirrors the production
// Executor but never persists, audits via the caller, and applies redaction.
type PreviewExecutor struct {
	getAuthHeaders AuthHeaderFunc
	applyAuth      AuthRequestFunc
	client         *http.Client
}

// NewPreviewExecutor builds a PreviewExecutor that pulls auth headers via
// authFn (typically the same Injector used by production tool dispatch).
func NewPreviewExecutor(authFn AuthHeaderFunc) *PreviewExecutor {
	return NewPreviewExecutorWithRequestAuth(authFn, nil)
}

// NewPreviewExecutorWithRequestAuth builds a PreviewExecutor that can use
// request-bound auth schemes such as Hawk.
func NewPreviewExecutorWithRequestAuth(authFn AuthHeaderFunc, requestAuthFn AuthRequestFunc) *PreviewExecutor {
	return &PreviewExecutor{
		getAuthHeaders: authFn,
		applyAuth:      requestAuthFn,
		client:         netguard.NewPublicHTTPClient(15 * time.Second),
	}
}

// Run issues the configured request and returns the redacted exchange.
func (p *PreviewExecutor) Run(ctx context.Context, req PreviewRequest) (*PreviewResponse, error) {
	if err := req.Spec.Validate(); err != nil {
		return nil, fmt.Errorf("invalid spec: %w", err)
	}
	ep, ok := findEndpoint(req.Spec.Endpoints, req.Endpoint)
	if !ok {
		return nil, fmt.Errorf("endpoint %q not declared on spec", req.Endpoint)
	}

	httpReq, displayReq, err := p.buildRequest(ctx, req, ep)
	if err != nil {
		return nil, err
	}
	pinnedCtx, err := netguard.PinPublicHosts(httpReq.Context(), httpReq.URL.Hostname())
	if err != nil {
		return nil, fmt.Errorf("preview request: %w", err)
	}
	httpReq = httpReq.WithContext(pinnedCtx)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := readPreviewBody(resp.Body, previewMaxBytes)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	out := &PreviewResponse{
		Request:  displayReq,
		Status:   resp.StatusCode,
		Response: PreviewHTTP{Headers: redactHeaders(resp.Header), Body: redactBody(body)},
		Note:     "Test responses are not saved.",
	}
	return out, nil
}

// buildRequest builds the *http.Request the executor will send AND the
// redacted PreviewHTTP that the UI will display.
func (p *PreviewExecutor) buildRequest(
	ctx context.Context, req PreviewRequest, ep EndpointSpec,
) (*http.Request, PreviewHTTP, error) {
	base := strings.TrimRight(req.Spec.BaseURL, "/")
	td := buildToolDef(base, req.Spec.Auth, ep)

	args := req.Args
	if args == nil {
		args = map[string]any{}
	}
	consumed := map[string]bool{}
	rawURL, err := substituteURL(td.URL, args, consumed)
	if err != nil {
		return nil, PreviewHTTP{}, fmt.Errorf("substitute url: %w", err)
	}
	rawURL = appendQueryParams(rawURL, td.QueryParams, args, consumed)

	var bodyBytes []byte
	if methodHasBody(td.Method) && td.BodyMapping != "none" {
		if body := buildBody(args, consumed); body != nil {
			bodyBytes, err = json.Marshal(body)
			if err != nil {
				return nil, PreviewHTTP{}, fmt.Errorf("marshal body: %w", err)
			}
		}
	}

	httpReq, err := http.NewRequestWithContext(ctx, td.Method, rawURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, PreviewHTTP{}, fmt.Errorf("create request: %w", err)
	}
	if len(bodyBytes) > 0 {
		httpReq.Header.Set("Content-Type", "application/json")
	}
	for k, v := range td.Headers {
		httpReq.Header.Set(k, v)
	}
	if err := p.applyRequestAuth(ctx, req.AuthScopeID, httpReq, bodyBytes); err != nil {
		return nil, PreviewHTTP{}, err
	}

	display := PreviewHTTP{
		Method:  httpReq.Method,
		URL:     redactURL(httpReq.URL),
		Headers: redactHeaders(httpReq.Header),
		Body:    redactBody(bodyBytes),
	}
	return httpReq, display, nil
}

func (p *PreviewExecutor) applyRequestAuth(
	ctx context.Context, authScopeID string, req *http.Request, body []byte,
) error {
	if authScopeID == "" {
		return nil
	}
	if p.applyAuth != nil {
		if err := p.applyAuth(ctx, authScopeID, req, body); err != nil {
			return fmt.Errorf("auth request: %w", err)
		}
		return nil
	}
	if p.getAuthHeaders == nil {
		return nil
	}
	ah, err := p.getAuthHeaders(ctx, authScopeID)
	if err != nil {
		return fmt.Errorf("auth headers: %w", err)
	}
	for k, vals := range ah {
		for _, v := range vals {
			req.Header.Set(k, v)
		}
	}
	return nil
}

// findEndpoint linearly searches eps for the named endpoint.
func findEndpoint(eps []EndpointSpec, name string) (EndpointSpec, bool) {
	for _, e := range eps {
		if e.Name == name {
			return e, true
		}
	}
	return EndpointSpec{}, false
}

// readPreviewBody reads up to max bytes, appending a truncation note if
// the body was longer.
func readPreviewBody(r io.Reader, max int) ([]byte, error) {
	limited := io.LimitReader(r, int64(max+1))
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if len(data) > max {
		data = data[:max]
		data = append(data, "\n... [truncated]"...)
	}
	return data, nil
}

// redactHeaders copies h into a flat map[string]string, replacing values
// for any sensitive header name with REDACTED. Header values are joined
// with ", " when multiple are present.
func redactHeaders(h http.Header) map[string]string {
	out := make(map[string]string, len(h))
	keys := make([]string, 0, len(h))
	for k := range h {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		vals := h.Values(k)
		if _, sensitive := sensitiveHeaders[strings.ToLower(k)]; sensitive {
			out[k] = redactedValue
			continue
		}
		out[k] = strings.Join(vals, ", ")
	}
	return out
}

// redactURL strips userinfo and replaces query values for keys whose name
// looks sensitive (api_key, token, secret, password). The path is kept
// verbatim because it commonly contains placeholders the user supplied.
func redactURL(u *url.URL) string {
	if u == nil {
		return ""
	}
	c := *u
	c.User = nil
	q := c.Query()
	for k := range q {
		if sensitiveBodyKeyRe.MatchString(k) {
			q.Set(k, redactedValue)
		}
	}
	c.RawQuery = q.Encode()
	return c.String()
}

// redactBody walks JSON bodies replacing any value whose key matches
// sensitiveBodyKeyRe. Non-JSON or unparseable bodies are returned as-is
// (truncated to previewMaxBytes upstream).
func redactBody(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	var obj any
	if err := json.Unmarshal(body, &obj); err != nil {
		return string(body)
	}
	redactJSONValue(obj)
	out, err := json.MarshalIndent(obj, "", "  ")
	if err != nil {
		return string(body)
	}
	return string(out)
}

// redactJSONValue recursively rewrites any map values whose keys match
// sensitiveBodyKeyRe to the redaction placeholder. Slices are descended.
func redactJSONValue(v any) {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			if sensitiveBodyKeyRe.MatchString(k) {
				x[k] = redactedValue
				continue
			}
			redactJSONValue(val)
		}
	case []any:
		for _, item := range x {
			redactJSONValue(item)
		}
	}
}
