// Package applecontainer implements `cage.Cage` with Apple `container`. Each
// box runs in its own virtual machine.
package applecontainer

import (
	"context"
	"fmt"
	"os/exec"
	"time"

	"prison/internal/cage"
)

const containerBinary = "container"

type Driver struct {
	runner            Runner
	lookPath          func(file string) (string, error)
	readinessTimeout  time.Duration
	readinessInterval time.Duration
}

func New() *Driver {
	return NewWithRunner(SystemRunner{})
}

func NewWithRunner(runner Runner) *Driver {
	return &Driver{
		runner:            runner,
		lookPath:          exec.LookPath,
		readinessTimeout:  60 * time.Second,
		readinessInterval: 500 * time.Millisecond,
	}
}

func (driver *Driver) Name() string { return "apple-container" }

func (driver *Driver) Description() string {
	return "Apple `container`, one virtual machine per box"
}

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

func (driver *Driver) Available() bool {
	_, err := driver.lookPath(containerBinary)
	return err == nil
}

func (driver *Driver) Require(ctx context.Context) error {
	if !driver.Available() {
		return fmt.Errorf(
			"apple `container` not found. Install it, then run " +
				"`container system start`")
	}
	running, err := driver.runQuietly(
		ctx, containerBinary, "system", "status")
	if err != nil {
		return err
	}
	if !running {
		return fmt.Errorf(
			"container system is not running. Run `container system start`")
	}
	return nil
}

func (driver *Driver) DNS() cage.DNSDomain {
	return dnsHelper{driver: driver}
}

func (driver *Driver) Route() cage.HostRoute {
	return routeHelper{driver: driver}
}

var (
	_ cage.Cage      = (*Driver)(nil)
	_ cage.DNSDomain = dnsHelper{}
	_ cage.HostRoute = routeHelper{}
)
