//go:build linux

package guest

import (
	"fmt"
	"net"
	"net/netip"
	"os"
	"strconv"
	"time"
)

// sentinelNetwork is the IP range that nat redirects to the tunnel.
const sentinelNetwork = "127.99.0.0/16"

// egressProbeTimeout is the timeout for the egress verification dial.
const egressProbeTimeout = 3 * time.Second

// defaultGateway returns the IPv4 default gateway address. It tries
// `ip route show default` first, then /proc/net/route. Returns an
// error if no default route exists.
func defaultGateway(runner *commandRunner) (netip.Addr, error) {
	if output, err := runner.run("ip", "route", "show", "default"); err == nil {
		if address, ok := parseIPRouteDefault(output); ok {
			return address, nil
		}
	}
	content, err := os.ReadFile("/proc/net/route")
	if err != nil {
		return netip.Addr{}, err
	}
	return parseProcNetRoute(string(content))
}

// installFirewall replaces the box's iptables rules with the
// confinement set. It takes a runner, the gateway address, and the
// broker port. Returns an error on the first failing rule.
func installFirewall(runner *commandRunner, gateway netip.Addr,
	brokerPort int) error {
	gatewayText := gateway.String()
	rulesV4 := [][]string{
		{"-F"},
		{"-X"},
		{"-t", "nat", "-F"},
		{"-P", "INPUT", "DROP"},
		{"-P", "FORWARD", "DROP"},
		{"-P", "OUTPUT", "DROP"},
		{"-A", "INPUT", "-i", "lo", "-j", "ACCEPT"},
		{"-A", "OUTPUT", "-o", "lo", "-j", "ACCEPT"},
		{"-A", "INPUT", "-m", "conntrack", "--ctstate",
			"ESTABLISHED,RELATED", "-j", "ACCEPT"},
		{"-A", "OUTPUT", "-m", "conntrack", "--ctstate",
			"ESTABLISHED,RELATED", "-j", "ACCEPT"},
		{"-A", "INPUT", "-s", gatewayText, "-j", "ACCEPT"},
		{"-A", "OUTPUT", "-d", gatewayText, "-p", "udp", "--dport", "67",
			"-j", "ACCEPT"},
		{"-A", "OUTPUT", "-d", gatewayText, "-p", "tcp", "--dport",
			strconv.Itoa(brokerPort), "-j", "ACCEPT"},
		{"-t", "nat", "-A", "OUTPUT", "-d", sentinelNetwork, "-p", "tcp",
			"-j", "REDIRECT", "--to-ports", strconv.Itoa(tunnelPort)},
		{"-A", "OUTPUT", "-p", "tcp", "-j", "REJECT", "--reject-with",
			"tcp-reset"},
		{"-A", "OUTPUT", "-j", "REJECT", "--reject-with",
			"icmp-port-unreachable"},
	}
	for _, rule := range rulesV4 {
		if _, err := runner.run("iptables", rule...); err != nil {
			return err
		}
	}
	rulesV6 := [][]string{
		{"-F"},
		{"-X"},
		{"-P", "INPUT", "DROP"},
		{"-P", "FORWARD", "DROP"},
		{"-P", "OUTPUT", "DROP"},
		{"-A", "INPUT", "-i", "lo", "-j", "ACCEPT"},
		{"-A", "OUTPUT", "-o", "lo", "-j", "ACCEPT"},
		{"-A", "INPUT", "-m", "conntrack", "--ctstate",
			"ESTABLISHED,RELATED", "-j", "ACCEPT"},
		{"-A", "OUTPUT", "-m", "conntrack", "--ctstate",
			"ESTABLISHED,RELATED", "-j", "ACCEPT"},
		{"-A", "INPUT", "-p", "ipv6-icmp", "-j", "ACCEPT"},
		{"-A", "OUTPUT", "-p", "ipv6-icmp", "-j", "ACCEPT"},
		{"-A", "OUTPUT", "-p", "tcp", "-j", "REJECT", "--reject-with",
			"tcp-reset"},
		{"-A", "OUTPUT", "-j", "REJECT", "--reject-with",
			"icmp6-port-unreachable"},
	}
	for _, rule := range rulesV6 {
		if _, err := runner.run("ip6tables", rule...); err != nil {
			return err
		}
	}
	return nil
}

// proveEgressClosed checks that direct egress is blocked. Returns an
// error if the connection succeeds.
func proveEgressClosed() error {
	conn, err := net.DialTimeout("tcp4", "1.1.1.1:443", egressProbeTimeout)
	if err != nil {
		return nil
	}
	conn.Close()
	return fmt.Errorf("egress is still open after applying the rules")
}
