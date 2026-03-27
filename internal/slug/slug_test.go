package slug

import (
	"regexp"
	"testing"
)

func TestGenerate_Length(t *testing.T) {
	s, err := Generate()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(s) != 8 {
		t.Errorf("expected length 8, got %d: %q", len(s), s)
	}
}

func TestGenerate_DNSSafe(t *testing.T) {
	s, err := Generate()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	matched, _ := regexp.MatchString(`^[a-z0-9]{8}$`, s)
	if !matched {
		t.Errorf("slug %q is not DNS-safe lowercase alphanumeric", s)
	}
}

func TestGenerate_Unique(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 100; i++ {
		s, err := Generate()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if seen[s] {
			t.Errorf("duplicate slug %q after %d generations", s, i)
		}
		seen[s] = true
	}
}
