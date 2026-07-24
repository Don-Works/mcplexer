package mcpversion

import (
	"errors"
	"testing"
)

func TestSelect(t *testing.T) {
	for _, version := range Supported() {
		t.Run(version, func(t *testing.T) {
			if got := Select(version); got != version {
				t.Fatalf("Select(%q) = %q, want proposal echoed", version, got)
			}
		})
	}

	for _, version := range []string{"", "2026-01-01", "DRAFT-2026-v1", "not-a-version"} {
		t.Run("unsupported_"+version, func(t *testing.T) {
			if got := Select(version); got != Latest {
				t.Fatalf("Select(%q) = %q, want latest %q", version, got, Latest)
			}
		})
	}
}

func TestValidateSelected(t *testing.T) {
	for _, version := range Supported() {
		if err := ValidateSelected(version); err != nil {
			t.Errorf("ValidateSelected(%q) = %v, want nil", version, err)
		}
	}

	for _, version := range []string{"", "2026-01-01", "DRAFT-2026-v1"} {
		err := ValidateSelected(version)
		if !errors.Is(err, ErrUnsupported) {
			t.Errorf("ValidateSelected(%q) = %v, want ErrUnsupported", version, err)
		}
	}
}

func TestSupportedReturnsCopy(t *testing.T) {
	first := Supported()
	first[0] = "mutated"
	if got := Supported()[0]; got != Version20241105 {
		t.Fatalf("Supported leaked mutable state: first version = %q", got)
	}
}
