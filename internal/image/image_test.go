package image

import (
	"strings"
	"testing"

	"prison/internal/plugin"
)

func inmateWanting(name, foundation string) *plugin.Inmate {
	inmate := &plugin.Inmate{Name: name}
	inmate.Image.Foundation = foundation
	return inmate
}

func TestResolveFoundationPrefersTheOverride(t *testing.T) {
	inmates := []*plugin.Inmate{inmateWanting("claude", "ubuntu:24.04")}
	foundation, err := ResolveFoundation(inmates, "fedora:41")
	if err != nil {
		t.Fatalf("ResolveFoundation error = %v, want nil", err)
	}
	if foundation != "fedora:41" {
		t.Errorf("ResolveFoundation = %s, want fedora:41", foundation)
	}
}

func TestResolveFoundationFallsBackToTheDefault(t *testing.T) {
	for _, inmates := range [][]*plugin.Inmate{
		nil,
		{inmateWanting("claude", ""), inmateWanting("codex", "")},
	} {
		foundation, err := ResolveFoundation(inmates, "")
		if err != nil {
			t.Fatalf("ResolveFoundation error = %v, want nil", err)
		}
		if foundation != DefaultFoundation {
			t.Errorf("ResolveFoundation = %s, want %s", foundation, DefaultFoundation)
		}
	}
}

func TestResolveFoundationTakesTheOneDeclared(t *testing.T) {
	inmates := []*plugin.Inmate{
		inmateWanting("claude", ""),
		inmateWanting("codex", "ubuntu:24.04"),
		inmateWanting("gemini", "ubuntu:24.04"),
	}
	foundation, err := ResolveFoundation(inmates, "")
	if err != nil {
		t.Fatalf("ResolveFoundation error = %v, want nil", err)
	}
	if foundation != "ubuntu:24.04" {
		t.Errorf("ResolveFoundation = %s, want ubuntu:24.04", foundation)
	}
}

func TestResolveFoundationRefusesAConflict(t *testing.T) {
	inmates := []*plugin.Inmate{
		inmateWanting("codex", "ubuntu:24.04"),
		inmateWanting("claude", "fedora:41"),
		inmateWanting("gemini", "ubuntu:24.04"),
	}
	_, err := ResolveFoundation(inmates, "")
	if err == nil {
		t.Fatal("ResolveFoundation error = nil, want an error")
	}
	message := err.Error()
	for _, want := range []string{
		"the inmates need different foundation images",
		"fedora:41 for claude",
		"ubuntu:24.04 for codex, gemini",
		"separate projects",
		"PRISON_FOUNDATION",
	} {
		if !strings.Contains(message, want) {
			t.Errorf("error = %s, want it to contain %q", message, want)
		}
	}
	if strings.Index(message, "fedora") > strings.Index(message, "ubuntu") {
		t.Errorf("error = %s, want sorted foundations", message)
	}
}
