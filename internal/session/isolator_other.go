//go:build !darwin && !linux

package session

import (
	"fmt"
	"runtime"

	"prison/internal/isolator"
	"prison/internal/state"
)

const IsolatorName = ""

func newIsolator(root *state.Root) (isolator.Isolator, error) {
	return nil, fmt.Errorf("prison does not run on %s", runtime.GOOS)
}
