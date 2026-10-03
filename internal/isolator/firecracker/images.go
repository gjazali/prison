package firecracker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"prison/internal/isolator"
)

const (
	buildctlBinary      = "buildctl"
	layoutDirectory     = "oci"
	diskFileName        = "rootfs.ext4"
	partialSuffix       = ".partial"
	defaultDockerfile   = "Dockerfile"
	builderHostVariable = "BUILDKIT_HOST"
)

// packDockerfile packs an image into an ext4 disk. `mkfs.ext4` runs as root
// in the build so that the disk keeps the file owners.
const packDockerfile = `FROM %s AS rootfs
FROM alpine:3.22 AS packer
RUN apk add --no-cache e2fsprogs
RUN --mount=type=bind,from=rootfs,target=/rootfs \
    size="$(du -sm /rootfs | cut -f1)" && \
    mkdir /out && \
    truncate -s "$((size * 13 / 10 + 512))M" /out/` + diskFileName + ` && \
    mkfs.ext4 -q -F -d /rootfs /out/` + diskFileName + `
FROM scratch
COPY --from=packer /out/` + diskFileName + ` /
`

var tagCharacters = strings.NewReplacer("/", "_", ":", "_", "@", "_")

func (driver *Driver) imageDirectory(tag string) string {
	return filepath.Join(driver.stateDirectory, "images",
		tagCharacters.Replace(tag))
}

func (driver *Driver) layoutPath(tag string) string {
	return filepath.Join(driver.imageDirectory(tag), layoutDirectory)
}

func (driver *Driver) diskPath(tag string) string {
	return filepath.Join(driver.imageDirectory(tag), diskFileName)
}

func (driver *Driver) requireStateDirectory() error {
	if driver.stateDirectory == "" {
		return errors.New("the aws-firecracker isolator has no state directory")
	}
	return nil
}

func (driver *Driver) ImageExists(
	ctx context.Context, tag string,
) (bool, error) {
	if err := driver.requireStateDirectory(); err != nil {
		return false, err
	}
	_, err := os.Stat(filepath.Join(driver.layoutPath(tag), "index.json"))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

func (driver *Driver) Build(
	ctx context.Context, spec isolator.BuildSpec) error {
	if err := driver.requireStateDirectory(); err != nil {
		return err
	}
	address, err := driver.builderAddress()
	if err != nil {
		return err
	}
	dockerfile := spec.Dockerfile
	if dockerfile == "" {
		dockerfile = defaultDockerfile
	}
	dockerfilePath := filepath.Join(spec.Context, dockerfile)
	arguments := []string{
		"--addr", address, "build", "--progress", "plain",
		"--frontend", "dockerfile.v0",
		"--local", "context=" + spec.Context,
		"--local", "dockerfile=" + filepath.Dir(dockerfilePath),
		"--opt", "filename=" + filepath.Base(dockerfilePath),
	}
	names := make([]string, 0, len(spec.Args))
	values := make([]string, 0, len(spec.Args))
	for name := range spec.Args {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		arguments = append(arguments,
			"--opt", "build-arg:"+name+"="+spec.Args[name])
		values = append(values, spec.Args[name])
	}
	contexts, err := driver.localImageContexts(values)
	if err != nil {
		return err
	}
	arguments = append(arguments, contexts...)
	layout := driver.layoutPath(spec.Tag)
	partial := layout + partialSuffix
	if err := os.RemoveAll(partial); err != nil {
		return fmt.Errorf("cannot clear %s: %w", partial, err)
	}
	arguments = append(arguments, "--output",
		"type=oci,tar=false,dest="+partial+",name="+spec.Tag)
	output := spec.Output
	if output == nil {
		output = os.Stderr
	}
	if err := driver.runBuild(ctx, arguments, output, spec.Tag); err != nil {
		os.RemoveAll(partial)
		return err
	}
	if err := os.RemoveAll(layout); err != nil {
		return fmt.Errorf("cannot replace %s: %w", layout, err)
	}
	if err := os.Rename(partial, layout); err != nil {
		return fmt.Errorf("cannot store image %s: %w", spec.Tag, err)
	}
	return os.RemoveAll(driver.diskPath(spec.Tag))
}

// ReleaseBuilder leaves the BuildKit daemon running because the user owns
// it.
func (driver *Driver) ReleaseBuilder(ctx context.Context) error {
	return nil
}

func (driver *Driver) ensureDisk(
	ctx context.Context, tag string, output io.Writer,
) (string, error) {
	disk := driver.diskPath(tag)
	if _, err := os.Stat(disk); err == nil {
		return disk, nil
	}
	present, err := driver.ImageExists(ctx, tag)
	if err != nil {
		return "", err
	}
	if !present {
		return "", fmt.Errorf("image %s does not exist. Run `prison build`",
			tag)
	}
	address, err := driver.builderAddress()
	if err != nil {
		return "", err
	}
	work, err := os.MkdirTemp(driver.imageDirectory(tag), "pack-")
	if err != nil {
		return "", fmt.Errorf("cannot create a pack directory: %w", err)
	}
	defer os.RemoveAll(work)
	dockerfile := filepath.Join(work, defaultDockerfile)
	content := fmt.Sprintf(packDockerfile, tag)
	if err := os.WriteFile(dockerfile, []byte(content), 0o644); err != nil {
		return "", fmt.Errorf("cannot write %s: %w", dockerfile, err)
	}
	contexts, err := driver.localImageContexts([]string{tag})
	if err != nil {
		return "", err
	}
	exported := filepath.Join(work, "out")
	arguments := append([]string{
		"--addr", address, "build", "--progress", "plain",
		"--frontend", "dockerfile.v0",
		"--local", "context=" + work,
		"--local", "dockerfile=" + work,
	}, contexts...)
	arguments = append(arguments,
		"--output", "type=local,dest="+exported)
	if err := driver.runBuild(ctx, arguments, output, tag); err != nil {
		return "", err
	}
	if err := os.Rename(filepath.Join(exported, diskFileName),
		disk); err != nil {
		return "", fmt.Errorf("cannot store the disk of %s: %w", tag, err)
	}
	return disk, nil
}

func (driver *Driver) runBuild(
	ctx context.Context, arguments []string, output io.Writer, tag string,
) error {
	status, err := driver.run(ctx, hostCommand{
		name:      buildctlBinary,
		arguments: arguments,
		stdout:    output,
		stderr:    output,
	})
	if err != nil {
		return fmt.Errorf("cannot run `buildctl`: %w", err)
	}
	if status != 0 {
		return fmt.Errorf("cannot build image %s", tag)
	}
	return nil
}

// localImageContexts lets a Dockerfile use a stored image in `FROM` through
// BuildKit named contexts.
func (driver *Driver) localImageContexts(values []string) ([]string, error) {
	var arguments []string
	seen := map[string]bool{}
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		present, err := driver.ImageExists(context.Background(), value)
		if err != nil {
			return nil, err
		}
		if !present {
			continue
		}
		digest, err := layoutDigest(driver.layoutPath(value))
		if err != nil {
			return nil, err
		}
		alias := fmt.Sprintf("image%d", len(seen))
		arguments = append(arguments,
			"--oci-layout", alias+"="+driver.layoutPath(value),
			"--opt", "context:"+value+"=oci-layout://"+alias+"@"+digest)
	}
	return arguments, nil
}

func layoutDigest(layout string) (string, error) {
	content, err := os.ReadFile(filepath.Join(layout, "index.json"))
	if err != nil {
		return "", fmt.Errorf("cannot read the image index: %w", err)
	}
	var index struct {
		Manifests []struct {
			Digest string `json:"digest"`
		} `json:"manifests"`
	}
	if err := json.Unmarshal(content, &index); err != nil {
		return "", fmt.Errorf("the image index in %s is not valid: %w",
			layout, err)
	}
	if len(index.Manifests) == 0 || index.Manifests[0].Digest == "" {
		return "", fmt.Errorf("the image index in %s is empty", layout)
	}
	return index.Manifests[0].Digest, nil
}

func (driver *Driver) builderAddress() (string, error) {
	if address := driver.getenv(builderHostVariable); address != "" {
		return address, nil
	}
	var candidates []string
	runtimeDirectory := driver.getenv("XDG_RUNTIME_DIR")
	if runtimeDirectory != "" {
		candidates = append(candidates,
			filepath.Join(runtimeDirectory, "buildkit", "buildkitd.sock"))
	}
	candidates = append(candidates, "/run/buildkit/buildkitd.sock")
	for _, candidate := range candidates {
		if driver.fileExists(candidate) {
			return "unix://" + candidate, nil
		}
	}
	return "", errors.New("no BuildKit daemon found. Start `buildkitd` " +
		"or set `BUILDKIT_HOST`")
}
