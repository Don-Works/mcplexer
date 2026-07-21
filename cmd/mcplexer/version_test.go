package main

import "testing"

func TestFormatBuildVersionAppendsCommitToExactTag(t *testing.T) {
	got := formatBuildVersion("v0.26.4", "abcdef1234567890", false)
	if want := "v0.26.4+abcdef123456"; got != want {
		t.Fatalf("formatBuildVersion() = %q, want %q", got, want)
	}
}

func TestFormatBuildVersionKeepsDescribeWithCommit(t *testing.T) {
	got := formatBuildVersion("v0.26.3-1-gabcdef123456", "abcdef1234567890", false)
	if want := "v0.26.3-1-gabcdef123456"; got != want {
		t.Fatalf("formatBuildVersion() = %q, want %q", got, want)
	}
}

func TestFormatBuildVersionFallsBackToDevCommitDirty(t *testing.T) {
	got := formatBuildVersion("", "abcdef1234567890", true)
	if want := "dev+abcdef123456-dirty"; got != want {
		t.Fatalf("formatBuildVersion() = %q, want %q", got, want)
	}
}
