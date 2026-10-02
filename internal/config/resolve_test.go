package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveInmates(t *testing.T) {
	cases := []struct {
		name      string
		overrides *Overrides
		project   *Project
		global    *Global
		want      string
	}{
		{
			name:      "the variable comes first",
			overrides: &Overrides{Inmates: pointerTo([]string{"from-env"})},
			project:   projectWithInmates("from-project"),
			global:    globalWithInmates("from-global"),
			want:      "from-env",
		},
		{
			name:      "an empty variable",
			overrides: &Overrides{Inmates: pointerTo([]string{})},
			project:   projectWithInmates("from-project"),
			global:    globalWithInmates("from-global"),
			want:      "",
		},
		{
			name:      "a trusted project beats the global file",
			overrides: &Overrides{},
			project:   projectWithInmates("from-project"),
			global:    globalWithInmates("from-global"),
			want:      "from-project",
		},
		{
			name:      "an empty project list",
			overrides: &Overrides{},
			project:   projectWithInmates(),
			global:    globalWithInmates("from-global"),
			want:      "",
		},
		{
			name:      "an untrusted project falls through",
			overrides: &Overrides{},
			project:   nil,
			global:    globalWithInmates("from-global"),
			want:      "from-global",
		},
		{
			name:      "no value anywhere",
			overrides: &Overrides{},
			project:   &Project{},
			global:    &Global{},
			want:      "",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got := ResolveInmates(testCase.overrides, testCase.project,
				testCase.global)
			if strings.Join(got, ",") != testCase.want {
				t.Errorf("ResolveInmates = %v, want %q", got, testCase.want)
			}
		})
	}
}

func TestResolveBoxShape(t *testing.T) {
	project := &Project{}
	project.Box.CPUs = pointerTo(2)
	project.Box.Memory = pointerTo("2G")
	project.Box.Sudo = pointerTo(true)

	empty := &Overrides{}
	if got := ResolveCPUs(empty, nil); got != DefaultCPUs {
		t.Errorf("ResolveCPUs = %d, want %d", got, DefaultCPUs)
	}
	if got := ResolveMemory(empty, nil); got != DefaultMemory {
		t.Errorf("ResolveMemory = %q, want %q",
			got, DefaultMemory)
	}
	if ResolveSudo(empty, nil) {
		t.Errorf("ResolveSudo = true, want false")
	}

	if got := ResolveCPUs(empty, project); got != 2 {
		t.Errorf("ResolveCPUs = %d, want 2", got)
	}
	if got := ResolveMemory(empty, project); got != "2G" {
		t.Errorf("ResolveMemory = %q, want 2G", got)
	}
	if !ResolveSudo(empty, project) {
		t.Errorf("ResolveSudo = false, want true")
	}

	overrides := &Overrides{
		CPUs:   pointerTo(16),
		Memory: pointerTo("32G"),
		Sudo:   pointerTo(false),
	}
	if got := ResolveCPUs(overrides, project); got != 16 {
		t.Errorf("ResolveCPUs = %d, want 16", got)
	}
	if got := ResolveMemory(overrides, project); got != "32G" {
		t.Errorf("ResolveMemory = %q, want 32G", got)
	}
	if ResolveSudo(overrides, project) {
		t.Errorf("ResolveSudo = true, want false")
	}
}

func TestResolvePorts(t *testing.T) {
	project := &Project{}
	project.Box.Ports = pointerTo([]int{4000})

	ports, explicit := ResolvePorts(&Overrides{}, nil)
	if len(ports) != len(DefaultPorts) || explicit {
		t.Errorf("ResolvePorts = %v, %v, want the defaults, false",
			ports, explicit)
	}
	ports, explicit = ResolvePorts(&Overrides{}, project)
	if len(ports) != 1 || ports[0] != 4000 || !explicit {
		t.Errorf("ResolvePorts = %v, %v, want [4000], true", ports, explicit)
	}
	ports, explicit = ResolvePorts(&Overrides{Ports: pointerTo([]int{})}, project)
	if len(ports) != 0 || !explicit {
		t.Errorf("ResolvePorts = %v, %v, want [], true",
			ports, explicit)
	}
}

func TestResolvePortsDoesNotShareTheDefault(t *testing.T) {
	ports, _ := ResolvePorts(&Overrides{}, nil)
	ports[0] = 1
	again, _ := ResolvePorts(&Overrides{}, nil)
	if again[0] != DefaultPorts[0] {
		t.Errorf("ResolvePorts = %v, want the defaults", again)
	}
}

func TestResolvePager(t *testing.T) {
	global := &Global{}
	global.Checkpoint.Pager = pointerTo("more")
	withPager := mapGetenv(map[string]string{"PAGER": "bat"})

	named := &Overrides{Pager: pointerTo("less -S")}
	got := ResolvePager(named, global, withPager)
	if got != "less -S" {
		t.Errorf("ResolvePager = %q, want less -S", got)
	}
	cleared := &Overrides{Pager: pointerTo("")}
	if got := ResolvePager(cleared, global, withPager); got != "" {
		t.Errorf("ResolvePager = %q, want empty", got)
	}
	if got := ResolvePager(&Overrides{}, global, withPager); got != "more" {
		t.Errorf("ResolvePager = %q, want more", got)
	}
	if got := ResolvePager(&Overrides{}, &Global{}, withPager); got != "bat" {
		t.Errorf("ResolvePager = %q, want bat", got)
	}

	withLess := mapGetenv(map[string]string{})
	t.Setenv("PATH", directoryHoldingLess(t))
	got = ResolvePager(&Overrides{}, &Global{}, withLess)
	if got != fallbackPager {
		t.Errorf("ResolvePager = %q, want %q", got, fallbackPager)
	}
	t.Setenv("PATH", t.TempDir())
	if got := ResolvePager(&Overrides{}, &Global{}, withLess); got != "" {
		t.Errorf("ResolvePager = %q, want empty", got)
	}
}

func TestResolveDiffTool(t *testing.T) {
	global := &Global{}
	global.Checkpoint.DiffTool = pointerTo("delta")

	overrides := &Overrides{DiffTool: pointerTo("diff-so-fancy")}
	if got := ResolveDiffTool(overrides, global); got != "diff-so-fancy" {
		t.Errorf("ResolveDiffTool = %q, want diff-so-fancy", got)
	}
	if got := ResolveDiffTool(&Overrides{}, global); got != "delta" {
		t.Errorf("ResolveDiffTool = %q, want delta", got)
	}
	clearedTool := &Overrides{DiffTool: pointerTo("")}
	if got := ResolveDiffTool(clearedTool, global); got != "" {
		t.Errorf("ResolveDiffTool = %q, want empty", got)
	}
	if got := ResolveDiffTool(&Overrides{}, &Global{}); got != "" {
		t.Errorf("ResolveDiffTool = %q, want empty", got)
	}
}

func projectWithInmates(names ...string) *Project {
	project := &Project{}
	list := append([]string{}, names...)
	project.Prison.Inmates = &list
	return project
}

func globalWithInmates(names ...string) *Global {
	global := &Global{}
	list := append([]string{}, names...)
	global.Prison.Inmates = &list
	return global
}

func directoryHoldingLess(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	path := filepath.Join(directory, "less")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
	return directory
}
