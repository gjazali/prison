// Package guest runs inside the box as PID 1. It confines the box and
// tunnels connections through the broker.
package guest

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"time"
)

const (
	tunnelPort       = 8790
	resolverAddress  = "127.0.0.53"
	resolverPort     = 53
	resolvConfPath   = "/etc/resolv.conf"
	resolvConfMarker = "# written by prison"
	guestBinaryPath  = "/usr/local/bin/prison-guest"
	shimDirectory    = "/home/dev/.prison/bin"
	devUserName      = "dev"
	fallbackDevID    = 501
	brokerTimeout    = 60 * time.Second
)

// Main runs the `prison-guest` binary. A symlink named `prison-sign` or
// `prison-ssh-sign` runs that subcommand.
func Main(arguments []string) int {
	logger := log.New(os.Stderr, "prison-guest: ", 0)
	client := newBoxClient(brokerTimeout)
	switch filepath.Base(os.Args[0]) {
	case "prison-sign":
		return runSign(arguments, client, os.Stdin, os.Stdout, os.Stderr)
	case "prison-ssh-sign":
		return runSSHSign(arguments, client, os.Stdout, os.Stderr)
	}
	if len(arguments) == 0 {
		printUsage(os.Stderr)
		return 2
	}
	switch arguments[0] {
	case "init":
		return runInit(arguments[1:], logger)
	case "ready":
		return runReady(arguments[1:], logger)
	case "probe":
		return runProbe(arguments[1:], os.Stdout, os.Stderr)
	case "sign":
		return runSign(arguments[1:], client, os.Stdin, os.Stdout, os.Stderr)
	case "ssh-sign":
		return runSSHSign(arguments[1:], client, os.Stdout, os.Stderr)
	case "help", "-h", "--help":
		printUsage(os.Stdout)
		return 0
	}
	fmt.Fprintf(os.Stderr, "prison-guest: %q is not a subcommand\n",
		arguments[0])
	printUsage(os.Stderr)
	return 2
}

func printUsage(w io.Writer) {
	fmt.Fprint(w, `usage: prison-guest <subcommand> [arguments]

  init <command...>                   run as PID 1 and confine the box
  ready                               exit 0 when the resolver is ready
  probe --command a,b [--term NAME]   report which tools the box has
  sign <secret> [file]                sign a payload with the broker
  ssh-sign -Y sign -n NS -f KEY FILE  replace ssh-keygen -Y sign
`)
}
