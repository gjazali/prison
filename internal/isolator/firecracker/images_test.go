package firecracker

import (
	"bytes"
	"compress/gzip"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"prison/internal/isolator"
)

type recordedBuild struct {
	arguments []string
}

func buildDriver(t *testing.T) (*Driver, *[]recordedBuild) {
	t.Helper()
	driver := New(t.TempDir(), nil)
	driver.getenv = func(name string) string {
		if name == builderHostVariable {
			return "unix:///run/test/buildkitd.sock"
		}
		return ""
	}
	var builds []recordedBuild
	driver.run = func(ctx context.Context, command hostCommand) (int, error) {
		if command.name != buildctlBinary {
			t.Fatalf("command = %s, want %s", command.name, buildctlBinary)
		}
		builds = append(builds, recordedBuild{arguments: command.arguments})
		output := argumentAfter(command.arguments, "--output")
		destination := optionValue(output, "dest")
		if strings.HasPrefix(output, "type=oci") {
			index := `{"manifests":[{"digest":"sha256:` +
				strings.Repeat("a", 64) + `"}]}`
			writeFile(t, filepath.Join(destination, "index.json"), index)
		} else {
			writeFile(t, filepath.Join(destination, diskFileName), "ext4")
		}
		return 0, nil
	}
	return driver, &builds
}

func argumentAfter(arguments []string, flag string) string {
	for index, argument := range arguments[:len(arguments)-1] {
		if argument == flag {
			return arguments[index+1]
		}
	}
	return ""
}

func optionValue(options, key string) string {
	for _, option := range strings.Split(options, ",") {
		if value, found := strings.CutPrefix(option, key+"="); found {
			return value
		}
	}
	return ""
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestBuildStoresTheLayout(t *testing.T) {
	driver, builds := buildDriver(t)
	ctx := context.Background()
	spec := isolator.BuildSpec{
		Tag:     "prison-base:abc",
		Context: "/tmp/context",
		Args:    map[string]string{"UID": "501", "PRISON_FOUNDATION": "x"},
		Output:  &bytes.Buffer{},
	}
	if err := driver.Build(ctx, spec); err != nil {
		t.Fatalf("Build = %v, want nil", err)
	}
	present, err := driver.ImageExists(ctx, "prison-base:abc")
	if err != nil || !present {
		t.Fatalf("ImageExists = %v, %v, want true", present, err)
	}
	arguments := strings.Join((*builds)[0].arguments, " ")
	for _, want := range []string{
		"--addr unix:///run/test/buildkit",
		"--local dockerfile=/tmp/context --opt filename=Dockerfile",
		"build-arg:PRISON_FOUNDATION=x --opt build-arg:UID=501",
		",name=prison-base:abc",
	} {
		if !strings.Contains(arguments, want) {
			t.Errorf("arguments = %q, want %q", arguments, want)
		}
	}
	if strings.Contains(arguments, "--oci-layout") {
		t.Errorf("arguments = %q, want no named context", arguments)
	}
}

func TestBuildUsesStoredImagesAsNamedContexts(t *testing.T) {
	driver, builds := buildDriver(t)
	ctx := context.Background()
	base := isolator.BuildSpec{Tag: "prison-base:abc", Context: "/tmp/base"}
	if err := driver.Build(ctx, base); err != nil {
		t.Fatalf("Build(base) = %v, want nil", err)
	}
	inmate := isolator.BuildSpec{
		Tag:        "prison-box:def",
		Context:    "/tmp/inmate",
		Dockerfile: "images/Dockerfile",
		Args:       map[string]string{"PRISON_BASE": "prison-base:abc"},
	}
	if err := driver.Build(ctx, inmate); err != nil {
		t.Fatalf("Build(inmate) = %v, want nil", err)
	}
	arguments := strings.Join((*builds)[1].arguments, " ")
	for _, want := range []string{
		"--local dockerfile=/tmp/inmate/images --opt filename=Dockerfile",
		"--oci-layout image1=" + driver.layoutPath("prison-base:abc"),
		"context:prison-base:abc=oci-layout://image1@sha256:aaaa",
	} {
		if !strings.Contains(arguments, want) {
			t.Errorf("arguments = %q, want %q", arguments, want)
		}
	}
}

func TestBuildFailureKeepsThePreviousLayout(t *testing.T) {
	driver, _ := buildDriver(t)
	ctx := context.Background()
	spec := isolator.BuildSpec{Tag: "prison-base:abc", Context: "/tmp/base"}
	if err := driver.Build(ctx, spec); err != nil {
		t.Fatalf("Build = %v, want nil", err)
	}
	driver.run = func(context.Context, hostCommand) (int, error) {
		return 1, nil
	}
	err := driver.Build(ctx, spec)
	if err == nil || !strings.Contains(err.Error(), "cannot build image") {
		t.Fatalf("Build = %v, want error", err)
	}
	present, _ := driver.ImageExists(ctx, "prison-base:abc")
	if !present {
		t.Error("ImageExists = false after a failed rebuild, want true")
	}
	partial := driver.layoutPath("prison-base:abc") + partialSuffix
	if _, err := os.Stat(partial); !os.IsNotExist(err) {
		t.Errorf("Stat(%s) = %v, want not exist", partial, err)
	}
}

func TestEnsureDiskPacksOnce(t *testing.T) {
	driver, builds := buildDriver(t)
	ctx := context.Background()
	spec := isolator.BuildSpec{Tag: "prison-box:def", Context: "/tmp/box"}
	if err := driver.Build(ctx, spec); err != nil {
		t.Fatalf("Build = %v, want nil", err)
	}
	for range 2 {
		disk, err := driver.ensureDisk(ctx, "prison-box:def", &bytes.Buffer{})
		if err != nil {
			t.Fatalf("ensureDisk = %v, want nil", err)
		}
		if disk != driver.diskPath("prison-box:def") {
			t.Errorf("ensureDisk = %s, want %s", disk,
				driver.diskPath("prison-box:def"))
		}
	}
	if len(*builds) != 2 {
		t.Fatalf("builds = %d, want 2", len(*builds))
	}
	arguments := strings.Join((*builds)[1].arguments, " ")
	if !strings.Contains(arguments, "context:prison-box:def=oci-layout://") ||
		!strings.Contains(arguments, "--output type=local,dest=") {
		t.Errorf("arguments = %q, want a local export of the box", arguments)
	}
	if err := driver.Build(ctx, spec); err != nil {
		t.Fatalf("Build = %v, want nil", err)
	}
	if _, err := os.Stat(driver.diskPath("prison-box:def")); err == nil {
		t.Error("Stat(disk) after Build = nil error, want not exist")
	}
}

func TestEnsureDiskNeedsTheImage(t *testing.T) {
	driver, _ := buildDriver(t)
	_, err := driver.ensureDisk(context.Background(), "prison-box:none", nil)
	if err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("ensureDisk = %v, want error", err)
	}
}

func TestBuilderAddress(t *testing.T) {
	driver := New(t.TempDir(), nil)
	environment := map[string]string{"XDG_RUNTIME_DIR": "/run/user/501"}
	existing := map[string]bool{}
	driver.getenv = func(name string) string { return environment[name] }
	driver.fileExists = func(path string) bool { return existing[path] }

	if _, err := driver.builderAddress(); err == nil {
		t.Error("builderAddress = nil error with no socket, want error")
	}
	existing["/run/buildkit/buildkitd.sock"] = true
	address, _ := driver.builderAddress()
	if address != "unix:///run/buildkit/buildkitd.sock" {
		t.Errorf("builderAddress = %q, want the system socket", address)
	}
	existing["/run/user/501/buildkit/buildkitd.sock"] = true
	address, _ = driver.builderAddress()
	if address != "unix:///run/user/501/buildkit/buildkitd.sock" {
		t.Errorf("builderAddress = %q, want the rootless socket", address)
	}
	environment[builderHostVariable] = "tcp://builder:1234"
	address, _ = driver.builderAddress()
	if address != "tcp://builder:1234" {
		t.Errorf("builderAddress = %q, want BUILDKIT_HOST", address)
	}
}

func TestKernelPathUnpacksOnce(t *testing.T) {
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	writer.Write([]byte("kernel image"))
	writer.Close()
	kernel := fstest.MapFS{
		kernelArchiveName(): {Data: compressed.Bytes()},
		kernelVersionFile:   {Data: []byte("6.18.54\n")},
	}
	driver := New(t.TempDir(), kernel)
	path, err := driver.kernelPath()
	if err != nil {
		t.Fatalf("kernelPath = %v, want nil", err)
	}
	if !strings.Contains(path, "6.18.54-") {
		t.Errorf("kernelPath = %s, want the version in the path", path)
	}
	content, err := os.ReadFile(path)
	if err != nil || string(content) != "kernel image" {
		t.Errorf("kernel content = %q, %v, want \"kernel image\"", content,
			err)
	}
	again, err := driver.kernelPath()
	if err != nil || again != path {
		t.Errorf("kernelPath again = %s, %v, want %s", again, err, path)
	}
}

func TestKernelPathWithoutKernel(t *testing.T) {
	driver := New(t.TempDir(), fstest.MapFS{})
	_, err := driver.kernelPath()
	if err == nil || !strings.Contains(err.Error(), "make kernel") {
		t.Errorf("kernelPath = %v, want a `make kernel` error", err)
	}
}
