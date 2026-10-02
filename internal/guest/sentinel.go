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
	freeAddresses []netip.Addr
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
	var address netip.Addr
	switch {
	case len(a.freeAddresses) > 0:
		last := len(a.freeAddresses) - 1
		address = a.freeAddresses[last]
		a.freeAddresses = a.freeAddresses[:last]
	default:
		candidate := addressAtOffset(firstSentinel, a.nextOffset)
		if candidate.Compare(lastSentinel) > 0 {
			return netip.Addr{}, false
		}
		address = candidate
		a.nextOffset++
	}
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

func (a *sentinelAllocator) Release(name string) bool {
	a.mutex.Lock()
	defer a.mutex.Unlock()
	address, ok := a.addressByName[name]
	if !ok {
		return false
	}
	delete(a.addressByName, name)
	delete(a.nameByAddress, address)
	a.freeAddresses = append(a.freeAddresses, address)
	return true
}

func addressAtOffset(base netip.Addr, offset uint32) netip.Addr {
	raw := base.As4()
	value := binary.BigEndian.Uint32(raw[:]) + offset
	binary.BigEndian.PutUint32(raw[:], value)
	return netip.AddrFrom4(raw)
}
