package config

import (
	"fmt"
	"regexp"
	"strings"
)

// forbiddenValueCharacters lists characters no config value may
// hold. They would break values in files or command lines.
const forbiddenValueCharacters = "\t\n\r\x00"

// memorySizePattern matches valid memory sizes like "512M" or "8G".
var memorySizePattern = regexp.MustCompile(`^[1-9][0-9]*[MG]$`)

// Bounds for CPU count and port number validation.
const (
	minimumCPUCount = 1
	maximumCPUCount = 32
	minimumPort     = 1
	maximumPort     = 65535
)

// DefaultCPUs is the CPU count when nothing is configured.
const DefaultCPUs = 4

// DefaultMemory is the memory size when nothing is configured.
const DefaultMemory = "8G"

// DefaultPorts lists the guest ports published when nothing is
// configured.
var DefaultPorts = []int{3000, 5173, 8000, 8080}

// requireSingleLine returns an error if value has tabs, newlines, or
// null bytes. It takes a description for error messages and the
// value to check. An empty string passes.
func requireSingleLine(description, value string) error {
	if strings.ContainsAny(value, forbiddenValueCharacters) {
		return fmt.Errorf("%s must be a single line without tabs", description)
	}
	return nil
}

// requireText checks that value is a non-empty single line.
func requireText(description, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s must not be empty", description)
	}
	return requireSingleLine(description, value)
}

// validateCPUCount checks that count is within the allowed range.
// It takes a description for error messages and the count to check.
func validateCPUCount(description string, count int) error {
	if count < minimumCPUCount || count > maximumCPUCount {
		return fmt.Errorf("%s must be a number from %d to %d",
			description, minimumCPUCount, maximumCPUCount)
	}
	return nil
}

// validateMemorySize checks that size matches the expected format.
// It takes a description for error messages and the size string.
func validateMemorySize(description, size string) error {
	if !memorySizePattern.MatchString(size) {
		return fmt.Errorf(
			`%s must be digits followed by M or G, such as "8G"`, description)
	}
	return nil
}

// validatePort checks that port is between 1 and 65535. It takes
// a description for error messages and the port number.
func validatePort(description string, port int) error {
	if port < minimumPort || port > maximumPort {
		return fmt.Errorf("%s must be a number from %d to %d",
			description, minimumPort, maximumPort)
	}
	return nil
}

// validateRelativePath checks that path is relative and stays
// inside the project. It returns the path with leading and
// trailing slashes removed, or an error.
func validateRelativePath(description, path string) (string, error) {
	if err := requireText(description, path); err != nil {
		return "", err
	}
	if strings.HasPrefix(path, "/") {
		return "", fmt.Errorf("%s must be relative to the project", description)
	}
	for _, segment := range strings.Split(path, "/") {
		if segment == ".." {
			return "", fmt.Errorf(
				"%s must not climb out of the project", description)
		}
	}
	return strings.Trim(path, "/"), nil
}
