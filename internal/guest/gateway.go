package guest

import (
	"encoding/binary"
	"errors"
	"net/netip"
	"strconv"
	"strings"
)

// parseIPRouteDefault extracts the gateway address from `ip route
// show default` output. Returns the address and true, or false if no
// gateway is found.
func parseIPRouteDefault(output string) (netip.Addr, bool) {
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		for index, field := range fields {
			if field == "via" && index+1 < len(fields) {
				if address, err := netip.ParseAddr(fields[index+1]); err == nil {
					return address, true
				}
			}
		}
	}
	return netip.Addr{}, false
}

// parseProcNetRoute extracts the default gateway from /proc/net/route
// content. Returns the gateway address or an error.
func parseProcNetRoute(content string) (netip.Addr, error) {
	lines := strings.Split(content, "\n")
	if len(lines) < 2 {
		return netip.Addr{}, errors.New("the routing table is empty")
	}
	for _, line := range lines[1:] {
		fields := strings.Fields(line)
		if len(fields) < 3 || fields[1] != "00000000" {
			continue
		}
		raw, err := strconv.ParseUint(fields[2], 16, 32)
		if err != nil {
			return netip.Addr{}, err
		}
		var bytes [4]byte
		binary.LittleEndian.PutUint32(bytes[:], uint32(raw))
		return netip.AddrFrom4(bytes), nil
	}
	return netip.Addr{}, errors.New("no default route")
}
