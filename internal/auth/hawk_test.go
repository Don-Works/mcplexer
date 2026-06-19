package auth

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestBuildHawkAuthorization_GetVector(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet, "http://example.com:8000/resource/1?b=1&a=2", nil)
	if err != nil {
		t.Fatal(err)
	}
	header, err := buildHawkAuthorization(req, nil, hawkCredentials{
		ID:        "dh37fgj492je",
		Key:       "werxhqb98rpaxn39848xrunpaw3489ruxnpa98w4rxn",
		Algorithm: "sha256",
		Ext:       "some-app-ext-data",
	}, time.Unix(1353832234, 0), "j4h3g2")
	if err != nil {
		t.Fatalf("buildHawkAuthorization: %v", err)
	}
	for _, want := range []string{
		`Hawk id="dh37fgj492je"`,
		`ts="1353832234"`,
		`nonce="j4h3g2"`,
		`ext="some-app-ext-data"`,
		`mac="6R4rV5iE+NPoym+WwjeHzjAGXUtLNIxmo1vpMofpLAE="`,
	} {
		if !strings.Contains(header, want) {
			t.Fatalf("header %q missing %s", header, want)
		}
	}
}

func TestBuildHawkAuthorization_PostPayloadVector(t *testing.T) {
	body := []byte("Thank you for flying Hawk")
	req, err := http.NewRequest(http.MethodPost, "http://example.com:8000/resource/1?b=1&a=2", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "text/plain")
	header, err := buildHawkAuthorization(req, body, hawkCredentials{
		ID:        "dh37fgj492je",
		Key:       "werxhqb98rpaxn39848xrunpaw3489ruxnpa98w4rxn",
		Algorithm: "sha256",
		Ext:       "some-app-ext-data",
	}, time.Unix(1353832234, 0), "j4h3g2")
	if err != nil {
		t.Fatalf("buildHawkAuthorization: %v", err)
	}
	for _, want := range []string{
		`hash="Yi9LfIIFRtBEPt74PVmbTF/xVAwPn7ub15ePICfgnuY="`,
		`mac="aSe1DERmZuRl3pI36/9BdZmnErTw3sNzOOAUlfeKjVw="`,
	} {
		if !strings.Contains(header, want) {
			t.Fatalf("header %q missing %s", header, want)
		}
	}
}

func TestApplyToRequest_HawkScopeSignsRequest(t *testing.T) {
	st := newFakeAuthScopeStore()
	sm := newTestManager(t, st, "scope-hawk", "hawk", map[string]string{
		"HAWK_ID":  "key-id-1",
		"HAWK_KEY": "super-secret-key",
	})
	inj := NewInjector(sm, nil, st)
	req, err := http.NewRequest(http.MethodGet, "https://app.absence.io/api/v2/users", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := inj.ApplyToRequest(context.Background(), "scope-hawk", req, nil); err != nil {
		t.Fatalf("ApplyToRequest: %v", err)
	}
	got := req.Header.Get("Authorization")
	if !strings.HasPrefix(got, `Hawk id="key-id-1"`) {
		t.Fatalf("Authorization = %q, want Hawk id", got)
	}
	if strings.Contains(got, "super-secret-key") {
		t.Fatalf("Authorization leaked raw key: %q", got)
	}
}

func TestHeadersForDownstream_HawkScopeErrors(t *testing.T) {
	st := newFakeAuthScopeStore()
	sm := newTestManager(t, st, "scope-hawk-static", "hawk", map[string]string{
		"HAWK_ID":  "id",
		"HAWK_KEY": "key",
	})
	inj := NewInjector(sm, nil, st)
	headers, err := inj.HeadersForDownstream(context.Background(), "scope-hawk-static")
	if err == nil {
		t.Fatalf("expected error, got headers=%v", headers)
	}
	if !strings.Contains(err.Error(), "request signing") {
		t.Fatalf("error = %q, want request signing", err)
	}
}
