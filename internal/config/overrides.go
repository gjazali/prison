package config

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	defaultDomain     = "prison"
	defaultNetwork    = "prison"
	defaultBrokerPort = 8787
	defaultPortBase   = 40000
	defaultPortLimit  = 40100
	defaultRootFolder = ".prison"
)

// Overrides holds the `PRISON_*` environment variables. A set pointer field
// outranks both configuration files. Plain fields always hold a value.
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
			port, err := parseNumber("PRISON_PORTS", field)
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

// readCheckpointTools keeps an empty value because it turns the tool off.
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

func (overrides *Overrides) readHostPlacement(
	getenv func(string) (string, bool)) error {
	root := valueOf(getenv, "PRISON_ROOT")
	if root == "" {
		home := valueOf(getenv, "HOME")
		if home == "" {
			return fmt.Errorf("HOME is not set. Set HOME or PRISON_ROOT")
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
		return fmt.Errorf("PRISON_PORT_BASE %d is above PRISON_PORT_LIMIT %d",
			overrides.PortBase, overrides.PortLimit)
	}
	return nil
}

func valueOf(getenv func(string) (string, bool), name string) string {
	value, set := getenv(name)
	if !set {
		return ""
	}
	return strings.TrimSpace(value)
}

func numberOr(
	getenv func(string) (string, bool), name string, fallback int) (int, error) {
	text := valueOf(getenv, name)
	if text == "" {
		return fallback, nil
	}
	return parseNumber(name, text)
}

func parseNumber(description, text string) (int, error) {
	number, err := strconv.Atoi(text)
	if err != nil {
		return 0, fmt.Errorf("%q in %s is not a number", text, description)
	}
	return number, nil
}
