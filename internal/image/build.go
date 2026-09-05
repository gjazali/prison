package image

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"prison/internal/cage"
)

// contextDirectoryMode is the permission used for directories in an
// extracted build context.
const contextDirectoryMode = 0o755

// contextFileMode is the permission used for files in an extracted
// build context. Embedded filesystems carry no modes, so Dockerfiles
// set the mode they need with `COPY --chmod`.
const contextFileMode = 0o644

// Ensure builds each missing step of a plan in order. It takes a
// context, a cage, a plan, and a writer for progress output. It
// returns an error on the first failure. A nil `out` discards output.
// It refuses to build a base image without the guest binary.
func Ensure(
	ctx context.Context, c cage.Cage, plan *Plan, out io.Writer,
) error {
	if out == nil {
		out = io.Discard
	}
	for _, step := range plan.Steps {
		present, err := c.ImageExists(ctx, step.Tag)
		if err != nil {
			return fmt.Errorf("cannot tell whether %s is built: %w",
				step.Tag, err)
		}
		if present {
			fmt.Fprintf(out, "have      %s\n", step.Tag)
			continue
		}
		if isBaseStep(step) &&
			!contextHasGuestBinary(step.Context, guestBinaryName) {
			return fmt.Errorf("the guest binary is missing from the base " +
				"image context; run `make build`")
		}
		fmt.Fprintf(out, "building  %s (%s)\n", step.Tag, step.Description)
		if err := buildStep(ctx, c, step, out); err != nil {
			return err
		}
	}
	return nil
}

// buildStep builds one image step. It takes a context, a cage, a
// step, and an output writer. It extracts the step's files into a
// temporary directory, builds the image, and removes the directory
// afterwards. It returns an error if the build fails.
func buildStep(
	ctx context.Context, c cage.Cage, step Step, out io.Writer,
) error {
	directory, err := os.MkdirTemp("", "prison-build-")
	if err != nil {
		return fmt.Errorf("cannot create a build directory for %s: %w",
			step.Tag, err)
	}
	defer os.RemoveAll(directory)
	if err := ExtractFS(step.Context, directory); err != nil {
		return fmt.Errorf("cannot lay out the build context for %s: %w",
			step.Tag, err)
	}
	err = c.Build(ctx, cage.BuildSpec{
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

// ExtractFS copies every file from `source` into `destination`. It
// creates directories as needed. Directories get mode 0755 and files
// get mode 0644. It returns an error if any file cannot be written.
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

// copyFile writes one file from `source` at `name` to `target` on
// disk. It streams the data and creates parent directories as needed.
// It returns an error if the file cannot be read or written.
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
