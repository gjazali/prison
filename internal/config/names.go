// Package config reads and validates the prison configuration files and
// environment overrides. It does not apply the values.
package config

import (
	"fmt"
	"regexp"
	"strings"
)

var namePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

var inmateVersionPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,63}$`)

var environmentNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func ValidateName(kind, name string) error {
	if !namePattern.MatchString(name) {
		return fmt.Errorf("%s name %q is not valid. Use up to 32 lowercase "+
			"letters, digits, and dashes, and start with a letter", kind, name)
	}
	return nil
}

// SplitInmateSpec splits an optional `:version` pin from an inmate spec.
func SplitInmateSpec(spec string) (name, version string) {
	name, version, _ = strings.Cut(spec, ":")
	return name, version
}

func ValidateInmateVersion(version string) error {
	if !inmateVersionPattern.MatchString(version) || strings.Contains(version, "..") {
		return fmt.Errorf("inmate version %q is not valid. Use letters, "+
			"digits, dots, underscores, pluses, and dashes", version)
	}
	return nil
}

func ValidateInmateSpec(spec string) error {
	name, version, hasVersion := strings.Cut(spec, ":")
	if err := ValidateName("inmate", name); err != nil {
		return err
	}
	if !hasVersion {
		return nil
	}
	if err := ValidateInmateVersion(version); err != nil {
		return fmt.Errorf("%w in %q", err, spec)
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
