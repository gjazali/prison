package session

import (
	"fmt"
	"net"
	"strconv"
	"time"

	"prison/internal/cage"
	"prison/internal/config"
)

// portProbeTimeout is how long to wait before treating a port as free.
const portProbeTimeout = 100 * time.Millisecond

// ResolvePorts returns the port mappings the box publishes. Returns
// nil when nothing is asked for or no block is free.
func (s *Session) ResolvePorts() ([]cage.PortMapping, error) {
	guestPorts, explicit := config.ResolvePorts(s.Overrides, s.Config)
	if explicit {
		mappings := make([]cage.PortMapping, 0, len(guestPorts))
		for _, port := range guestPorts {
			mappings = append(mappings, cage.PortMapping{Host: port, Guest: port})
		}
		return mappings, nil
	}
	if len(guestPorts) == 0 {
		return nil, nil
	}
	base, err := s.allocatePortBlock(len(guestPorts))
	if err != nil {
		return nil, err
	}
	if base == 0 {
		return nil, nil
	}
	mappings := make([]cage.PortMapping, 0, len(guestPorts))
	for index, guest := range guestPorts {
		mappings = append(mappings, cage.PortMapping{
			Host:  base + index,
			Guest: guest,
		})
	}
	return mappings, nil
}

// allocatePortBlock takes a size and returns the base port of a free
// block. Returns zero if no block is available.
func (s *Session) allocatePortBlock(size int) (int, error) {
	claimed, err := s.Root.ClaimedPortBlocks()
	if err != nil {
		return 0, err
	}
	taken := map[int]bool{}
	for id, base := range claimed {
		if id == s.Project.ID {
			continue
		}
		for offset := 0; offset < size; offset++ {
			taken[base+offset] = true
		}
	}
	if s.Record != nil && s.Record.PortBlock != 0 {
		base := s.Record.PortBlock
		// Skip loopback probe for the recorded block; this project's
		// own box is publishing it.
		if base >= s.Overrides.PortBase &&
			base+size-1 <= s.Overrides.PortLimit &&
			!blockIsTaken(taken, base, size) {
			return base, nil
		}
	}
	for base := s.Overrides.PortBase; base+size-1 <= s.Overrides.PortLimit; base += size {
		if blockIsTaken(taken, base, size) {
			continue
		}
		if !blockIsFree(base, size) {
			continue
		}
		return base, nil
	}
	return 0, nil
}

// blockIsTaken takes a map of claimed ports, a base, and a size.
// Returns true if any port in the block is claimed.
func blockIsTaken(taken map[int]bool, base, size int) bool {
	for offset := 0; offset < size; offset++ {
		if taken[base+offset] {
			return true
		}
	}
	return false
}

// blockIsFree takes a base port and size. Returns true if no port in
// the block is listening on loopback.
func blockIsFree(base, size int) bool {
	for offset := 0; offset < size; offset++ {
		address := net.JoinHostPort("127.0.0.1", strconv.Itoa(base+offset))
		connection, err := net.DialTimeout("tcp", address, portProbeTimeout)
		if err == nil {
			connection.Close()
			return false
		}
	}
	return true
}

// PortsExhaustedMessage returns the message shown when every port in
// the range is taken.
func PortsExhaustedMessage(base, limit int) string {
	return fmt.Sprintf(
		"every host port from %d to %d is taken, so nothing is published; "+
			"free a block with `prison rm` on a project you are done with, "+
			"or name the ports in prison.toml", base, limit)
}
