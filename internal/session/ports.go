package session

import (
	"fmt"
	"net"
	"strconv"
	"time"

	"prison/internal/cage"
	"prison/internal/config"
)

const portProbeTimeout = 100 * time.Millisecond

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
		// The recorded block skips the loopback probe because this
		// project's own box publishes it.
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

func blockIsTaken(taken map[int]bool, base, size int) bool {
	for offset := 0; offset < size; offset++ {
		if taken[base+offset] {
			return true
		}
	}
	return false
}

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

func PortsExhaustedMessage(base, limit int) string {
	return fmt.Sprintf(
		"host ports %d to %d are all taken. Remove an unused box with "+
			"`prison rm` or set the ports in prison.toml", base, limit)
}
