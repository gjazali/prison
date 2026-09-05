//go:build linux

package guest

import (
	"encoding/binary"
	"net"
	"net/netip"
	"unsafe"

	"golang.org/x/sys/unix"
)

// originalDestination reads the original destination address from a
// redirected connection. Takes a TCP connection. Returns the IPv4
// address and port the client dialed. Fails if the socket option is
// missing, meaning the connection was not redirected.
func originalDestination(conn *net.TCPConn) (netip.AddrPort, error) {
	rawConn, err := conn.SyscallConn()
	if err != nil {
		return netip.AddrPort{}, err
	}
	var sockaddr unix.RawSockaddrInet4
	var optionErr error
	controlErr := rawConn.Control(func(descriptor uintptr) {
		size := uint32(unsafe.Sizeof(sockaddr))
		_, _, errno := unix.Syscall6(unix.SYS_GETSOCKOPT, descriptor,
			unix.SOL_IP, unix.SO_ORIGINAL_DST,
			uintptr(unsafe.Pointer(&sockaddr)),
			uintptr(unsafe.Pointer(&size)), 0)
		if errno != 0 {
			optionErr = errno
		}
	})
	if controlErr != nil {
		return netip.AddrPort{}, controlErr
	}
	if optionErr != nil {
		return netip.AddrPort{}, optionErr
	}
	// The kernel stores the port in network byte order.
	portBytes := (*[2]byte)(unsafe.Pointer(&sockaddr.Port))[:]
	port := binary.BigEndian.Uint16(portBytes)
	return netip.AddrPortFrom(netip.AddrFrom4(sockaddr.Addr), port), nil
}
