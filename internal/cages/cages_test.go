package cages

import (
	"strings"
	"testing"
)

func TestLookupFindsTheDefault(t *testing.T) {
	found, err := Lookup(DefaultName, Options{})
	if err != nil {
		t.Fatalf("Lookup(%q): %v", DefaultName, err)
	}
	if found.Name() != DefaultName {
		t.Errorf("Name() = %q, want %q", found.Name(), DefaultName)
	}
	if found.Description() == "" {
		t.Error(`Description() = "", want text`)
	}
}

func TestLookupRefusesAnUnknownName(t *testing.T) {
	found, err := Lookup("docker", Options{})
	if err == nil {
		t.Fatalf("Lookup = %v, want error", found)
	}
	if !strings.Contains(err.Error(), "docker") ||
		!strings.Contains(err.Error(), DefaultName) {
		t.Errorf("error = %v, want both names", err)
	}
}

func TestNamesHoldsTheDefault(t *testing.T) {
	names := Names()
	if len(names) == 0 {
		t.Fatal("Names() is empty")
	}
	for _, name := range names {
		if _, err := Lookup(name, Options{}); err != nil {
			t.Errorf("Lookup(%q): %v", name, err)
		}
	}
	if names[0] != DefaultName {
		t.Errorf("Names()[0] = %q, want %q", names[0], DefaultName)
	}
}
