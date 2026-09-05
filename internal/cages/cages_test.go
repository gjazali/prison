package cages

import (
	"strings"
	"testing"
)

// TestLookupFindsTheDefault checks that the default cage name
// resolves and the cage reports that name.
func TestLookupFindsTheDefault(t *testing.T) {
	found, err := Lookup(DefaultName)
	if err != nil {
		t.Fatalf("Lookup(%q): %v", DefaultName, err)
	}
	if found.Name() != DefaultName {
		t.Errorf("the cage calls itself %q", found.Name())
	}
	if found.Description() == "" {
		t.Error("the cage has no description")
	}
}

// TestLookupRefusesAnUnknownName checks that the error mentions
// both the bad name and the available cages.
func TestLookupRefusesAnUnknownName(t *testing.T) {
	found, err := Lookup("docker")
	if err == nil {
		t.Fatalf("an unknown cage resolved to %v", found)
	}
	if !strings.Contains(err.Error(), "docker") ||
		!strings.Contains(err.Error(), DefaultName) {
		t.Errorf("error does not name both: %v", err)
	}
}

// TestNamesHoldsTheDefault checks that Names is non-empty and starts
// with the default cage name.
func TestNamesHoldsTheDefault(t *testing.T) {
	names := Names()
	if len(names) == 0 {
		t.Fatal("no cages are compiled in")
	}
	for _, name := range names {
		if _, err := Lookup(name); err != nil {
			t.Errorf("listed cage %q does not resolve: %v",
				name, err)
		}
	}
	if names[0] != DefaultName {
		t.Errorf("the listing starts with %q", names[0])
	}
}
