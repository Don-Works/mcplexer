package addon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	neturl "net/url"
	"regexp"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/netguard"
)

// maxResponseBytes is the maximum response body size before truncation.
const maxResponseBytes = 200 * 1024 // 200KB

// placeholderRe matches {{param}} placeholders in URLs and query params.
var placeholderRe = regexp.MustCompile(`\{\{(\w+)\}\}`)

// AuthHeaderFunc returns auth headers for the given auth scope (server ID).
type AuthHeaderFunc func(ctx context.Context, authScopeID string) (http.Header, error)

// AuthRequestFunc applies auth to a concrete outbound request. Request-bound
// schemes such as Hawk need the method, URL, headers, and body to be final
// before they can compute an Authorization header.
type AuthRequestFunc func(
	ctx context.Context, authScopeID string, req *http.Request, body []byte,
) error

// Executor makes HTTP requests for addon tools.
type Executor struct {
	getAuthHeaders AuthHeaderFunc
	applyAuth      AuthRequestFunc
	client         *http.Client
}

// NewExecutor creates an Executor that uses authFn to obtain OAuth headers.
func NewExecutor(authFn AuthHeaderFunc) *Executor {
	return NewExecutorWithRequestAuth(authFn, nil)
}

// NewExecutorWithRequestAuth creates an Executor that can use request-bound
// auth. If requestAuthFn is nil, the executor falls back to authFn.
func NewExecutorWithRequestAuth(authFn AuthHeaderFunc, requestAuthFn AuthRequestFunc) *Executor {
	return &Executor{
		getAuthHeaders: authFn,
		applyAuth:      requestAuthFn,
		client:         netguard.NewPublicHTTPClient(30 * time.Second),
	}
}

// callToolResult mirrors the MCP CallToolResult structure.
type callToolResult struct {
	Content []toolContent `json:"content"`
	IsError bool          `json:"isError,omitempty"`
}

type toolContent struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// Execute runs an addon tool call by making the configured HTTP request.
func (e *Executor) Execute(
	ctx context.Context,
	tool *ResolvedTool,
	authScopeID string,
	args json.RawMessage,
) (json.RawMessage, error) {
	// Parse arguments. Decode with UseNumber so JSON numbers are preserved as
	// json.Number (their exact source text) rather than float64 — otherwise a
	// large integer path/query param like an id of 55215543 formats via "%v"
	// as scientific notation ("5.5215543e+07"), corrupting the URL into a 404.
	var params map[string]any
	if len(args) > 0 {
		dec := json.NewDecoder(bytes.NewReader(args))
		dec.UseNumber()
		if err := dec.Decode(&params); err != nil {
			return nil, fmt.Errorf("unmarshal args: %w", err)
		}
	}
	if params == nil {
		params = make(map[string]any)
	}

	consumed := make(map[string]bool)

	// Build URL with placeholder substitution.
	url, err := substituteURL(tool.URL, params, consumed)
	if err != nil {
		return nil, fmt.Errorf("build url: %w", err)
	}

	// Build query string.
	url = appendQueryParams(url, tool.QueryParams, params, consumed)

	// Build request body for methods that support it.
	var bodyReader io.Reader
	var bodyBytes []byte
	method := strings.ToUpper(tool.Method)
	if methodHasBody(method) && tool.BodyMapping != "none" {
		body := buildBody(params, consumed)
		if body != nil {
			encoded, err := json.Marshal(body)
			if err != nil {
				return nil, fmt.Errorf("marshal body: %w", err)
			}
			bodyBytes = encoded
			bodyReader = bytes.NewReader(bodyBytes)
		}
	}

	// Create HTTP request.
	httpReq, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	if bodyReader != nil {
		httpReq.Header.Set("Content-Type", "application/json")
	}

	// Apply static headers from the tool definition.
	for k, v := range tool.Headers {
		httpReq.Header.Set(k, v)
	}

	if err := e.applyRequestAuth(ctx, authScopeID, httpReq, bodyBytes); err != nil {
		return nil, err
	}

	// Validate and pin DNS to prevent SSRF / DNS rebinding.
	pinnedCtx, err := netguard.PinPublicHosts(httpReq.Context(), httpReq.URL.Hostname())
	if err != nil {
		return nil, fmt.Errorf("addon request: %w", err)
	}
	httpReq = httpReq.WithContext(pinnedCtx)

	// Execute request.
	resp, err := e.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// Read response body with truncation.
	respBody, err := readTruncated(resp.Body, maxResponseBytes)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	slog.Debug("addon tool executed",
		"tool", tool.FullName,
		"status", resp.StatusCode,
		"body_len", len(respBody),
	)

	// Build MCP-format result.
	result := callToolResult{
		Content: []toolContent{{
			Type: "text",
			Text: string(respBody),
		}},
	}

	if resp.StatusCode >= 400 {
		result.IsError = true
		msg := fmt.Sprintf("HTTP %d: %s", resp.StatusCode, respBody)
		if ra := resp.Header.Get("Retry-After"); ra != "" && resp.StatusCode == http.StatusTooManyRequests {
			msg = fmt.Sprintf("HTTP 429 (retry after %s): %s", ra, respBody)
		}
		result.Content[0].Text = msg
	}

	return json.Marshal(result)
}

func (e *Executor) applyRequestAuth(
	ctx context.Context, authScopeID string, req *http.Request, body []byte,
) error {
	if authScopeID == "" {
		return nil
	}
	if e.applyAuth != nil {
		if err := e.applyAuth(ctx, authScopeID, req, body); err != nil {
			return fmt.Errorf("apply auth: %w", err)
		}
		return nil
	}
	if e.getAuthHeaders == nil {
		return nil
	}
	authHeaders, err := e.getAuthHeaders(ctx, authScopeID)
	if err != nil {
		return fmt.Errorf("get auth headers: %w", err)
	}
	for k, vals := range authHeaders {
		for _, v := range vals {
			req.Header.Set(k, v)
		}
	}
	return nil
}

// substituteURL replaces {{param}} placeholders in the URL template. Each
// substituted value is URL-path-escaped so a malicious tool argument cannot
// inject a different host, traverse out of the intended path, or smuggle
// query parameters that ride on the user's OAuth token. Validators reject
// values that would still escape the path even after percent-encoding (e.g.
// raw `..` segments are blocked).
func substituteURL(tmpl string, params map[string]any, consumed map[string]bool) (string, error) {
	var lastErr error
	result := placeholderRe.ReplaceAllStringFunc(tmpl, func(match string) string {
		name := placeholderRe.FindStringSubmatch(match)[1]
		val, ok := params[name]
		if !ok {
			lastErr = fmt.Errorf("missing required url param %q", name)
			return match
		}
		consumed[name] = true
		raw := fmt.Sprintf("%v", val)
		if strings.Contains(raw, "..") || strings.ContainsAny(raw, "\r\n") {
			lastErr = fmt.Errorf("invalid value for url param %q: rejected suspicious characters", name)
			return match
		}
		return neturl.PathEscape(raw)
	})
	if lastErr != nil {
		return "", lastErr
	}
	return result, nil
}

// appendQueryParams adds query parameters to the URL, substituting
// {{param}} references. Missing optional params are silently skipped.
func appendQueryParams(
	url string,
	queryDefs map[string]string,
	params map[string]any,
	consumed map[string]bool,
) string {
	if len(queryDefs) == 0 {
		return url
	}

	sep := "?"
	if strings.Contains(url, "?") {
		sep = "&"
	}

	var parts []string
	for key, valTmpl := range queryDefs {
		// Substitute placeholders directly into the unescaped value, then
		// percent-encode the entire result. Substituting before encoding
		// (the previous behaviour) accidentally double-encoded literal `=`
		// or `&` baked into the template, but never escaped placeholder
		// values themselves; the new behaviour escapes once, correctly.
		resolved := placeholderRe.ReplaceAllStringFunc(valTmpl, func(match string) string {
			name := placeholderRe.FindStringSubmatch(match)[1]
			val, ok := params[name]
			if !ok {
				return ""
			}
			consumed[name] = true
			return fmt.Sprintf("%v", val)
		})
		if resolved == "" {
			continue // skip empty/missing optional params
		}
		parts = append(parts, neturl.QueryEscape(key)+"="+neturl.QueryEscape(resolved))
	}

	if len(parts) == 0 {
		return url
	}
	return url + sep + strings.Join(parts, "&")
}

// buildBody creates a request body from arguments not consumed by URL or query params.
func buildBody(params map[string]any, consumed map[string]bool) map[string]any {
	body := make(map[string]any)
	for k, v := range params {
		if !consumed[k] {
			body[k] = v
		}
	}
	if len(body) == 0 {
		return nil
	}
	return body
}

// methodHasBody returns true for HTTP methods that typically carry a body.
func methodHasBody(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch:
		return true
	}
	return false
}

// readTruncated reads up to maxBytes from r, appending a truncation notice
// if the response exceeds the limit.
func readTruncated(r io.Reader, maxBytes int) ([]byte, error) {
	limited := io.LimitReader(r, int64(maxBytes+1))
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if len(data) > maxBytes {
		data = data[:maxBytes]
		data = append(data, "\n... [truncated at 200KB]"...)
	}
	return data, nil
}
