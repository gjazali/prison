package image

import (
	"strings"
	"testing"

	"prison/internal/plugin"
)

// inmateWanting returns an inmate with the given name and foundation.
func inmateWanting(name, foundation string) *plugin.Inmate {
	inmate := &plugin.Inmate{Name: name}
	inmate.Image.Foundation = foundation
	return inmate
}

// TestResolveFoundationPrefersTheOverride checks that a host
// override wins over inmates and the default.
func TestResolveFoundationPrefersTheOverride(t *testing.T) {
	inmates := []*plugin.Inmate{inmateWanting("claude", "ubuntu:24.04")}
	foundation, err := ResolveFoundation(inmates, "fedora:41")
	if err != nil {
		t.Fatalf("ResolveFoundation: %v", err)
	}
	if foundation != "fedora:41" {
		t.Errorf("got %s, want the override fedora:41", foundation)
	}
}

// TestResolveFoundationFallsBackToTheDefault checks that no
// declared foundation resolves to the default.
func TestResolveFoundationFallsBackToTheDefault(t *testing.T) {
	for _, inmates := range [][]*plugin.Inmate{
		nil,
		{inmateWanting("claude", ""), inmateWanting("codex", "")},
	} {
		foundation, err := ResolveFoundation(inmates, "")
		if err != nil {
			t.Fatalf("ResolveFoundation: %v", err)
		}
		if foundation != DefaultFoundation {
			t.Errorf("got %s, want %s", foundation, DefaultFoundation)
		}
	}
}

// TestResolveFoundationTakesTheOneDeclared checks that a single
// declared foundation is used even when not all inmates declare one.
func TestResolveFoundationTakesTheOneDeclared(t *testing.T) {
	inmates := []*plugin.Inmate{
		inmateWanting("claude", ""),
		inmateWanting("codex", "ubuntu:24.04"),
		inmateWanting("gemini", "ubuntu:24.04"),
	}
	foundation, err := ResolveFoundation(inmates, "")
	if err != nil {
		t.Fatalf("ResolveFoundation: %v", err)
	}
	if foundation != "ubuntu:24.04" {
		t.Errorf("got %s, want ubuntu:24.04", foundation)
	}
}

// TestResolveFoundationRefusesAConflict checks that conflicting
// foundations produce an error naming each foundation, its inmates,
// and both resolutions.
func TestResolveFoundationRefusesAConflict(t *testing.T) {
	inmates := []*plugin.Inmate{
		inmateWanting("codex", "ubuntu:24.04"),
		inmateWanting("claude", "fedora:41"),
		inmateWanting("gemini", "ubuntu:24.04"),
	}
	_, err := ResolveFoundation(inmates, "")
	if err == nil {
		t.Fatal("two foundations were accepted")
	}
	message := err.Error()
	for _, want := range []string{
		"these inmates need different foundation images",
		"fedora:41 for claude",
		"ubuntu:24.04 for codex, gemini",
		"separate projects",
		"PRISON_FOUNDATION",
	} {
		if !strings.Contains(message, want) {
			t.Errorf("the error does not mention %q: %s", want, message)
		}
	}
	if strings.Index(message, "fedora") > strings.Index(message, "ubuntu") {
		t.Errorf("the foundations are not sorted: %s", message)
	}
}
