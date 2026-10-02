package config

import (
	"fmt"
	"strconv"
	"strings"

	"prison/internal/policy"
)

var projectSchema = tableSchema{
	"prison":      {"inmates"},
	"box":         {"cpus", "memory", "ports", "sudo", "shadow"},
	"setup":       {"commands"},
	"checkpoint":  {"ignore"},
	"egress":      {"hosts"},
	"secrets":     {"require"},
	"environment": nil,
}

const secretNameTheBrokerUses = "prison"

// ProjectPrison holds the `[prison]` table. A nil list falls back to the
// global file. An empty list means no inmates.
type ProjectPrison struct {
	Inmates *[]string `toml:"inmates"`
}

// ProjectBox holds the `[box]` table. An empty ports list publishes no ports.
type ProjectBox struct {
	CPUs   *int     `toml:"cpus"`
	Memory *string  `toml:"memory"`
	Ports  *[]int   `toml:"ports"`
	Sudo   *bool    `toml:"sudo"`
	Shadow []string `toml:"shadow"`
}

type ProjectSetup struct {
	Commands []string `toml:"commands"`
}

type ProjectCheckpoint struct {
	Ignore []string `toml:"ignore"`
}

type ProjectEgress struct {
	Hosts []string `toml:"hosts"`
}

// ProjectSecrets declares the secrets that the project needs. It does not
// grant access to them.
type ProjectSecrets struct {
	Require []string `toml:"require"`
}

// Project is a project's `prison.toml`. It takes effect only after the user
// trusts it.
type Project struct {
	Prison      ProjectPrison     `toml:"prison"`
	Box         ProjectBox        `toml:"box"`
	Setup       ProjectSetup      `toml:"setup"`
	Checkpoint  ProjectCheckpoint `toml:"checkpoint"`
	Egress      ProjectEgress     `toml:"egress"`
	Secrets     ProjectSecrets    `toml:"secrets"`
	Environment map[string]string `toml:"-"`
}

// projectDocument holds the raw `[environment]` values before they become
// strings.
type projectDocument struct {
	Project
	Environment map[string]any `toml:"environment"`
}

// LoadProject returns an empty Project when the file is missing.
func LoadProject(path string) (*Project, error) {
	document := &projectDocument{}
	found, err := decodeFile(path, document, projectSchema)
	if err != nil {
		return nil, err
	}
	project := &document.Project
	project.Environment = nil
	if !found {
		return project, nil
	}
	environment, err := readEnvironment(document.Environment)
	if err != nil {
		return nil, unusable(path, err)
	}
	project.Environment = environment
	if err := project.validate(); err != nil {
		return nil, unusable(path, err)
	}
	return project, nil
}

// validate also normalizes the shadow paths in place.
func (project *Project) validate() error {
	if project.Prison.Inmates != nil {
		for _, inmate := range *project.Prison.Inmates {
			if err := ValidateName("inmate", inmate); err != nil {
				return err
			}
		}
	}
	if err := project.validateBox(); err != nil {
		return err
	}
	for _, command := range project.Setup.Commands {
		if err := requireText("every setup command", command); err != nil {
			return err
		}
	}
	for _, pattern := range project.Checkpoint.Ignore {
		_, err := validateRelativePath("every entry in checkpoint.ignore", pattern)
		if err != nil {
			return err
		}
	}
	for _, host := range project.Egress.Hosts {
		if err := requireText("every entry in egress.hosts", host); err != nil {
			return err
		}
		if _, err := policy.ParsePattern(host); err != nil {
			return fmt.Errorf("egress.hosts: %w", err)
		}
	}
	return project.validateSecrets()
}

func (project *Project) validateBox() error {
	box := &project.Box
	if box.CPUs != nil {
		if err := validateCPUCount("box.cpus", *box.CPUs); err != nil {
			return err
		}
	}
	if box.Memory != nil {
		if err := validateMemorySize("box.memory", *box.Memory); err != nil {
			return err
		}
	}
	if box.Ports != nil {
		for _, port := range *box.Ports {
			if err := validatePort("every entry in box.ports", port); err != nil {
				return err
			}
		}
	}
	for index, path := range box.Shadow {
		cleaned, err := validateRelativePath("every entry in box.shadow", path)
		if err != nil {
			return err
		}
		if cleaned == ".git" || strings.HasPrefix(cleaned, ".git/") {
			return fmt.Errorf("box.shadow cannot contain .git")
		}
		box.Shadow[index] = cleaned
	}
	return nil
}

func (project *Project) validateSecrets() error {
	for _, name := range project.Secrets.Require {
		if err := ValidateName("secret", name); err != nil {
			return err
		}
		if name == secretNameTheBrokerUses {
			return fmt.Errorf("secret name %q is reserved for the broker", name)
		}
	}
	return nil
}

func readEnvironment(table map[string]any) (map[string]string, error) {
	environment := make(map[string]string, len(table))
	for _, name := range sortedKeys(table) {
		if err := ValidateBoxEnvironmentName(name); err != nil {
			return nil, err
		}
		value, err := environmentText(name, table[name])
		if err != nil {
			return nil, err
		}
		description := fmt.Sprintf("environment.%s", name)
		if err := requireSingleLine(description, value); err != nil {
			return nil, err
		}
		environment[name] = value
	}
	return environment, nil
}

func environmentText(name string, value any) (string, error) {
	switch typed := value.(type) {
	case string:
		return typed, nil
	case int64:
		return strconv.FormatInt(typed, 10), nil
	case int:
		return strconv.Itoa(typed), nil
	case bool:
		return strconv.FormatBool(typed), nil
	}
	return "", fmt.Errorf(
		"environment.%s must be a string, number, or boolean", name)
}
