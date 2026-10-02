package config

import (
	"fmt"
	"regexp"
	"strings"
)

// forbiddenValueCharacters can break values in files and command lines.
const forbiddenValueCharacters = "\t\n\r\x00"

var memorySizePattern = regexp.MustCompile(`^[1-9][0-9]*[MG]$`)

const (
	minimumCPUCount = 1
	maximumCPUCount = 32
	minimumPort     = 1
	maximumPort     = 65535
)

const DefaultCPUs = 4

const DefaultMemory = "8G"

var DefaultPorts = []int{3000, 5173, 8000, 8080}

func requireSingleLine(description, value string) error {
	if strings.ContainsAny(value, forbiddenValueCharacters) {
		return fmt.Errorf("%s must be a single line without tabs", description)
	}
	return nil
}

func requireText(description, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s must not be empty", description)
	}
	return requireSingleLine(description, value)
}

func validateCPUCount(description string, count int) error {
	if count < minimumCPUCount || count > maximumCPUCount {
		return fmt.Errorf("%s must be a number from %d to %d",
			description, minimumCPUCount, maximumCPUCount)
	}
	return nil
}

func validateMemorySize(description, size string) error {
	if !memorySizePattern.MatchString(size) {
		return fmt.Errorf(`%s must be a size such as "512M" or "8G"`, description)
	}
	return nil
}

func validatePort(description string, port int) error {
	if port < minimumPort || port > maximumPort {
		return fmt.Errorf("%s must be a number from %d to %d",
			description, minimumPort, maximumPort)
	}
	return nil
}

func validateRelativePath(description, path string) (string, error) {
	if err := requireText(description, path); err != nil {
		return "", err
	}
	if strings.HasPrefix(path, "/") {
		return "", fmt.Errorf("%s must be a relative path", description)
	}
	for _, segment := range strings.Split(path, "/") {
		if segment == ".." {
			return "", fmt.Errorf(`%s must not contain ".."`, description)
		}
	}
	return strings.Trim(path, "/"), nil
}
