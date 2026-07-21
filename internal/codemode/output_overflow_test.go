package codemode

import (
	"strings"
	"testing"
)

// T-F: the print-output cap retains omitted bytes so the gateway can stash
// the full stream in CCR instead of discarding it.
func TestOutputCaptureRetainsOverflow(t *testing.T) {
	o := newOutputCapture(1024)
	first := strings.Repeat("a", 700)
	second := strings.Repeat("b", 700) // 324 displayed, 376 overflow
	third := strings.Repeat("c", 500)  // all overflow
	o.WriteString(first)
	o.WriteString(second)
	o.WriteString(third)

	if !o.Truncated() {
		t.Fatal("expected truncation")
	}
	if got := o.Raw(); got != first+second[:324] {
		t.Fatalf("Raw() = %d bytes, want the displayed 1024-byte prefix", len(got))
	}
	overflow := string(o.Overflow())
	if overflow != second[324:]+third {
		t.Fatalf("Overflow() = %d bytes, want the exact omitted suffix (%d bytes)",
			len(overflow), len(second[324:]+third))
	}
	if !o.OverflowComplete() {
		t.Fatal("all omitted bytes fit the retention bound → complete")
	}
	if o.Raw()+overflow != first+second+third {
		t.Fatal("Raw+Overflow must reconstruct the full print stream byte-exactly")
	}
	if o.BytesOmitted() != len(overflow) {
		t.Fatalf("BytesOmitted %d != retained overflow %d", o.BytesOmitted(), len(overflow))
	}
}

func TestOutputCaptureOverflowRetentionBound(t *testing.T) {
	o := newOutputCapture(64)
	huge := strings.Repeat("x", overflowRetainMaxBytes+4096+64)
	o.WriteString(huge)
	if o.OverflowComplete() {
		t.Fatal("past the retention bound, OverflowComplete must be false")
	}
	if len(o.Overflow()) != overflowRetainMaxBytes {
		t.Fatalf("retained %d bytes, want exactly the %d retention bound",
			len(o.Overflow()), overflowRetainMaxBytes)
	}
	if o.BytesOmitted() != len(huge)-64 {
		t.Fatalf("BytesOmitted %d, want %d", o.BytesOmitted(), len(huge)-64)
	}
}
