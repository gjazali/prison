package config

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
)

// Defaults for host-side variables when unset.
const (
	defaultDomain     = "prison"
	defaultNetwork    = "prison"
	defaultBrokerPort = 8787
	defaultPortBase   = 40000
	defaultPortLimit  = 40100
	defaultRootFolder = ".prison"
)

// Overrides holds every PRISON_* environment variable. A pointer
// field is nil when unset. When set, it outranks both config files.
// Plain fields always have a usable value, using the default when
// the variable is unset.
type Overrides struct {
	Inmates    *[]string
	Cage       *string
	Pager      *string
	DiffTool   *string
	CPUs       *int
	Memory     *string
	Sudo       *bool
	Ports      *[]int
	Image      string
	Foundation string
	Root       string
	Domain     string
	Network    string
	BrokerPort int
	PortBase   int
	PortLimit  int
}

// ReadOverrides reads all PRISON_* variables through getenv.
// It takes a lookup function and returns parsed overrides or an
// error if any value is invalid.
func ReadOverrides(getenv func(string) (string, bool)) (*Overrides, error) {
	overrides := &Overrides{
		Image:      valueOf(getenv, "PRISON_IMAGE"),
		Foundation: valueOf(getenv, "PRISON_FOUNDATION"),
		Domain:     defaultDomain,
		Network:    defaultNetwork,
	}
	if err := overrides.readInmates(getenv); err != nil {
		return nil, err
	}
	if err := overrides.readBoxShape(getenv); err != nil {
		return nil, err
	}
	if err := overrides.readCheckpointTools(getenv); err != nil {
		return nil, err
	}
	if err := overrides.readHostPlacement(getenv); err != nil {
		return nil, err
	}
	return overrides, nil
}

// readInmates reads PRISON_INMATES and PRISON_CAGE. The inmate
// list is space-separated. An empty value means "no inmates,"
// which is different from being unset.
func (overrides *Overrides) readInmates(
	getenv func(string) (string, bool)) error {
	if text, set := getenv("PRISON_INMATES"); set {
		names := []string{}
		for _, name := range strings.Fields(text) {
			if err := ValidateName("inmate", name); err != nil {
				return fmt.Errorf("PRISON_INMATES: %w", err)
			}
			names = append(names, name)
		}
		overrides.Inmates = &names
	}
	if cage := valueOf(getenv, "PRISON_CAGE"); cage != "" {
		if err := ValidateName("cage", cage); err != nil {
			return fmt.Errorf("PRISON_CAGE: %w", err)
		}
		overrides.Cage = &cage
	}
	return nil
}

// readBoxShape reads PRISON_CPUS, PRISON_MEMORY, PRISON_SUDO, and
// PRISON_PORTS. Sudo is on only when the variable is exactly "1".
func (overrides *Overrides) readBoxShape(
	getenv func(string) (string, bool)) error {
	if text := valueOf(getenv, "PRISON_CPUS"); text != "" {
		count, err := parseNumber("PRISON_CPUS", text)
		if err != nil {
			return err
		}
		if err := validateCPUCount("PRISON_CPUS", count); err != nil {
			return err
		}
		overrides.CPUs = &count
	}
	if memory := valueOf(getenv, "PRISON_MEMORY"); memory != "" {
		if err := validateMemorySize("PRISON_MEMORY", memory); err != nil {
			return err
		}
		overrides.Memory = &memory
	}
	if text, set := getenv("PRISON_SUDO"); set {
		wanted := text == "1"
		overrides.Sudo = &wanted
	}
	if text, set := getenv("PRISON_PORTS"); set {
		ports := []int{}
		for _, field := range strings.Fields(text) {
			port, err := parseNumber("every port in PRISON_PORTS", field)
			if err != nil {
				return err
			}
			if err := validatePort("every port in PRISON_PORTS", port); err != nil {
				return err
			}
			ports = append(ports, port)
		}
		overrides.Ports = &ports
	}
	return nil
}

// readCheckpointTools reads PRISON_PAGER and PRISON_DIFF_TOOL.
// An empty value is kept as-is, meaning "use nothing."
func (overrides *Overrides) readCheckpointTools(
	getenv func(string) (string, bool)) error {
	if text, set := getenv("PRISON_PAGER"); set {
		if err := requireSingleLine("PRISON_PAGER", text); err != nil {
			return err
		}
		overrides.Pager = &text
	}
	if text, set := getenv("PRISON_DIFF_TOOL"); set {
		if err := requireSingleLine("PRISON_DIFF_TOOL", text); err != nil {
			return err
		}
		overrides.DiffTool = &text
	}
	return nil
}

// readHostPlacement reads the variables that control where prison
// puts files on the host. The root falls back to .prison under HOME.
// If neither HOME nor PRISON_ROOT is set, it returns an error.
func (overrides *Overrides) readHostPlacement(
	getenv func(string) (string, bool)) error {
	root := valueOf(getenv, "PRISON_ROOT")
	if root == "" {
		home := valueOf(getenv, "HOME")
		if home == "" {
			return fmt.Errorf(
				"prison cannot tell where your home directory is; set HOME, or " +
					"PRISON_ROOT to where prison should keep its state")
		}
		root = filepath.Join(home, defaultRootFolder)
	}
	if err := requireText("PRISON_ROOT", root); err != nil {
		return err
	}
	overrides.Root = root

	if domain := valueOf(getenv, "PRISON_DOMAIN"); domain != "" {
		if err := requireText("PRISON_DOMAIN", domain); err != nil {
			return err
		}
		overrides.Domain = domain
	}
	if network := valueOf(getenv, "PRISON_NETWORK"); network != "" {
		if err := requireText("PRISON_NETWORK", network); err != nil {
			return err
		}
		overrides.Network = network
	}

	numbers := []struct {
		name     string
		fallback int
		target   *int
	}{
		{"PRISON_BROKER_PORT", defaultBrokerPort, &overrides.BrokerPort},
		{"PRISON_PORT_BASE", defaultPortBase, &overrides.PortBase},
		{"PRISON_PORT_LIMIT", defaultPortLimit, &overrides.PortLimit},
	}
	for _, number := range numbers {
		value, err := numberOr(getenv, number.name, number.fallback)
		if err != nil {
			return err
		}
		if err := validatePort(number.name, value); err != nil {
			return err
		}
		*number.target = value
	}
	if overrides.PortBase > overrides.PortLimit {
		return fmt.Errorf(
			"PRISON_PORT_BASE is %d and PRISON_PORT_LIMIT is %d, so there is no "+
				"range to publish from; the base must not be above the limit",
			overrides.PortBase, overrides.PortLimit)
	}
	return nil
}

// valueOf returns a trimmed variable value, or "" if unset.
func valueOf(getenv func(string) (string, bool), name string) string {
	value, set := getenv(name)
	if !set {
		return ""
	}
	return strings.TrimSpace(value)
}

// numberOr returns a numeric variable's value, or fallback if unset.
// It returns an error if the value is not a whole number.
func numberOr(
	getenv func(string) (string, bool), name string, fallback int) (int, error) {
	text := valueOf(getenv, name)
	if text == "" {
		return fallback, nil
	}
	return parseNumber(name, text)
}

// parseNumber parses text as an integer. It returns an error naming
// description if the text is not a number.
func parseNumber(description, text string) (int, error) {
	number, err := strconv.Atoi(text)
	if err != nil {
		return 0, fmt.Errorf("%s must be a number, not %q", description, text)
	}
	return number, nil
}
