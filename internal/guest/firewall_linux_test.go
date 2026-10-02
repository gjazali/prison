//go:build linux

package guest

import (
	"net/netip"
	"strings"
	"testing"
)

func TestFirewallRulesV4(t *testing.T) {
	rules := firewallRulesV4(netip.MustParseAddr("192.168.198.1"), 8787,
		[]int{2049})
	for _, want := range []string{
		":OUTPUT DROP [0:0]",
		"-A INPUT -s 192.168.198.1 -j ACCEPT",
		"-A OUTPUT -d 192.168.198.1 -p tcp --dport 8787 -j ACCEPT",
		"-A OUTPUT -d 192.168.198.1 -p tcp --dport 2049 -j ACCEPT",
		"-A OUTPUT -d 127.99.0.0/16 -p tcp -j REDIRECT --to-ports 8790",
	} {
		if !strings.Contains(rules, want) {
			t.Errorf("firewallRulesV4() = %s, want %q", rules, want)
		}
	}
	if strings.Count(rules, "COMMIT") != 2 {
		t.Errorf("firewallRulesV4() = %s, want 2 COMMIT lines", rules)
	}
	reject := strings.Index(rules, "REJECT --reject-with tcp-reset")
	port := strings.Index(rules, "--dport 2049")
	if reject < port {
		t.Errorf("reject rule index = %d, want after %d", reject, port)
	}
}

func TestFirewallRulesV6AllowOnlyNeighborDiscovery(t *testing.T) {
	rules := firewallRulesV6()
	if strings.Contains(rules, "--dport") {
		t.Errorf("firewallRulesV6() = %s, want no --dport", rules)
	}
	if !strings.Contains(rules, "-A OUTPUT -p ipv6-icmp -j ACCEPT") {
		t.Errorf("firewallRulesV6() = %s, want ipv6-icmp accepted", rules)
	}
}
