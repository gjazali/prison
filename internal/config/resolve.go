package config

import (
	"os/exec"
	"strings"
)

const fallbackPager = "less -R"

// ResolveInmates takes a nil project when the project is untrusted.
func ResolveInmates(
	overrides *Overrides, project *Project, global *Global) []string {
	if overrides != nil && overrides.Inmates != nil {
		return copyStrings(*overrides.Inmates)
	}
	if project != nil && project.Prison.Inmates != nil {
		return copyStrings(*project.Prison.Inmates)
	}
	if global != nil && global.Prison.Inmates != nil {
		return copyStrings(*global.Prison.Inmates)
	}
	return []string{}
}

func ResolveCPUs(overrides *Overrides, project *Project) int {
	if overrides != nil && overrides.CPUs != nil {
		return *overrides.CPUs
	}
	if project != nil && project.Box.CPUs != nil {
		return *project.Box.CPUs
	}
	return DefaultCPUs
}

func ResolveMemory(overrides *Overrides, project *Project) string {
	if overrides != nil && overrides.Memory != nil {
		return *overrides.Memory
	}
	if project != nil && project.Box.Memory != nil {
		return *project.Box.Memory
	}
	return DefaultMemory
}

func ResolveSudo(overrides *Overrides, project *Project) bool {
	if overrides != nil && overrides.Sudo != nil {
		return *overrides.Sudo
	}
	if project != nil && project.Box.Sudo != nil {
		return *project.Box.Sudo
	}
	return false
}

func ResolvePorts(overrides *Overrides, project *Project) ([]int, bool) {
	if overrides != nil && overrides.Ports != nil {
		return copyPorts(*overrides.Ports), true
	}
	if project != nil && project.Box.Ports != nil {
		return copyPorts(*project.Box.Ports), true
	}
	return copyPorts(DefaultPorts), false
}

// ResolvePager returns an empty string to turn paging off.
func ResolvePager(
	overrides *Overrides, global *Global,
	getenv func(string) (string, bool)) string {
	if overrides != nil && overrides.Pager != nil {
		return *overrides.Pager
	}
	if global != nil && global.Checkpoint.Pager != nil {
		return *global.Checkpoint.Pager
	}
	if getenv != nil {
		if pager, set := getenv("PAGER"); set {
			return strings.TrimSpace(pager)
		}
	}
	if _, err := exec.LookPath("less"); err == nil {
		return fallbackPager
	}
	return ""
}

// ResolveDiffTool returns an empty string when prison renders the diff
// itself.
func ResolveDiffTool(overrides *Overrides, global *Global) string {
	if overrides != nil && overrides.DiffTool != nil {
		return *overrides.DiffTool
	}
	if global != nil && global.Checkpoint.DiffTool != nil {
		return *global.Checkpoint.DiffTool
	}
	return ""
}

func copyStrings(values []string) []string {
	copied := make([]string, len(values))
	copy(copied, values)
	return copied
}

func copyPorts(values []int) []int {
	copied := make([]int, len(values))
	copy(copied, values)
	return copied
}
