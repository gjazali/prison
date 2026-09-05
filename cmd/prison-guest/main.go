// Command prison-guest runs inside a box as PID 1. It installs
// confinement, runs the resolver and tunnel, and supervises the box's
// command.
package main

import (
	"os"

	"prison/internal/guest"
)

// main runs the guest subcommand and exits with its status code.
func main() {
	os.Exit(guest.Main(os.Args[1:]))
}
