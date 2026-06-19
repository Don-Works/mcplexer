package addon_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/don-works/mcplexer/internal/addon"
	"github.com/don-works/mcplexer/internal/netguard"
)

// fakeAuthHeader returns the same bearer token regardless of scope ID. The
// preview executor must request this header and then redact its value
// before returning the displayed request to the UI.
func fakeAuthHeader(want string) addon.AuthHeaderFunc {
	return func(_ context.Context, _ string) (http.Header, error) {
		h := http.Header{}
		h.Set("Authorization", "Bearer "+want)
		return h, nil
	}
}

func allowPrivateFetch(t *testing.T) {
	t.Helper()
	t.Setenv(netguard.AllowPrivateFetchEnv, "1")
}

// captureServer is an httptest.Server that records the request it received
// and replies with a canned response. Useful for asserting auth honoring +
// redaction in one round trip.
type captureServer struct {
	srv         *httptest.Server
	gotAuth     string
	gotBody     []byte
	gotMethod   string
	gotPath     string
	respHeaders map[string]string
	respBody    string
	respStatus  int
}

func newCaptureServer() *captureServer {
	c := &captureServer{
		respHeaders: map[string]string{},
		respStatus:  200,
	}
	c.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.gotAuth = r.Header.Get("Authorization")
		c.gotMethod = r.Method
		c.gotPath = r.URL.Path + "?" + r.URL.RawQuery
		body, _ := io.ReadAll(r.Body)
		c.gotBody = body
		for k, v := range c.respHeaders {
			w.Header().Set(k, v)
		}
		w.WriteHeader(c.respStatus)
		_, _ = io.WriteString(w, c.respBody)
	}))
	return c
}

func (c *captureServer) close() { c.srv.Close() }

func bearerSpec(baseURL string) addon.AddonSpec {
	return addon.AddonSpec{
		Name:         "weatherco",
		Description:  "Public weather API",
		BaseURL:      baseURL,
		ParentServer: "weatherco-server",
		Auth:         addon.AuthSpec{Kind: addon.AuthBearer},
		Endpoints: []addon.EndpointSpec{
			{
				Name: "echo", Description: "echo back",
				Method: "POST", Path: "/echo",
				Params: []addon.ParamSpec{
					{Name: "secret_key", Type: "string", In: "body", Required: true},
					{Name: "msg", Type: "string", In: "body"},
				},
			},
		},
	}
}

func TestPreviewExecutor_HonorsAuth(t *testing.T) {
	allowPrivateFetch(t)
	cs := newCaptureServer()
	defer cs.close()
	cs.respBody = `{"ok":true}`

	exec := addon.NewPreviewExecutor(fakeAuthHeader("super-secret-token"))
	res, err := exec.Run(context.Background(), addon.PreviewRequest{
		Spec:        bearerSpec(cs.srv.URL),
		Endpoint:    "echo",
		Args:        map[string]any{"secret_key": "abc", "msg": "hello"},
		AuthScopeID: "scope-1",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if cs.gotAuth != "Bearer super-secret-token" {
		t.Fatalf("server got Authorization=%q, want Bearer super-secret-token", cs.gotAuth)
	}
	if cs.gotMethod != "POST" {
		t.Errorf("server got method=%q, want POST", cs.gotMethod)
	}
	// Display request must redact the Authorization header.
	if got := res.Request.Headers["Authorization"]; got != "REDACTED" {
		t.Errorf("display Authorization = %q, want REDACTED", got)
	}
	// Response status flows through.
	if res.Status != 200 {
		t.Errorf("status = %d, want 200", res.Status)
	}
	// Persistence note is set so the UI banner has stable copy.
	if !strings.Contains(res.Note, "not saved") {
		t.Errorf("note = %q, want it to mention not saved", res.Note)
	}
}

func TestPreviewExecutor_RedactsRequestBody(t *testing.T) {
	allowPrivateFetch(t)
	cs := newCaptureServer()
	defer cs.close()
	cs.respBody = `{"ok":true}`

	exec := addon.NewPreviewExecutor(fakeAuthHeader("token"))
	res, err := exec.Run(context.Background(), addon.PreviewRequest{
		Spec:     bearerSpec(cs.srv.URL),
		Endpoint: "echo",
		Args: map[string]any{
			"secret_key": "shhh",
			"msg":        "hello",
		},
		AuthScopeID: "scope-1",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// The displayed request body must redact secret_key but keep msg.
	if !strings.Contains(res.Request.Body, "REDACTED") {
		t.Errorf("display body missing REDACTED: %s", res.Request.Body)
	}
	if strings.Contains(res.Request.Body, "shhh") {
		t.Errorf("display body leaked secret: %s", res.Request.Body)
	}
	if !strings.Contains(res.Request.Body, "hello") {
		t.Errorf("display body lost non-secret: %s", res.Request.Body)
	}
	// The wire-level request body still contains the real secret —
	// redaction is a UI concern only, not an HTTP-layer mutation.
	var wire map[string]any
	if err := json.Unmarshal(cs.gotBody, &wire); err != nil {
		t.Fatalf("server body not json: %v", err)
	}
	if wire["secret_key"] != "shhh" {
		t.Errorf("server got %v, want shhh — preview must not mutate the live call", wire["secret_key"])
	}
}

func TestPreviewExecutor_RedactsResponseHeadersAndBody(t *testing.T) {
	allowPrivateFetch(t)
	cs := newCaptureServer()
	defer cs.close()
	cs.respHeaders["Set-Cookie"] = "session=abcdef; HttpOnly"
	cs.respHeaders["X-Api-Key"] = "leak-me-please"
	cs.respBody = `{"access_token":"sk-live-XXXX","other":"keep","nested":{"password":"p","ok":1},"list":[{"refresh_token":"rt"}]}`

	exec := addon.NewPreviewExecutor(fakeAuthHeader("t"))
	res, err := exec.Run(context.Background(), addon.PreviewRequest{
		Spec:     bearerSpec(cs.srv.URL),
		Endpoint: "echo",
		Args:     map[string]any{"secret_key": "k", "msg": "m"},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// Sensitive headers redacted.
	for _, k := range []string{"Set-Cookie", "X-Api-Key"} {
		if got := res.Response.Headers[k]; got != "REDACTED" {
			t.Errorf("response header %s = %q, want REDACTED", k, got)
		}
	}
	// Sensitive body keys redacted but other keys retained.
	body := res.Response.Body
	for _, leak := range []string{"sk-live-XXXX", "rt", `"p"`} {
		if strings.Contains(body, leak) {
			t.Errorf("response body leaked %q: %s", leak, body)
		}
	}
	if !strings.Contains(body, "REDACTED") {
		t.Errorf("response body missing REDACTED: %s", body)
	}
	if !strings.Contains(body, "keep") {
		t.Errorf("response body lost non-secret 'keep': %s", body)
	}
}

func TestPreviewExecutor_RedactsSensitiveQueryParams(t *testing.T) {
	allowPrivateFetch(t)
	cs := newCaptureServer()
	defer cs.close()
	cs.respBody = `{}`

	spec := addon.AddonSpec{
		Name: "kapi", Description: "demo", BaseURL: cs.srv.URL,
		ParentServer: "kapi-srv",
		Auth:         addon.AuthSpec{Kind: addon.AuthAPIKeyQuery, QueryName: "api_key"},
		Endpoints: []addon.EndpointSpec{{
			Name: "list_things", Description: "list", Method: "GET", Path: "/things",
		}},
	}
	exec := addon.NewPreviewExecutor(nil)
	res, err := exec.Run(context.Background(), addon.PreviewRequest{
		Spec: spec, Endpoint: "list_things",
		Args: map[string]any{"_api_key": "real-key-do-not-leak"},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.Contains(res.Request.URL, "real-key-do-not-leak") {
		t.Errorf("display URL leaked api_key: %s", res.Request.URL)
	}
	if !strings.Contains(res.Request.URL, "REDACTED") {
		t.Errorf("display URL missing REDACTED: %s", res.Request.URL)
	}
}

func TestPreviewExecutor_RejectsPrivateBaseURL(t *testing.T) {
	exec := addon.NewPreviewExecutor(nil)
	_, err := exec.Run(context.Background(), addon.PreviewRequest{
		Spec:     bearerSpec("http://127.0.0.1:8080"),
		Endpoint: "echo",
		Args:     map[string]any{"secret_key": "abc", "msg": "hello"},
	})
	if err == nil || !strings.Contains(err.Error(), "preview request") {
		t.Fatalf("expected preview request error for private base_url, got %v", err)
	}
}

func TestPreviewExecutor_RejectsUnknownEndpoint(t *testing.T) {
	exec := addon.NewPreviewExecutor(fakeAuthHeader("t"))
	_, err := exec.Run(context.Background(), addon.PreviewRequest{
		Spec:     bearerSpec("https://example.invalid"),
		Endpoint: "does_not_exist",
	})
	if err == nil || !strings.Contains(err.Error(), "endpoint") {
		t.Fatalf("expected endpoint error, got %v", err)
	}
}
