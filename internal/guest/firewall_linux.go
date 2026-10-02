//go:build linux

package guest

import (
	"fmt"
	"net"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"time"
)

const sentinelNetwork = "127.99.0.0/16"

const egressProbeTimeout = 3 * time.Second

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

// firewallRulesFile holds the rules for one restore. A restore replaces
// each table in one step, so a partial rule set is never active.
const firewallRulesFile = "/run/prison-firewall"

func installFirewall(runner *commandRunner, gateway netip.Addr,
	brokerPort int, gatewayPorts []int) error {
	restores := []struct{ program, rules string }{
		{"iptables-restore",
			firewallRulesV4(gateway, brokerPort, gatewayPorts)},
		{"ip6tables-restore", firewallRulesV6()},
	}
	for _, restore := range restores {
		err := os.WriteFile(firewallRulesFile, []byte(restore.rules), 0o600)
		if err != nil {
			return err
		}
		_, err = runner.run(restore.program, firewallRulesFile)
		os.Remove(firewallRulesFile)
		if err != nil {
			return err
		}
	}
	return nil
}

func firewallRulesV4(gateway netip.Addr, brokerPort int,
	gatewayPorts []int) string {
	gatewayText := gateway.String()
	lines := []string{
		"*filter",
		":INPUT DROP [0:0]",
		":FORWARD DROP [0:0]",
		":OUTPUT DROP [0:0]",
		"-A INPUT -i lo -j ACCEPT",
		"-A OUTPUT -o lo -j ACCEPT",
		"-A INPUT -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT",
		"-A OUTPUT -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT",
		"-A INPUT -s " + gatewayText + " -j ACCEPT",
		"-A OUTPUT -d " + gatewayText + " -p udp --dport 67 -j ACCEPT",
		"-A OUTPUT -d " + gatewayText + " -p tcp --dport " +
			strconv.Itoa(brokerPort) + " -j ACCEPT",
	}
	for _, port := range gatewayPorts {
		lines = append(lines, "-A OUTPUT -d "+gatewayText+
			" -p tcp --dport "+strconv.Itoa(port)+" -j ACCEPT")
	}
	lines = append(lines,
		"-A OUTPUT -p tcp -j REJECT --reject-with tcp-reset",
		"-A OUTPUT -j REJECT --reject-with icmp-port-unreachable",
		"COMMIT",
		"*nat",
		":PREROUTING ACCEPT [0:0]",
		":INPUT ACCEPT [0:0]",
		":OUTPUT ACCEPT [0:0]",
		":POSTROUTING ACCEPT [0:0]",
		"-A OUTPUT -d "+sentinelNetwork+" -p tcp -j REDIRECT --to-ports "+
			strconv.Itoa(tunnelPort),
		"COMMIT",
	)
	return strings.Join(lines, "\n") + "\n"
}

func firewallRulesV6() string {
	return strings.Join([]string{
		"*filter",
		":INPUT DROP [0:0]",
		":FORWARD DROP [0:0]",
		":OUTPUT DROP [0:0]",
		"-A INPUT -i lo -j ACCEPT",
		"-A OUTPUT -o lo -j ACCEPT",
		"-A INPUT -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT",
		"-A OUTPUT -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT",
		"-A INPUT -p ipv6-icmp -j ACCEPT",
		"-A OUTPUT -p ipv6-icmp -j ACCEPT",
		"-A OUTPUT -p tcp -j REJECT --reject-with tcp-reset",
		"-A OUTPUT -j REJECT --reject-with icmp6-port-unreachable",
		"COMMIT",
	}, "\n") + "\n"
}

func proveEgressClosed() error {
	conn, err := net.DialTimeout("tcp4", "1.1.1.1:443", egressProbeTimeout)
	if err != nil {
		return nil
	}
	conn.Close()
	return fmt.Errorf("egress is open after the firewall rules are applied")
}
