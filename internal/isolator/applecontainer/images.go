package applecontainer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"prison/internal/isolator"
)

func (driver *Driver) ImageExists(
	ctx context.Context, tag string,
) (bool, error) {
	return driver.runQuietly(
		ctx, containerBinary, "image", "inspect", tag)
}

func buildArguments(spec isolator.BuildSpec) []string {
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

func (driver *Driver) Build(
	ctx context.Context, spec isolator.BuildSpec,
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
		return fmt.Errorf("cannot build image %s", spec.Tag)
	}
	return nil
}

// ReleaseBuilder stops the BuildKit builder. Apple `container` keeps it and
// its guest memory after a build.
func (driver *Driver) ReleaseBuilder(ctx context.Context) error {
	_, err := driver.runQuietly(
		ctx, containerBinary, "builder", "stop")
	return err
}
