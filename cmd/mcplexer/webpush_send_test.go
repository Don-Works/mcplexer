package main

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestWebPushRetryableStatus(t *testing.T) {
	for _, status := range []int{
		http.StatusRequestTimeout, http.StatusTooEarly,
		http.StatusTooManyRequests, http.StatusInternalServerError,
	} {
		if !webPushRetryableStatus(status) {
			t.Errorf("status %d should retry", status)
		}
	}
	for _, status := range []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusGone} {
		if webPushRetryableStatus(status) {
			t.Errorf("status %d should not retry", status)
		}
	}
}

func TestScrubPushEndpoint(t *testing.T) {
	endpoint := "https://push.example/send/secret-token"
	got := scrubPushEndpoint("POST "+endpoint+": connection reset", endpoint)
	if strings.Contains(got, "secret-token") || !strings.Contains(got, "[redacted-push-endpoint]") {
		t.Fatalf("scrubbed error=%q", got)
	}
}

func TestWaitWebPushRetryHonoursCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := waitWebPushRetry(ctx, time.Second); err == nil {
		t.Fatal("cancelled retry wait returned nil")
	}
}
