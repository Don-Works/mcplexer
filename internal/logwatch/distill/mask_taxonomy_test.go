package distill

import (
	"strings"
	"testing"
)

func TestNormalize_PreservesTaxonomyButCapturesValues(t *testing.T) {
	first, values := NormalizeWithValues(
		`duplicate key violates unique constraint "orders_invoice_number_key" orderNum=SO-900001 request_id=550e8400-e29b-41d4-a716-446655440000`,
	)
	second := Normalize(
		`duplicate key violates unique constraint "orders_customer_key" orderNum=SO-900002 request_id=ea3ef4c0-ae0c-4d2b-b2d8-a184b30c12ca`,
	)
	if first == second {
		t.Fatal("different constraint names collapsed into one template")
	}
	if !strings.Contains(first, `constraint "orders_invoice_number_key"`) ||
		!strings.Contains(first, "orderNum=SO-<n>") {
		t.Fatalf("taxonomy/value masking mismatch: %q", first)
	}
	got := map[string]string{}
	for _, value := range values {
		got[value.Field] = value.Value
	}
	if got["ordernum"] != "SO-900001" || got["request_id"] == "" {
		t.Fatalf("masked value evidence: %+v", values)
	}
}

func TestNormalize_PreservesCodeLocations(t *testing.T) {
	first := Normalize(`ERROR app/orders.go:91 duplicate key orderNum=SO-100001`)
	second := Normalize(`ERROR app/orders.go:142 duplicate key orderNum=SO-100002`)
	if first == second {
		t.Fatalf("distinct code locations collapsed: %q", first)
	}
	if !strings.Contains(first, "app/orders.go:91") || !strings.Contains(first, "SO-<n>") {
		t.Fatalf("location/value taxonomy mismatch: %q", first)
	}
}
