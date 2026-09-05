// Package config reads and validates prison's configuration files and
// environment overrides. It produces validated values but does not
// apply any settings.
package config

import (
	"fmt"
	"regexp"
	"strings"
)

// namePattern matches valid inmate, cage, or secret names.
var namePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

// environmentNamePattern matches valid environment variable names.
var environmentNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ValidateName checks that name is a valid identifier for kind.
// It takes a kind label and the name to check. It returns an
// error if name does not match the allowed pattern.
func ValidateName(kind, name string) error {
	if !namePattern.MatchString(name) {
		return fmt.Errorf("%q is not a valid %s name; use lowercase letters, digits, and dashes, starting with a letter, at most 32 characters", name, kind)
	}
	return nil
}

// ValidateEnvironmentName checks that name is a valid environment
// variable name. It returns an error if it is not.
func ValidateEnvironmentName(name string) error {
	if !environmentNamePattern.MatchString(name) {
		return fmt.Errorf("%q is not a valid environment variable name", name)
	}
	return nil
}

// ReservedEnvironmentNames lists the variables prison sets itself.
// Config files cannot use these names.
var ReservedEnvironmentNames = []string{
	"NODE_EXTRA_CA_CERTS",
	"SSL_CERT_FILE",
	"REQUESTS_CA_BUNDLE",
	"CURL_CA_BUNDLE",
	"GIT_SSL_CAINFO",
}

// ReservedEnvironmentPrefix is the prefix for variables prison owns.
const ReservedEnvironmentPrefix = "PRISON_"

// ValidateBoxEnvironmentName checks that name is valid for use in a
// box. It takes a variable name and returns an error if the name
// is invalid or reserved by prison.
func ValidateBoxEnvironmentName(name string) error {
	if err := ValidateEnvironmentName(name); err != nil {
		return err
	}
	if strings.HasPrefix(name, ReservedEnvironmentPrefix) {
		return fmt.Errorf("prison sets %s itself, so a configuration file cannot", name)
	}
	for _, reserved := range ReservedEnvironmentNames {
		if name == reserved {
			return fmt.Errorf("prison sets %s itself, so a configuration file cannot", name)
		}
	}
	return nil
}
