package models

import (
	"bytes"
	"io"
	"net/http"
)

// roundTripFunc adapts a function to http.RoundTripper.
type roundTripFunc func(req *http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

// mockClient builds an *http.Client whose transport invokes fn.
func mockClient(fn roundTripFunc) *http.Client {
	return &http.Client{Transport: fn}
}

// jsonResponse builds a canned *http.Response with the given status and
// JSON body.
func jsonResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(bytes.NewBufferString(body)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	}
}
