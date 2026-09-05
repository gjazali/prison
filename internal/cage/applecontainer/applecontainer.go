// Package applecontainer implements cage.Cage using Apple's
// `container` runtime. Each box runs in its own virtual machine.
package applecontainer

import (
	"context"
	"fmt"
	"os/exec"
	"time"

	"prison/internal/cage"
)

// containerBinary is the backend CLI command name.
const containerBinary = "container"

// Driver is the Apple `container` cage. Build it with New.
type Driver struct {
	runner            Runner
	lookPath          func(file string) (string, error)
	readinessTimeout  time.Duration
	readinessInterval time.Duration
}

// New returns a driver that runs the real `container` CLI.
func New() *Driver {
	return NewWithRunner(SystemRunner{})
}

// NewWithRunner returns a driver that runs commands through the given
// runner.
func NewWithRunner(runner Runner) *Driver {
	return &Driver{
		runner:            runner,
		lookPath:          exec.LookPath,
		readinessTimeout:  60 * time.Second,
		readinessInterval: 500 * time.Millisecond,
	}
}

// Name returns the cage identifier used in `prison.toml`.
func (driver *Driver) Name() string { return "apple-container" }

// Description returns a short summary for cage listings.
func (driver *Driver) Description() string {
	return "Apple `container`, one virtual machine per box"
}

// Capabilities returns the set of features this backend supports.
func (driver *Driver) Capabilities() cage.Capabilities {
	return cage.Capabilities{
		Isolation:       cage.IsolationVM,
		GuestAddresses:  true,
		GuestHostnames:  true,
		DNSDomain:       true,
		RouteRepair:     true,
		HostOnlyNetwork: true,
		HostFirewall:    true,
	}
}

// Available returns true if the `container` command is on PATH.
func (driver *Driver) Available() bool {
	_, err := driver.lookPath(containerBinary)
	return err == nil
}

// Require returns an error if the backend is not installed or not
// running.
func (driver *Driver) Require(ctx context.Context) error {
	if !driver.Available() {
		return fmt.Errorf(
			"apple `container` not found; install it, then run " +
				"`container system start`")
	}
	running, err := driver.runQuietly(
		ctx, containerBinary, "system", "status")
	if err != nil {
		return err
	}
	if !running {
		return fmt.Errorf(
			"container system is not running; run " +
				"`container system start`")
	}
	return nil
}

// DNS returns the local DNS domain helper.
func (driver *Driver) DNS() cage.DNSDomain {
	return dnsHelper{driver: driver}
}

// Route returns the host route helper for macOS.
func (driver *Driver) Route() cage.HostRoute {
	return routeHelper{driver: driver}
}

// Compile-time interface checks.
var (
	_ cage.Cage      = (*Driver)(nil)
	_ cage.DNSDomain = dnsHelper{}
	_ cage.HostRoute = routeHelper{}
)
