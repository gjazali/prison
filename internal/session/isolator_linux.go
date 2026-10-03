package session

import (
	"fmt"
	"io/fs"

	"prison"
	"prison/internal/isolator"
	"prison/internal/isolator/firecracker"
	"prison/internal/state"
)

const IsolatorName = "aws-firecracker"

func newIsolator(root *state.Root) (isolator.Isolator, error) {
	kernel, err := fs.Sub(prison.KernelAssets, prison.KernelDir)
	if err != nil {
		return nil, fmt.Errorf("the kernel is missing from this build: %w", err)
	}
	return firecracker.New(root.IsolatorDir(), kernel), nil
}
