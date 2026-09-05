// Package cages is the registry of compiled-in cages. The set is fixed
// at build time.
package cages

import (
	"fmt"
	"strings"

	"prison/internal/cage"
	"prison/internal/cage/applecontainer"
)

// DefaultName is the cage used when no cage is configured.
const DefaultName = "apple-container"

// Names returns all available cage names.
func Names() []string {
	return []string{DefaultName}
}

// Lookup returns the cage with the given name. Returns an error for
// unknown names.
func Lookup(name string) (cage.Cage, error) {
	switch name {
	case DefaultName:
		return applecontainer.New(), nil
	default:
		return nil, fmt.Errorf(
			"unknown cage %q; this build has %s",
			name, strings.Join(Names(), ", "))
	}
}
