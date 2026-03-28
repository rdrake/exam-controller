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
	matched, _ := regexp.MatchString(`^[a-z][a-z0-9]{7}$`, s)
	if !matched {
		t.Errorf("slug %q does not match DNS-1035 label pattern (must start with letter)", s)
	}
}

func TestGenerate_FirstCharAlwaysLetter(t *testing.T) {
	for i := range 200 {
		s, err := Generate()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if s[0] < 'a' || s[0] > 'z' {
			t.Fatalf("slug %q starts with non-letter on iteration %d", s, i)
		}
	}
}

func TestGenerate_Unique(t *testing.T) {
	seen := make(map[string]bool)
	for i := range 100 {
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
