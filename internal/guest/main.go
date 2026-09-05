// Package guest is the in-box side of prison. It runs as PID 1,
// sets up confinement, and tunnels connections through the broker.
// It targets Linux but compiles on all platforms.
package guest

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"time"
)

// Fixed addresses and paths inside a box.
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

// Main dispatches a guest subcommand and returns the process exit
// status. It is the whole body of the prison-guest binary. When the
// binary is reached through a symlink named prison-sign or
// prison-ssh-sign, the arguments are those of the sign or ssh-sign
// subcommand.
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

// printUsage writes the subcommand summary to w.
func printUsage(w io.Writer) {
	fmt.Fprint(w, `usage: prison-guest <subcommand> [arguments]

  init <command...>                   PID 1: confine the box, run command
  ready                               exit 0 once the resolver is in place
  probe --command a,b [--term NAME]   report which tools the box has
  sign <secret> [file]                have the broker sign a payload
  ssh-sign -Y sign -n NS -f KEY FILE  stand in for ssh-keygen -Y sign
`)
}
