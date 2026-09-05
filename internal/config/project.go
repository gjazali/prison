package config

import (
	"fmt"
	"strconv"
	"strings"

	"prison/internal/policy"
)

// projectSchema lists the tables and keys prison.toml accepts.
// The [environment] table accepts any key.
var projectSchema = tableSchema{
	"prison":      {"inmates"},
	"box":         {"cpus", "memory", "ports", "sudo", "shadow"},
	"setup":       {"commands"},
	"checkpoint":  {"ignore"},
	"egress":      {"hosts"},
	"secrets":     {"require"},
	"environment": nil,
}

// secretNameTheBrokerUses is the secret name reserved by the broker.
// Projects cannot request this name.
const secretNameTheBrokerUses = "prison"

// ProjectPrison is the [prison] table of a project file. A nil
// list means absent, so the global file decides. An empty list
// means "no inmates."
type ProjectPrison struct {
	Inmates *[]string `toml:"inmates"`
}

// ProjectBox is the [box] table. It describes the box's resources.
// A nil field means absent. An empty ports list means "publish
// nothing."
type ProjectBox struct {
	CPUs   *int     `toml:"cpus"`
	Memory *string  `toml:"memory"`
	Ports  *[]int   `toml:"ports"`
	Sudo   *bool    `toml:"sudo"`
	Shadow []string `toml:"shadow"`
}

// ProjectSetup is the [setup] table. It lists commands to run in
// the box after creation.
type ProjectSetup struct {
	Commands []string `toml:"commands"`
}

// ProjectCheckpoint is the [checkpoint] table. It lists file
// patterns to exclude from checkpoints.
type ProjectCheckpoint struct {
	Ignore []string `toml:"ignore"`
}

// ProjectEgress is the [egress] table. It lists host patterns to
// add to the egress allowlist.
type ProjectEgress struct {
	Hosts []string `toml:"hosts"`
}

// ProjectSecrets is the [secrets] table. It declares which secrets
// the project needs. It does not grant access to them.
type ProjectSecrets struct {
	Require []string `toml:"require"`
}

// Project is a project's prison.toml. It describes what the box
// needs and takes effect only after trust. The zero value means
// no file exists.
type Project struct {
	Prison      ProjectPrison     `toml:"prison"`
	Box         ProjectBox        `toml:"box"`
	Setup       ProjectSetup      `toml:"setup"`
	Checkpoint  ProjectCheckpoint `toml:"checkpoint"`
	Egress      ProjectEgress     `toml:"egress"`
	Secrets     ProjectSecrets    `toml:"secrets"`
	Environment map[string]string `toml:"-"`
}

// projectDocument is the raw decoded form of a project file.
// It holds a Project plus the [environment] table before values
// are converted to strings.
type projectDocument struct {
	Project
	Environment map[string]any `toml:"environment"`
}

// LoadProject reads and validates the project file at path. It takes
// a file path and returns a *Project and an error. A missing file
// gives an empty Project, not an error. Unknown tables or keys
// cause an error.
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

// validate checks every value in a decoded project file. It returns
// the first problem it finds. It also normalizes shadow paths.
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
			return fmt.Errorf("in egress.hosts, %w", err)
		}
	}
	return project.validateSecrets()
}

// validateBox checks the [box] table and normalizes shadow paths
// by stripping leading and trailing slashes.
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
			return fmt.Errorf(
				"box.shadow cannot cover .git, which prison already mounts over")
		}
		box.Shadow[index] = cleaned
	}
	return nil
}

// validateSecrets checks each name in [secrets] require. The
// broker's reserved name is not allowed.
func (project *Project) validateSecrets() error {
	for _, name := range project.Secrets.Require {
		if err := ValidateName("secret", name); err != nil {
			return err
		}
		if name == secretNameTheBrokerUses {
			return fmt.Errorf(
				"%q is not a secret a project can ask for; the broker uses that "+
					"name for its own route", name)
		}
	}
	return nil
}

// readEnvironment converts the [environment] table into string values
// for the box. It takes a parsed table and returns a map of strings.
// Problems are reported in alphabetical order by name.
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

// environmentText converts one [environment] value to a string.
// It accepts strings, numbers, and booleans. Other types are
// refused.
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
		"environment.%s must be text, a number, or true or false", name)
}
