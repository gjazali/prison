//go:build !linux

package guest

import (
	"errors"
	"net"
	"net/netip"
)

func originalDestination(conn *net.TCPConn) (netip.AddrPort, error) {
	return netip.AddrPort{}, errors.New("the guest tunnel runs only on Linux")
}
