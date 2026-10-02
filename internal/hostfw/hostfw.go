// Package hostfw manages the host firewall for the box network. On macOS
// it uses a pf anchor, lines in the main ruleset, and a launchd boot job.
// On Linux it uses an nftables table and a systemd unit.
package hostfw

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// Spec describes the box network. SubnetV6 is empty when the network has
// no IPv6.
type Spec struct {
	SubnetV4   string
	SubnetV6   string
	BrokerPort int
}

func (spec Spec) validate() error {
	if strings.TrimSpace(spec.SubnetV4) == "" {
		return errors.New(
			"the box network has no IPv4 subnet. Run `prison up` first")
	}
	if spec.BrokerPort < 1 || spec.BrokerPort > 65535 {
		return fmt.Errorf("broker port %d is not between 1 and 65535",
			spec.BrokerPort)
	}
	return nil
}

type Runner func(ctx context.Context, name string, args ...string) (string, error)

func SystemRunner(ctx context.Context, name string, args ...string) (string, error) {
	output, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		return string(output), fmt.Errorf("%s: %w", name, err)
	}
	return string(output), nil
}

type State string

// StateStale covers any partial or mismatched install.
const (
	StateInstalled   State = "installed"
	StateMissing     State = "missing"
	StateStale       State = "stale"
	StateUnsupported State = "unsupported"
)

func writeStagedFile(stagingDir, name, content string) (string, error) {
	if err := os.MkdirAll(stagingDir, 0o700); err != nil {
		return "", fmt.Errorf("cannot create %s: %w", stagingDir, err)
	}
	path := filepath.Join(stagingDir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return "", fmt.Errorf("cannot write %s: %w", path, err)
	}
	return path, nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func Rules(spec Spec) string {
	switch runtime.GOOS {
	case "darwin":
		return pfRules(spec)
	case "linux":
		return nftRules(spec)
	}
	return ""
}

// OpenPorts returns the host ports that a box can reach.
func OpenPorts(spec Spec) []int {
	if runtime.GOOS == "linux" {
		return []int{spec.BrokerPort, nfsPort}
	}
	return []int{spec.BrokerPort}
}

// ReadCommand returns the command that shows the loaded rules.
func ReadCommand() string {
	if runtime.GOOS == "linux" {
		return nftReadCommand()
	}
	return pfReadCommand()
}

func Describe(spec Spec) []string {
	if runtime.GOOS == "linux" {
		return nftDescribe(spec)
	}
	return pfDescribe(spec)
}

func isSupportedOperatingSystem() bool {
	return runtime.GOOS == "darwin" || runtime.GOOS == "linux"
}

func unsupportedError() error {
	return fmt.Errorf("no firewall rules for %s", runtime.GOOS)
}

func Status(ctx context.Context, spec Spec, stagingDir string, run Runner) (State, error) {
	if !isSupportedOperatingSystem() {
		return StateUnsupported, nil
	}
	if err := spec.validate(); err != nil {
		return StateMissing, err
	}
	if runtime.GOOS == "linux" {
		return nftStatus(ctx, spec, nftSystemPaths(), run)
	}
	return pfStatus(ctx, spec, stagingDir, systemPaths(), run)
}

func Install(ctx context.Context, spec Spec, stagingDir string, run Runner, out io.Writer) error {
	if !isSupportedOperatingSystem() {
		return unsupportedError()
	}
	if err := spec.validate(); err != nil {
		return err
	}
	if runtime.GOOS == "linux" {
		return nftInstall(ctx, spec, stagingDir, nftSystemPaths(), run)
	}
	return pfInstall(ctx, spec, stagingDir, systemPaths(), run, out)
}

// Remove writes nonfatal failures to `out` as warnings.
func Remove(ctx context.Context, spec Spec, stagingDir string, run Runner, out io.Writer) error {
	if !isSupportedOperatingSystem() {
		return unsupportedError()
	}
	if runtime.GOOS == "linux" {
		return nftRemove(ctx, nftSystemPaths(), run)
	}
	return pfRemove(ctx, stagingDir, systemPaths(), run, out)
}
