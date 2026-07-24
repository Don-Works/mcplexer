package distill

import (
	"strings"
	"testing"
)

func TestIsGenericMonitoringTitle(t *testing.T) {
	cases := map[string]bool{
		"new error-class log template on host/src (×3)":       true,
		"observed warn-class monitoring event on host/src":    true,
		"Still unresolved: new error-class log template on x": true,
		"POST /webhook/stripe returning HTTP 500":             false,
		"nginx permission denied on storefront.conf":          false,
		"": true,
	}
	for title, want := range cases {
		if got := IsGenericMonitoringTitle(title); got != want {
			t.Errorf("IsGenericMonitoringTitle(%q)=%v want %v", title, got, want)
		}
	}
}

func TestOperatorAnomalyTitle_UsesSignature(t *testing.T) {
	sample := `2026/07/24 [error] recv() failed (104: Connection reset by peer) while reading response header from upstream`
	got := OperatorAnomalyTitle("error", "prod-1", "stack", 3, true, sample, "")
	if IsGenericMonitoringTitle(got) {
		t.Fatalf("title still generic: %q", got)
	}
	if !strings.Contains(got, "recv() failed") || !strings.Contains(got, "prod-1/stack") {
		t.Fatalf("title missing signature or location: %q", got)
	}
}

func TestImproveMonitoringTitle_ReplacesGeneric(t *testing.T) {
	got := ImproveMonitoringTitle(
		"new error-class log template on prod/stack (×1)",
		"Observed evidence\n- Store API 502 for /products?slug=x\n",
		"", "",
	)
	if IsGenericMonitoringTitle(got) {
		t.Fatalf("still generic: %q", got)
	}
	if !strings.Contains(got, "Store API") {
		t.Fatalf("expected store api signature, got %q", got)
	}
}
