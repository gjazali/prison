// Command prison is the host-side CLI. Subcommands live in package
// cli.
package main

import (
	"os"

	"prison/internal/cli"
)

// main runs the CLI and exits with its status code.
func main() {
	os.Exit(cli.Execute(os.Args[1:]))
}
