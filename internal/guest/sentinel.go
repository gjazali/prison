package guest

import (
	"encoding/binary"
	"net/netip"
	"sync"
)

// Sentinel addresses are loopback, so connections fail fast when the
// tunnel is down.
var (
	firstSentinel = netip.AddrFrom4([4]byte{127, 99, 0, 1})
	lastSentinel  = netip.AddrFrom4([4]byte{127, 99, 255, 254})
)

// sentinelAllocator maps names to loopback addresses so that the tunnel
// can find the name of a connection.
type sentinelAllocator struct {
	mutex         sync.Mutex
	addressByName map[string]netip.Addr
	nameByAddress map[netip.Addr]string
	nextOffset    uint32
}

func newSentinelAllocator() *sentinelAllocator {
	return &sentinelAllocator{
		addressByName: map[string]netip.Addr{},
		nameByAddress: map[netip.Addr]string{},
	}
}

func (a *sentinelAllocator) Allocate(name string) (netip.Addr, bool) {
	a.mutex.Lock()
	defer a.mutex.Unlock()
	if address, ok := a.addressByName[name]; ok {
		return address, true
	}
	address := addressAtOffset(firstSentinel, a.nextOffset)
	if address.Compare(lastSentinel) > 0 {
		return netip.Addr{}, false
	}
	a.nextOffset++
	a.addressByName[name] = address
	a.nameByAddress[address] = name
	return address, true
}

func (a *sentinelAllocator) Lookup(address netip.Addr) (string, bool) {
	a.mutex.Lock()
	defer a.mutex.Unlock()
	name, ok := a.nameByAddress[address.Unmap()]
	return name, ok
}

func addressAtOffset(base netip.Addr, offset uint32) netip.Addr {
	raw := base.As4()
	value := binary.BigEndian.Uint32(raw[:]) + offset
	binary.BigEndian.PutUint32(raw[:], value)
	return netip.AddrFrom4(raw)
}
