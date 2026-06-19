package gateway

import "testing"

func TestErrorTracker_ThresholdDetection(t *testing.T) {
	var tracker errorTracker

	// First two errors should not trigger.
	if tracker.RecordError("err1") {
		t.Error("should not trigger on 1st error")
	}
	if tracker.RecordError("err2") {
		t.Error("should not trigger on 2nd error")
	}

	// Third error should trigger.
	if !tracker.RecordError("err3") {
		t.Error("should trigger on 3rd error")
	}
}

func TestErrorTracker_ResetOnSuccess(t *testing.T) {
	var tracker errorTracker

	tracker.RecordError("err1")
	tracker.RecordError("err2")
	tracker.RecordSuccess()

	// After reset, should not trigger on next error.
	if tracker.RecordError("err3") {
		t.Error("should not trigger after success reset")
	}
}

func TestErrorTracker_ContinuesTriggeringAboveThreshold(t *testing.T) {
	var tracker errorTracker

	tracker.RecordError("err1")
	tracker.RecordError("err2")

	// 3rd and subsequent errors should all trigger.
	if !tracker.RecordError("err3") {
		t.Error("3rd error should trigger")
	}
	if !tracker.RecordError("err4") {
		t.Error("4th error should also trigger")
	}
}

func TestErrorTracker_GuidanceSQL(t *testing.T) {
	var tracker errorTracker

	tracker.RecordError("ERROR: column \"foo\" does not exist")
	tracker.RecordError("ERROR: relation \"bar\" does not exist")
	tracker.RecordError("ERROR: syntax error at or near \"SELECT\"")

	guidance := tracker.Guidance()
	if guidance == "" {
		t.Fatal("expected guidance, got empty string")
	}
	if want := "information_schema"; !containsAny(guidance, want) {
		t.Errorf("expected guidance to mention %q, got %q", want, guidance)
	}
}

func TestErrorTracker_GuidanceTypeMismatch(t *testing.T) {
	var tracker errorTracker

	tracker.RecordError("cannot unmarshal string into Go value of type map")
	tracker.RecordError("cannot unmarshal string into Go value of type map")
	tracker.RecordError("cannot unmarshal string into Go value of type map")

	guidance := tracker.Guidance()
	if want := "objects/arrays"; !containsAny(guidance, want) {
		t.Errorf("expected guidance to mention %q, got %q", want, guidance)
	}
}

func TestErrorTracker_GuidanceReadOnly(t *testing.T) {
	var tracker errorTracker

	tracker.RecordError("ERROR: permission denied for table users")
	tracker.RecordError("ERROR: cannot execute DELETE in a read-only transaction")
	tracker.RecordError("ERROR: cannot execute UPDATE in a read-only transaction")

	guidance := tracker.Guidance()
	if want := "SELECT"; !containsAny(guidance, want) {
		t.Errorf("expected guidance to mention %q, got %q", want, guidance)
	}
}

func TestErrorTracker_GuidanceDefault(t *testing.T) {
	var tracker errorTracker

	tracker.RecordError("unknown error foo")
	tracker.RecordError("unknown error bar")
	tracker.RecordError("unknown error baz")

	guidance := tracker.Guidance()
	if want := "Multiple consecutive errors"; !containsAny(guidance, want) {
		t.Errorf("expected default guidance, got %q", guidance)
	}
}
