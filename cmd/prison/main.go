// Command prison is the host-side CLI.
package main

import (
	"os"

	"prison/internal/cli"
)

func main() {
	os.Exit(cli.Execute(os.Args[1:]))
}
