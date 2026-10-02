// Command prison-guest runs as PID 1 inside a box.
package main

import (
	"os"

	"prison/internal/guest"
)

func main() {
	os.Exit(guest.Main(os.Args[1:]))
}
