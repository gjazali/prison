package applecontainer

import (
	"context"
	"fmt"
	"math/bits"
	"strconv"
	"strings"

	"prison/internal/cage"
)

// routeHelper repairs the host's route to the box network. Reads
// live state from the host since the bridge only exists while a box
// is running.
type routeHelper struct {
	driver *Driver
}

// Describe returns the route for the given gateway, or nil if no
// interface carries it.
func (helper routeHelper) Describe(
	ctx context.Context, gateway string,
) (*cage.RouteInfo, error) {
	if gateway == "" {
		return nil, nil
	}
	stdout, _, status, err := helper.driver.runCapturing(ctx, "ifconfig")
	if err != nil {
		return nil, err
	}
	if status != 0 {
		return nil, fmt.Errorf("reading the host interfaces failed")
	}
	interfaceName, netmask := interfaceCarrying(string(stdout), gateway)
	if interfaceName == "" || netmask == "" {
		return nil, nil
	}
	prefix, err := netmaskPrefixLength(netmask)
	if err != nil {
		return nil, err
	}
	network, err := networkAddressFor(gateway, prefix)
	if err != nil {
		return nil, err
	}
	return &cage.RouteInfo{
		Network:   network,
		Prefix:    prefix,
		Interface: interfaceName,
	}, nil
}

// Installed returns true if the host route reaches the box network
// through the correct bridge interface.
func (helper routeHelper) Installed(
	ctx context.Context, gateway string,
) (bool, error) {
	route, err := helper.Describe(ctx, gateway)
	if err != nil || route == nil {
		return true, err
	}
	probe := probeAddressIn(route.Network)
	stdout, _, status, err := helper.driver.runCapturing(
		ctx, "route", "-n", "get", probe)
	if err != nil {
		return false, err
	}
	if status != 0 {
		return false, nil
	}
	return routedInterface(string(stdout)) == route.Interface, nil
}

// Command returns the shell command that would add the route, or
// empty if it cannot be determined.
func (helper routeHelper) Command(
	ctx context.Context, gateway string,
) (string, error) {
	route, err := helper.Describe(ctx, gateway)
	if err != nil || route == nil {
		return "", err
	}
	return routeAddCommand(route), nil
}

// Install adds the route via sudo. Returns an error if no interface
// carries the gateway.
func (helper routeHelper) Install(
	ctx context.Context, gateway string,
) error {
	route, err := helper.Describe(ctx, gateway)
	if err != nil {
		return err
	}
	if route == nil {
		return fmt.Errorf(
			"cannot work out the box network from %s; is a box "+
				"running?", gateway)
	}
	status, err := helper.driver.runner.Run(ctx, Command{
		Name: "sudo",
		Arguments: []string{
			"route", "-n", "add", "-net",
			fmt.Sprintf("%s/%d", route.Network, route.Prefix),
			"-interface", route.Interface,
		},
		InheritStreams: true,
	})
	if err != nil {
		return err
	}
	if status != 0 {
		return fmt.Errorf("adding the route to %s/%d failed",
			route.Network, route.Prefix)
	}
	return nil
}

// routeAddCommand formats the shell command that adds a route. Takes a
// RouteInfo and returns the command string.
func routeAddCommand(route *cage.RouteInfo) string {
	return fmt.Sprintf(
		"sudo route -n add -net %s/%d -interface %s",
		route.Network, route.Prefix, route.Interface)
}

// interfaceCarrying finds the interface that holds an IPv4 address.
// Takes ifconfig output and an address. Returns the interface name and
// hex netmask, or two empty strings if no match.
func interfaceCarrying(
	output string, address string,
) (interfaceName string, netmask string) {
	current := ""
	for _, line := range strings.Split(output, "\n") {
		if line != "" && line[0] >= 'a' && line[0] <= 'z' {
			current = strings.TrimSuffix(strings.Fields(line)[0], ":")
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 4 &&
			fields[0] == "inet" && fields[1] == address {
			return current, fields[3]
		}
	}
	return "", ""
}

// routedInterface reads the interface name from `route -n get` output.
// Returns an empty string if the output has no interface line.
func routedInterface(output string) string {
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "interface:" {
			return fields[1]
		}
	}
	return ""
}

// probeAddressIn returns an address inside the given network, used for
// route lookups. Takes a network address and returns a probe address.
func probeAddressIn(network string) string {
	if index := strings.LastIndex(network, "."); index >= 0 {
		return network[:index+1] + "2"
	}
	return network
}

// netmaskPrefixLength converts a hex netmask like `0xffffff00` into a
// prefix length. Takes the mask string and returns the length or an
// error.
func netmaskPrefixLength(netmask string) (int, error) {
	digits := strings.TrimPrefix(
		strings.TrimPrefix(netmask, "0x"), "0X")
	value, err := strconv.ParseUint(digits, 16, 32)
	if err != nil {
		return 0, fmt.Errorf("unreadable netmask %s: %w", netmask, err)
	}
	return bits.OnesCount32(uint32(value)), nil
}

// networkAddressFor returns the network address for an IPv4 address at
// the given prefix length. Takes an address and prefix, returns the
// network string or an error.
func networkAddressFor(address string, prefix int) (string, error) {
	octets := strings.Split(address, ".")
	if len(octets) != 4 || prefix < 0 || prefix > 32 {
		return "", fmt.Errorf("cannot mask %s to a /%d", address, prefix)
	}
	masked := make([]string, 4)
	for index, text := range octets {
		value, err := strconv.Atoi(text)
		if err != nil || value < 0 || value > 255 {
			return "", fmt.Errorf("unreadable address %s", address)
		}
		kept := prefix - index*8
		switch {
		case kept >= 8:
			masked[index] = text
		case kept <= 0:
			masked[index] = "0"
		default:
			masked[index] = strconv.Itoa(
				value & (0xff << (8 - kept)) & 0xff)
		}
	}
	return strings.Join(masked, "."), nil
}
