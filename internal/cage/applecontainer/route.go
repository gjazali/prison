package applecontainer

import (
	"context"
	"fmt"
	"math/bits"
	"strconv"
	"strings"

	"prison/internal/cage"
)

// routeHelper reads live host state because the bridge exists only while a
// box runs.
type routeHelper struct {
	driver *Driver
}

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
		return nil, fmt.Errorf("cannot read the host interfaces")
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

// Installed reports true when no interface carries the gateway, because
// then there is no route to repair.
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

func (helper routeHelper) Command(
	ctx context.Context, gateway string,
) (string, error) {
	route, err := helper.Describe(ctx, gateway)
	if err != nil || route == nil {
		return "", err
	}
	return routeAddCommand(route), nil
}

func (helper routeHelper) Install(
	ctx context.Context, gateway string,
) error {
	route, err := helper.Describe(ctx, gateway)
	if err != nil {
		return err
	}
	if route == nil {
		return fmt.Errorf("cannot find the box network for gateway %s. "+
			"Make sure that a box is running", gateway)
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
		return fmt.Errorf("cannot add the route to %s/%d",
			route.Network, route.Prefix)
	}
	return nil
}

func routeAddCommand(route *cage.RouteInfo) string {
	return fmt.Sprintf(
		"sudo route -n add -net %s/%d -interface %s",
		route.Network, route.Prefix, route.Interface)
}

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

func routedInterface(output string) string {
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "interface:" {
			return fields[1]
		}
	}
	return ""
}

func probeAddressIn(network string) string {
	if index := strings.LastIndex(network, "."); index >= 0 {
		return network[:index+1] + "2"
	}
	return network
}

func netmaskPrefixLength(netmask string) (int, error) {
	digits := strings.TrimPrefix(
		strings.TrimPrefix(netmask, "0x"), "0X")
	value, err := strconv.ParseUint(digits, 16, 32)
	if err != nil {
		return 0, fmt.Errorf("invalid netmask %s: %w", netmask, err)
	}
	return bits.OnesCount32(uint32(value)), nil
}

func networkAddressFor(address string, prefix int) (string, error) {
	octets := strings.Split(address, ".")
	if len(octets) != 4 || prefix < 0 || prefix > 32 {
		return "", fmt.Errorf("cannot mask %s to a /%d", address, prefix)
	}
	masked := make([]string, 4)
	for index, text := range octets {
		value, err := strconv.Atoi(text)
		if err != nil || value < 0 || value > 255 {
			return "", fmt.Errorf("invalid address %s", address)
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
