// Package image builds the chain of container images that a box runs on.
// Each tag holds a hash of its inputs, so an unchanged chain is not
// rebuilt.
package image

import (
	"fmt"
	"sort"
	"strings"

	"prison/internal/plugin"
)

const DefaultFoundation = "debian:bookworm-slim"

const BaseRepository = "prison/base"

const BoxRepository = "prison/box"

func ResolveFoundation(
	inmates []*plugin.Inmate, override string,
) (string, error) {
	if override != "" {
		return override, nil
	}
	wanting := map[string][]string{}
	for _, inmate := range inmates {
		if inmate.Image.Foundation == "" {
			continue
		}
		wanting[inmate.Image.Foundation] = append(
			wanting[inmate.Image.Foundation], inmate.Name)
	}
	switch len(wanting) {
	case 0:
		return DefaultFoundation, nil
	case 1:
		for foundation := range wanting {
			return foundation, nil
		}
	}
	foundations := make([]string, 0, len(wanting))
	for foundation := range wanting {
		foundations = append(foundations, foundation)
	}
	sort.Strings(foundations)
	described := make([]string, 0, len(foundations))
	for _, foundation := range foundations {
		described = append(described, fmt.Sprintf("%s for %s",
			foundation, strings.Join(wanting[foundation], ", ")))
	}
	return "", fmt.Errorf("the inmates need different foundation images "+
		"(%s). Run them in separate projects or set PRISON_FOUNDATION",
		strings.Join(described, " and "))
}
