package applecontainer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"prison/internal/cage"
)

// ImageExists returns true if the backend holds the given image tag.
func (driver *Driver) ImageExists(
	ctx context.Context, tag string,
) (bool, error) {
	return driver.runQuietly(
		ctx, containerBinary, "image", "inspect", tag)
}

// buildArguments builds the argv for `container build` from a spec.
func buildArguments(spec cage.BuildSpec) []string {
	arguments := []string{"build", "--tag", spec.Tag}
	if spec.Dockerfile != "" {
		arguments = append(arguments, "--file",
			filepath.Join(spec.Context, spec.Dockerfile))
	}
	names := make([]string, 0, len(spec.Args))
	for name := range spec.Args {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		arguments = append(arguments,
			"--build-arg", name+"="+spec.Args[name])
	}
	return append(arguments, spec.Context)
}

// Build builds an image from a context directory. Output goes to the
// spec's writer, or to stderr if none is set.
func (driver *Driver) Build(
	ctx context.Context, spec cage.BuildSpec,
) error {
	output := spec.Output
	if output == nil {
		output = os.Stderr
	}
	status, err := driver.runner.Run(ctx, Command{
		Name:      containerBinary,
		Arguments: buildArguments(spec),
		Stdout:    output,
		Stderr:    output,
	})
	if err != nil {
		return err
	}
	if status != 0 {
		return fmt.Errorf("building image %s failed", spec.Tag)
	}
	return nil
}
