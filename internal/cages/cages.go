package cages

import (
	"fmt"
	"strings"

	"prison/internal/cage"
)

// Options carries what a cage needs from the session. `StateDirectory` is
// empty when only the description of the cage is needed.
type Options struct {
	StateDirectory string
}

type registration struct {
	name        string
	constructor func(options Options) cage.Cage
}

func Names() []string {
	names := make([]string, 0, len(registered))
	for _, entry := range registered {
		names = append(names, entry.name)
	}
	return names
}

func Lookup(name string, options Options) (cage.Cage, error) {
	for _, entry := range registered {
		if entry.name == name {
			return entry.constructor(options), nil
		}
	}
	available := strings.Join(Names(), ", ")
	if available == "" {
		available = "none"
	}
	return nil, fmt.Errorf("unknown cage %q. Available cages: %s",
		name, available)
}
