package image

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"prison/internal/isolator"
)

const contextDirectoryMode = 0o755

// contextFileMode applies to every file because embedded filesystems carry
// no modes. Dockerfiles set other modes with `COPY --chmod`.
const contextFileMode = 0o644

func Ensure(
	ctx context.Context, c isolator.Isolator, plan *Plan, out io.Writer,
) error {
	if out == nil {
		out = io.Discard
	}
	started := false
	defer func() {
		if started {
			releaseBuilder(ctx, c, out)
		}
	}()
	for _, step := range plan.Steps {
		present, err := c.ImageExists(ctx, step.Tag)
		if err != nil {
			return fmt.Errorf("cannot look up image %s: %w", step.Tag, err)
		}
		if present {
			continue
		}
		if isBaseStep(step) &&
			!contextHasGuestBinary(step.Context, guestBinaryName) {
			return fmt.Errorf(
				"the base image has no guest binary. Run `make build`")
		}
		fmt.Fprintf(out, "building  %s (%s)\n", step.Tag, step.Description)
		started = true
		if err := buildStep(ctx, c, step, out); err != nil {
			return err
		}
	}
	return nil
}

// releaseBuilder ignores cancellation so that an interrupted build still
// releases the builder.
func releaseBuilder(ctx context.Context, c isolator.Isolator, out io.Writer) {
	err := c.ReleaseBuilder(context.WithoutCancel(ctx))
	if err != nil {
		fmt.Fprintf(out, "note      cannot stop the image builder: %v\n", err)
	}
}

func buildStep(
	ctx context.Context, c isolator.Isolator, step Step, out io.Writer,
) error {
	directory, err := os.MkdirTemp("", "prison-build-")
	if err != nil {
		return fmt.Errorf("cannot create a build directory for %s: %w",
			step.Tag, err)
	}
	defer os.RemoveAll(directory)
	if err := ExtractFS(step.Context, directory); err != nil {
		return fmt.Errorf("cannot write the build context for %s: %w",
			step.Tag, err)
	}
	err = c.Build(ctx, isolator.BuildSpec{
		Tag:        step.Tag,
		Context:    directory,
		Dockerfile: step.Dockerfile,
		Args:       step.Args,
		Output:     out,
	})
	if err != nil {
		return fmt.Errorf("cannot build %s: %w", step.Tag, err)
	}
	return nil
}

func ExtractFS(source fs.FS, destination string) error {
	if err := os.MkdirAll(destination, contextDirectoryMode); err != nil {
		return fmt.Errorf("cannot create %s: %w", destination, err)
	}
	return fs.WalkDir(source, ".", func(
		name string, entry fs.DirEntry, err error,
	) error {
		if err != nil {
			return err
		}
		if name == "." {
			return nil
		}
		target := filepath.Join(destination, filepath.FromSlash(name))
		if entry.IsDir() {
			if err := os.MkdirAll(target, contextDirectoryMode); err != nil {
				return fmt.Errorf("cannot create %s: %w", target, err)
			}
			return nil
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		return copyFile(source, name, target)
	})
}

func copyFile(source fs.FS, name, target string) error {
	parent := filepath.Dir(target)
	if err := os.MkdirAll(parent, contextDirectoryMode); err != nil {
		return fmt.Errorf("cannot create %s: %w", parent, err)
	}
	reader, err := source.Open(name)
	if err != nil {
		return fmt.Errorf("cannot read %s: %w", name, err)
	}
	defer reader.Close()
	writer, err := os.OpenFile(
		target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, contextFileMode)
	if err != nil {
		return fmt.Errorf("cannot write %s: %w", target, err)
	}
	if _, err := io.Copy(writer, reader); err != nil {
		writer.Close()
		return fmt.Errorf("cannot write %s: %w", target, err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("cannot write %s: %w", target, err)
	}
	return nil
}
