package session

import (
	"prison/internal/isolator"
	"prison/internal/isolator/applecontainer"
	"prison/internal/state"
)

const IsolatorName = "apple-container"

func newIsolator(root *state.Root) (isolator.Isolator, error) {
	return applecontainer.New(), nil
}
