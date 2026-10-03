// Package firecracker implements `isolator.Isolator` with AWS Firecracker on
// Linux. Each box runs in its own microVM.
package firecracker

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"prison/internal/isolator"
)

const (
	firecrackerBinary = "firecracker"
	kvmDevicePath     = "/dev/kvm"
)

type Driver struct {
	stateDirectory  string
	kernel          fs.FS
	lookPath        func(file string) (string, error)
	deviceIsUsable  func(path string) bool
	runVersionCheck func(ctx context.Context, path string) (string, error)
	run             commandRunner
	getenv          func(name string) string
	fileExists      func(path string) bool
	sysfsNet        string
	uid             int
	gid             int
	procfs          string
	helperSource    func() (string, error)

	readinessTimeout  time.Duration
	readinessInterval time.Duration
	stopTimeout       time.Duration
}

func New(stateDirectory string, kernel fs.FS) *Driver {
	return &Driver{
		stateDirectory:  stateDirectory,
		kernel:          kernel,
		lookPath:        exec.LookPath,
		deviceIsUsable:  readableAndWritable,
		runVersionCheck: firstLineOfVersion,
		run:             runOnHost,
		getenv:          os.Getenv,
		fileExists:      pathExists,
		sysfsNet:        "/sys/class/net",
		uid:             os.Getuid(),
		gid:             os.Getgid(),
		procfs:          "/proc",
		helperSource:    os.Executable,

		readinessTimeout:  60 * time.Second,
		readinessInterval: 250 * time.Millisecond,
		stopTimeout:       20 * time.Second,
	}
}

func (driver *Driver) Require(ctx context.Context) error {
	if _, err := driver.lookPath(firecrackerBinary); err != nil {
		return fmt.Errorf("`firecracker` not found. Install it from " +
			"https://github.com/firecracker-microvm/firecracker/releases")
	}
	if !driver.deviceIsUsable(kvmDevicePath) {
		return fmt.Errorf("cannot use %s. Add the user to the `kvm` group",
			kvmDevicePath)
	}
	return nil
}

func (driver *Driver) Doctor(ctx context.Context, w io.Writer) {
	path, err := driver.lookPath(firecrackerBinary)
	if err != nil {
		fmt.Fprintf(w, "  firecracker  not found on PATH\n")
	} else {
		version, err := driver.runVersionCheck(ctx, path)
		if err != nil {
			version = fmt.Sprintf("cannot read the version: %v", err)
		}
		fmt.Fprintf(w, "  firecracker  %s (%s)\n", path, version)
	}
	kvmState := "usable"
	if !driver.deviceIsUsable(kvmDevicePath) {
		kvmState = "not usable"
	}
	fmt.Fprintf(w, "  kvm          %s %s\n", kvmDevicePath, kvmState)
	builder, err := driver.builderAddress()
	if err != nil {
		builder = err.Error()
	}
	fmt.Fprintf(w, "  buildkit     %s\n", builder)
	kernel, err := driver.readKernelFile(kernelVersionFile)
	kernelState := strings.TrimSpace(string(kernel))
	if err != nil {
		kernelState = "missing. Run `make kernel`"
	}
	fmt.Fprintf(w, "  kernel       %s\n", kernelState)
	for _, tool := range []struct{ name, label, fix string }{
		{"exportfs", "nfs server", "Install `nfs-kernel-server`"},
		{"nft", "nftables", "Install `nftables`"},
	} {
		state := "installed"
		if _, err := driver.lookPath(tool.name); err != nil &&
			!driver.fileExists("/usr/sbin/"+tool.name) {
			state = "missing. " + tool.fix
		}
		fmt.Fprintf(w, "  %-12s %s\n", tool.label, state)
	}
	helperState := "current"
	if !driver.nfsHelperIsCurrent(ctx) {
		helperState = "missing or outdated. Run `prison up`"
	}
	fmt.Fprintf(w, "  nfs helper   %s\n", helperState)
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func readableAndWritable(path string) bool {
	return unix.Access(path, unix.R_OK|unix.W_OK) == nil
}

func firstLineOfVersion(ctx context.Context, path string) (string, error) {
	output, err := exec.CommandContext(ctx, path, "--version").Output()
	if err != nil {
		return "", err
	}
	for index, character := range output {
		if character == '\n' {
			return string(output[:index]), nil
		}
	}
	return string(output), nil
}

var _ isolator.Base = (*Driver)(nil)
