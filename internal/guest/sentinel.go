package guest

import (
	"encoding/binary"
	"net/netip"
	"sync"
)

// Sentinel address range: 127.99.0.1 through 127.99.255.254. All
// addresses are loopback, so resolved names fail immediately when the
// tunnel is down.
var (
	firstSentinel = netip.AddrFrom4([4]byte{127, 99, 0, 1})
	lastSentinel  = netip.AddrFrom4([4]byte{127, 99, 255, 254})
)

// sentinelAllocator assigns one loopback address per name and maps
// addresses back to names for the tunnel.
type sentinelAllocator struct {
	mutex         sync.Mutex
	addressByName map[string]netip.Addr
	nameByAddress map[netip.Addr]string
	freeAddresses []netip.Addr
	nextOffset    uint32
}

// newSentinelAllocator returns an empty allocator starting at
// 127.99.0.1.
func newSentinelAllocator() *sentinelAllocator {
	return &sentinelAllocator{
		addressByName: map[string]netip.Addr{},
		nameByAddress: map[netip.Addr]string{},
	}
}

// Allocate returns the sentinel address for name, allocating a new one
// on first use. Returns false if the range is exhausted.
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

// Lookup returns the name for the given sentinel address, or false
// if unknown.
func (a *sentinelAllocator) Lookup(address netip.Addr) (string, bool) {
	a.mutex.Lock()
	defer a.mutex.Unlock()
	name, ok := a.nameByAddress[address.Unmap()]
	return name, ok
}

// Release frees the address held by name and returns true. Returns
// false if name had no address.
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

// addressAtOffset returns the IPv4 address at the given offset from
// base.
func addressAtOffset(base netip.Addr, offset uint32) netip.Addr {
	raw := base.As4()
	value := binary.BigEndian.Uint32(raw[:]) + offset
	binary.BigEndian.PutUint32(raw[:], value)
	return netip.AddrFrom4(raw)
}
