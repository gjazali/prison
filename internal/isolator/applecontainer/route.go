package applecontainer

import (
	"context"
	"fmt"
	"maps"
	"net"
	"net/netip"
	"slices"
	"strings"

	"prison/internal/isolator"
)

// routeHelper reads live host state because the bridge exists only while a
// box runs.
type routeHelper struct {
	driver *Driver
}

func (helper routeHelper) Describe(
	ctx context.Context, gateway string,
) (*isolator.RouteInfo, error) {
	if gateway == "" {
		return nil, nil
	}
	addressesByInterface, err := hostInterfaceAddresses()
	if err != nil {
		return nil, fmt.Errorf("cannot read the host interfaces: %w", err)
	}
	return routeCarrying(gateway, addressesByInterface), nil
}

var hostInterfaceAddresses = func() (map[string][]net.Addr, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	addressesByInterface := make(map[string][]net.Addr, len(interfaces))
	for _, hostInterface := range interfaces {
		addresses, err := hostInterface.Addrs()
		if err != nil {
			return nil, err
		}
		addressesByInterface[hostInterface.Name] = addresses
	}
	return addressesByInterface, nil
}

// routeCarrying matches IPv4 only because the route commands expect IPv4.
func routeCarrying(
	gateway string, addressesByInterface map[string][]net.Addr,
) *isolator.RouteInfo {
	gatewayAddress, err := netip.ParseAddr(gateway)
	if err != nil || !gatewayAddress.Is4() {
		return nil
	}
	names := slices.Sorted(maps.Keys(addressesByInterface))
	for _, interfaceName := range names {
		for _, address := range addressesByInterface[interfaceName] {
			network, ok := address.(*net.IPNet)
			if !ok {
				continue
			}
			ip, ok := netip.AddrFromSlice(network.IP)
			if !ok || ip.Unmap() != gatewayAddress {
				continue
			}
			prefix, _ := network.Mask.Size()
			return &isolator.RouteInfo{
				Network: netip.PrefixFrom(gatewayAddress, prefix).
					Masked().Addr().String(),
				Prefix:    prefix,
				Interface: interfaceName,
			}
		}
	}
	return nil
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

func routeAddCommand(route *isolator.RouteInfo) string {
	return fmt.Sprintf(
		"sudo route -n add -net %s/%d -interface %s",
		route.Network, route.Prefix, route.Interface)
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
