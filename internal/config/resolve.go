package config

import (
	"os/exec"
	"strings"
)

// fallbackPager is the pager used when no other pager is set and
// `less` is available on the host.
const fallbackPager = "less -R"

// ResolveInmates returns the inmates for a box. It checks the
// environment, then the project file, then the global file.
// A nil project means the file is untrusted and gets skipped.
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

// ResolveCPUs returns the CPU count for the box. It checks
// PRISON_CPUS, then the project file, then DefaultCPUs.
func ResolveCPUs(overrides *Overrides, project *Project) int {
	if overrides != nil && overrides.CPUs != nil {
		return *overrides.CPUs
	}
	if project != nil && project.Box.CPUs != nil {
		return *project.Box.CPUs
	}
	return DefaultCPUs
}

// ResolveMemory returns the memory size for the box. It checks
// PRISON_MEMORY, then the project file, then DefaultMemory.
func ResolveMemory(overrides *Overrides, project *Project) string {
	if overrides != nil && overrides.Memory != nil {
		return *overrides.Memory
	}
	if project != nil && project.Box.Memory != nil {
		return *project.Box.Memory
	}
	return DefaultMemory
}

// ResolveSudo reports whether the box gives root access. It checks
// PRISON_SUDO, then the project file. Defaults to false.
func ResolveSudo(overrides *Overrides, project *Project) bool {
	if overrides != nil && overrides.Sudo != nil {
		return *overrides.Sudo
	}
	if project != nil && project.Box.Sudo != nil {
		return *project.Box.Sudo
	}
	return false
}

// ResolvePorts returns the guest ports published to the host and
// whether they were explicitly set. It checks PRISON_PORTS, then
// the project file, then DefaultPorts.
func ResolvePorts(overrides *Overrides, project *Project) ([]int, bool) {
	if overrides != nil && overrides.Ports != nil {
		return copyPorts(*overrides.Ports), true
	}
	if project != nil && project.Box.Ports != nil {
		return copyPorts(*project.Box.Ports), true
	}
	return copyPorts(DefaultPorts), false
}

// ResolvePager returns the pager command for diffs. It checks
// PRISON_PAGER, then the config file, then the PAGER variable,
// then falls back to `less`. An empty string means no paging.
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

// ResolveDiffTool returns the external diff renderer. It checks
// PRISON_DIFF_TOOL, then the config file. An empty string means
// prison renders the diff itself.
func ResolveDiffTool(overrides *Overrides, global *Global) string {
	if overrides != nil && overrides.DiffTool != nil {
		return *overrides.DiffTool
	}
	if global != nil && global.Checkpoint.DiffTool != nil {
		return *global.Checkpoint.DiffTool
	}
	return ""
}

// copyStrings returns a copy of a string slice.
func copyStrings(values []string) []string {
	copied := make([]string, len(values))
	copy(copied, values)
	return copied
}

// copyPorts returns a copy of an int slice.
func copyPorts(values []int) []int {
	copied := make([]int, len(values))
	copy(copied, values)
	return copied
}
