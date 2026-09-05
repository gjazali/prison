package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestResolveInmates checks precedence: variable, then trusted
// project, then global, then nothing.
func TestResolveInmates(t *testing.T) {
	cases := []struct {
		name      string
		overrides *Overrides
		project   *Project
		global    *Global
		want      string
	}{
		{
			name:      "the variable wins",
			overrides: &Overrides{Inmates: pointerTo([]string{"from-env"})},
			project:   projectWithInmates("from-project"),
			global:    globalWithInmates("from-global"),
			want:      "from-env",
		},
		{
			name:      "an empty variable holds nothing",
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
			name:      "an empty project list holds nothing",
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
			name:      "nothing anywhere holds nothing",
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

// TestResolveBoxShape checks CPUs, memory, and sudo precedence:
// variable, then project, then default.
func TestResolveBoxShape(t *testing.T) {
	project := &Project{}
	project.Box.CPUs = pointerTo(2)
	project.Box.Memory = pointerTo("2G")
	project.Box.Sudo = pointerTo(true)

	empty := &Overrides{}
	if got := ResolveCPUs(empty, nil); got != DefaultCPUs {
		t.Errorf("ResolveCPUs with nothing set = %d, want %d", got, DefaultCPUs)
	}
	if got := ResolveMemory(empty, nil); got != DefaultMemory {
		t.Errorf("ResolveMemory with nothing set = %q, want %q",
			got, DefaultMemory)
	}
	if ResolveSudo(empty, nil) {
		t.Errorf("ResolveSudo with nothing set = true, want false")
	}

	if got := ResolveCPUs(empty, project); got != 2 {
		t.Errorf("ResolveCPUs from the project = %d, want 2", got)
	}
	if got := ResolveMemory(empty, project); got != "2G" {
		t.Errorf("ResolveMemory from the project = %q, want 2G", got)
	}
	if !ResolveSudo(empty, project) {
		t.Errorf("ResolveSudo from the project = false, want true")
	}

	overrides := &Overrides{
		CPUs:   pointerTo(16),
		Memory: pointerTo("32G"),
		Sudo:   pointerTo(false),
	}
	if got := ResolveCPUs(overrides, project); got != 16 {
		t.Errorf("ResolveCPUs from the environment = %d, want 16", got)
	}
	if got := ResolveMemory(overrides, project); got != "32G" {
		t.Errorf("ResolveMemory from the environment = %q, want 32G", got)
	}
	if ResolveSudo(overrides, project) {
		t.Errorf("ResolveSudo from the environment = true, want false")
	}
}

// TestResolvePorts checks the precedence ladder and the explicit
// flag.
func TestResolvePorts(t *testing.T) {
	project := &Project{}
	project.Box.Ports = pointerTo([]int{4000})

	ports, explicit := ResolvePorts(&Overrides{}, nil)
	if len(ports) != len(DefaultPorts) || explicit {
		t.Errorf("with nothing set = %v, %v, want the default block",
			ports, explicit)
	}
	ports, explicit = ResolvePorts(&Overrides{}, project)
	if len(ports) != 1 || ports[0] != 4000 || !explicit {
		t.Errorf("from the project = %v, %v, want 4000 named", ports, explicit)
	}
	ports, explicit = ResolvePorts(&Overrides{Ports: pointerTo([]int{})}, project)
	if len(ports) != 0 || !explicit {
		t.Errorf("from an empty variable = %v, %v, want nothing named",
			ports, explicit)
	}
}

// TestResolvePortsDoesNotShareTheDefault checks that mutating the
// returned slice does not affect future calls.
func TestResolvePortsDoesNotShareTheDefault(t *testing.T) {
	ports, _ := ResolvePorts(&Overrides{}, nil)
	ports[0] = 1
	again, _ := ResolvePorts(&Overrides{}, nil)
	if again[0] != DefaultPorts[0] {
		t.Errorf("the default block was changed to %v", again)
	}
}

// TestResolvePager checks the pager ladder: variable, global file,
// PAGER, less on PATH, and the empty string that stops the search.
func TestResolvePager(t *testing.T) {
	global := &Global{}
	global.Checkpoint.Pager = pointerTo("more")
	withPager := mapGetenv(map[string]string{"PAGER": "bat"})

	named := &Overrides{Pager: pointerTo("less -S")}
	got := ResolvePager(named, global, withPager)
	if got != "less -S" {
		t.Errorf("the variable did not win: %q", got)
	}
	cleared := &Overrides{Pager: pointerTo("")}
	if got := ResolvePager(cleared, global, withPager); got != "" {
		t.Errorf("an empty variable did not stop the search: %q", got)
	}
	if got := ResolvePager(&Overrides{}, global, withPager); got != "more" {
		t.Errorf("the file did not win over PAGER: %q", got)
	}
	if got := ResolvePager(&Overrides{}, &Global{}, withPager); got != "bat" {
		t.Errorf("PAGER was not used: %q", got)
	}

	withLess := mapGetenv(map[string]string{})
	t.Setenv("PATH", directoryHoldingLess(t))
	got = ResolvePager(&Overrides{}, &Global{}, withLess)
	if got != fallbackPager {
		t.Errorf("less on PATH gave %q, want %q", got, fallbackPager)
	}
	t.Setenv("PATH", t.TempDir())
	if got := ResolvePager(&Overrides{}, &Global{}, withLess); got != "" {
		t.Errorf("no less on PATH gave %q, want no paging", got)
	}
}

// TestResolveDiffTool checks the variable and global file rungs, and
// the empty string that selects built-in rendering.
func TestResolveDiffTool(t *testing.T) {
	global := &Global{}
	global.Checkpoint.DiffTool = pointerTo("delta")

	overrides := &Overrides{DiffTool: pointerTo("diff-so-fancy")}
	if got := ResolveDiffTool(overrides, global); got != "diff-so-fancy" {
		t.Errorf("the variable did not win: %q", got)
	}
	if got := ResolveDiffTool(&Overrides{}, global); got != "delta" {
		t.Errorf("the file was not used: %q", got)
	}
	clearedTool := &Overrides{DiffTool: pointerTo("")}
	if got := ResolveDiffTool(clearedTool, global); got != "" {
		t.Errorf("an empty variable did not stop the search: %q", got)
	}
	if got := ResolveDiffTool(&Overrides{}, &Global{}); got != "" {
		t.Errorf("nothing set gave %q, want prison's own rendering", got)
	}
}

// projectWithInmates returns a Project declaring the given inmates.
func projectWithInmates(names ...string) *Project {
	project := &Project{}
	list := append([]string{}, names...)
	project.Prison.Inmates = &list
	return project
}

// globalWithInmates returns a Global declaring the given inmates.
func globalWithInmates(names ...string) *Global {
	global := &Global{}
	list := append([]string{}, names...)
	global.Prison.Inmates = &list
	return global
}

// directoryHoldingLess returns a temporary directory containing a
// `less` executable.
func directoryHoldingLess(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	path := filepath.Join(directory, "less")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
	return directory
}
