// Package image builds the chain of container images a box runs on.
// It starts with a base image and adds one layer per inmate. Tags
// carry a hash of their inputs, so unchanged sets rebuild nothing.
package image

import (
	"fmt"
	"sort"
	"strings"

	"prison/internal/plugin"
)

// DefaultFoundation is the base image used when no inmate or host
// override names a different one.
const DefaultFoundation = "debian:bookworm-slim"

// BaseRepository is the image repository for the base image tag.
const BaseRepository = "prison/base"

// BoxRepository is the image repository for inmate layer tags,
// including the final image a box runs.
const BoxRepository = "prison/box"

// ResolveFoundation picks the base image for a set of inmates. It
// takes a list of inmates and an override string. The override wins
// when set. Otherwise, if all inmates agree on one foundation, that
// is used. If none is set, it falls back to DefaultFoundation. It
// returns an error when inmates disagree.
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
	return "", fmt.Errorf(
		"these inmates need different foundation images (%s). Run them "+
			"in separate projects, or settle it with PRISON_FOUNDATION.",
		strings.Join(described, "; "))
}
