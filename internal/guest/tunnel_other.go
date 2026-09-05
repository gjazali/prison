//go:build !linux

package guest

import (
	"errors"
	"net"
	"net/netip"
)

// originalDestination always fails on non-Linux platforms. Takes a
// TCP connection. Returns an error because the guest tunnel needs
// Linux netfilter.
func originalDestination(conn *net.TCPConn) (netip.AddrPort, error) {
	return netip.AddrPort{}, errors.New(
		"the guest tunnel only runs on Linux")
}
