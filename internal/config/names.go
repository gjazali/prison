// Package config reads and validates the prison configuration files and
// environment overrides. It does not apply the values.
package config

import (
	"fmt"
	"regexp"
	"strings"
)

var namePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

var environmentNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func ValidateName(kind, name string) error {
	if !namePattern.MatchString(name) {
		return fmt.Errorf("%s name %q is not valid. Use up to 32 lowercase "+
			"letters, digits, and dashes, and start with a letter", kind, name)
	}
	return nil
}

func ValidateEnvironmentName(name string) error {
	if !environmentNamePattern.MatchString(name) {
		return fmt.Errorf("environment variable name %q is not valid", name)
	}
	return nil
}

var ReservedEnvironmentNames = []string{
	"NODE_EXTRA_CA_CERTS",
	"SSL_CERT_FILE",
	"REQUESTS_CA_BUNDLE",
	"CURL_CA_BUNDLE",
	"GIT_SSL_CAINFO",
}

const ReservedEnvironmentPrefix = "PRISON_"

func ValidateBoxEnvironmentName(name string) error {
	if err := ValidateEnvironmentName(name); err != nil {
		return err
	}
	if strings.HasPrefix(name, ReservedEnvironmentPrefix) {
		return fmt.Errorf("%s is reserved by prison", name)
	}
	for _, reserved := range ReservedEnvironmentNames {
		if name == reserved {
			return fmt.Errorf("%s is reserved by prison", name)
		}
	}
	return nil
}
