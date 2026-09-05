package config

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestReadOverridesDefaults checks that an environment with only HOME
// returns defaults and no overrides.
func TestReadOverridesDefaults(t *testing.T) {
	overrides, err := ReadOverrides(mapGetenv(map[string]string{
		"HOME": "/Users/example",
	}))
	if err != nil {
		t.Fatalf("ReadOverrides: %v", err)
	}
	if overrides.Inmates != nil || overrides.Cage != nil ||
		overrides.Pager != nil || overrides.DiffTool != nil ||
		overrides.CPUs != nil || overrides.Memory != nil ||
		overrides.Sudo != nil || overrides.Ports != nil {
		t.Errorf("an empty environment set something: %+v", overrides)
	}
	if overrides.Root != filepath.Join("/Users/example", ".prison") {
		t.Errorf("root = %q, want it under HOME", overrides.Root)
	}
	if overrides.Domain != "prison" || overrides.Network != "prison" {
		t.Errorf("domain = %q, network = %q, want prison for both",
			overrides.Domain, overrides.Network)
	}
	if overrides.BrokerPort != 8787 {
		t.Errorf("broker port = %d, want 8787", overrides.BrokerPort)
	}
	if overrides.PortBase != 40000 || overrides.PortLimit != 40100 {
		t.Errorf("port range = %d to %d, want 40000 to 40100",
			overrides.PortBase, overrides.PortLimit)
	}
}

// TestReadOverridesReadsEverything sets every variable and checks
// each value.
func TestReadOverridesReadsEverything(t *testing.T) {
	overrides, err := ReadOverrides(mapGetenv(map[string]string{
		"HOME":               "/Users/example",
		"PRISON_INMATES":     "claude some-tool",
		"PRISON_CAGE":        "apple-container",
		"PRISON_PAGER":       "less -R",
		"PRISON_DIFF_TOOL":   "delta",
		"PRISON_CPUS":        "2",
		"PRISON_MEMORY":      "512M",
		"PRISON_SUDO":        "1",
		"PRISON_PORTS":       "3000 8080",
		"PRISON_IMAGE":       "prison/box:local",
		"PRISON_FOUNDATION":  "node:24-bookworm",
		"PRISON_ROOT":        "/tmp/prison-root",
		"PRISON_DOMAIN":      "cells",
		"PRISON_NETWORK":     "cells",
		"PRISON_BROKER_PORT": "9000",
		"PRISON_PORT_BASE":   "41000",
		"PRISON_PORT_LIMIT":  "41100",
	}))
	if err != nil {
		t.Fatalf("ReadOverrides: %v", err)
	}
	if strings.Join(*overrides.Inmates, ",") != "claude,some-tool" {
		t.Errorf("inmates = %v, want two of them", *overrides.Inmates)
	}
	if *overrides.Cage != "apple-container" {
		t.Errorf("cage = %q", *overrides.Cage)
	}
	if *overrides.Pager != "less -R" || *overrides.DiffTool != "delta" {
		t.Errorf("pager = %q, diff tool = %q",
			*overrides.Pager, *overrides.DiffTool)
	}
	if *overrides.CPUs != 2 || *overrides.Memory != "512M" {
		t.Errorf("cpus = %d, memory = %q", *overrides.CPUs, *overrides.Memory)
	}
	if !*overrides.Sudo {
		t.Errorf("sudo = false, want true for PRISON_SUDO=1")
	}
	if len(*overrides.Ports) != 2 || (*overrides.Ports)[1] != 8080 {
		t.Errorf("ports = %v, want 3000 and 8080", *overrides.Ports)
	}
	if overrides.Image != "prison/box:local" ||
		overrides.Foundation != "node:24-bookworm" {
		t.Errorf("image = %q, foundation = %q",
			overrides.Image, overrides.Foundation)
	}
	if overrides.Root != "/tmp/prison-root" {
		t.Errorf("root = %q, want PRISON_ROOT to win", overrides.Root)
	}
	if overrides.Domain != "cells" || overrides.Network != "cells" {
		t.Errorf("domain = %q, network = %q",
			overrides.Domain, overrides.Network)
	}
	if overrides.BrokerPort != 9000 || overrides.PortBase != 41000 ||
		overrides.PortLimit != 41100 {
		t.Errorf("ports = %d, %d, %d", overrides.BrokerPort,
			overrides.PortBase, overrides.PortLimit)
	}
}

// TestReadOverridesSetButEmpty checks that setting a variable to
// empty is distinct from not setting it.
func TestReadOverridesSetButEmpty(t *testing.T) {
	overrides, err := ReadOverrides(mapGetenv(map[string]string{
		"HOME":             "/Users/example",
		"PRISON_INMATES":   "",
		"PRISON_PORTS":     "",
		"PRISON_PAGER":     "",
		"PRISON_DIFF_TOOL": "",
		"PRISON_SUDO":      "0",
	}))
	if err != nil {
		t.Fatalf("ReadOverrides: %v", err)
	}
	if overrides.Inmates == nil || len(*overrides.Inmates) != 0 {
		t.Errorf("inmates = %v, want a declared empty list", overrides.Inmates)
	}
	if overrides.Ports == nil || len(*overrides.Ports) != 0 {
		t.Errorf("ports = %v, want a declared empty list", overrides.Ports)
	}
	if overrides.Pager == nil || *overrides.Pager != "" {
		t.Errorf("pager = %v, want an empty string", overrides.Pager)
	}
	if overrides.DiffTool == nil || *overrides.DiffTool != "" {
		t.Errorf("diff tool = %v, want an empty string", overrides.DiffTool)
	}
	if overrides.Sudo == nil || *overrides.Sudo {
		t.Errorf("sudo = %v, want false for anything but 1", overrides.Sudo)
	}
}

// TestReadOverridesRefuses checks one bad value per validated
// variable.
func TestReadOverridesRefuses(t *testing.T) {
	cases := []struct {
		rule      string
		variables map[string]string
	}{
		{"inmate name", map[string]string{"PRISON_INMATES": "Claude"}},
		{"cage name", map[string]string{"PRISON_CAGE": "Apple Container"}},
		{"cpu count", map[string]string{"PRISON_CPUS": "99"}},
		{"cpu count is a number", map[string]string{"PRISON_CPUS": "many"}},
		{"memory size", map[string]string{"PRISON_MEMORY": "8"}},
		{"port range", map[string]string{"PRISON_PORTS": "3000 70000"}},
		{"broker port", map[string]string{"PRISON_BROKER_PORT": "0"}},
		{"port base", map[string]string{"PRISON_PORT_BASE": "no"}},
		{
			"port range order",
			map[string]string{
				"PRISON_PORT_BASE": "41000", "PRISON_PORT_LIMIT": "40000",
			},
		},
		{"pager on one line", map[string]string{"PRISON_PAGER": "less\n-R"}},
	}
	for _, testCase := range cases {
		t.Run(testCase.rule, func(t *testing.T) {
			variables := map[string]string{"HOME": "/Users/example"}
			for name, value := range testCase.variables {
				variables[name] = value
			}
			overrides, err := ReadOverrides(mapGetenv(variables))
			if err == nil {
				t.Fatalf("%v was accepted, want a refusal", testCase.variables)
			}
			if overrides != nil {
				t.Errorf("a refused environment still gave %+v", overrides)
			}
		})
	}
}

// TestReadOverridesNeedsSomewhereForState checks that missing HOME
// and PRISON_ROOT produces an error naming both.
func TestReadOverridesNeedsSomewhereForState(t *testing.T) {
	_, err := ReadOverrides(mapGetenv(map[string]string{}))
	if err == nil {
		t.Fatalf("an environment with no home was accepted")
	}
	for _, want := range []string{"HOME", "PRISON_ROOT"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message %q does not mention %q", err, want)
		}
	}
}
