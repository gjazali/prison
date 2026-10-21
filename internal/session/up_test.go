package session

import (
	"context"
	"runtime"
	"testing"

	"prison/internal/config"
	"prison/internal/isolator"
	"prison/internal/plugin"
	"prison/internal/state"
)

func TestInmateByNameAnswersToAliases(t *testing.T) {
	aliased := &plugin.Inmate{Name: "antigravity-cli"}
	aliased.Inmate.Aliases = []string{"agy"}
	named := &plugin.Inmate{Name: "agy"}
	session := &Session{Inmates: []*plugin.Inmate{aliased}}

	found, enabled := session.InmateByName("agy")
	if !enabled || found != aliased {
		t.Errorf("InmateByName(\"agy\") = %v, %v, want antigravity-cli, true",
			found, enabled)
	}
	if _, enabled := session.InmateByName("nothing"); enabled {
		t.Error("InmateByName(\"nothing\") = true, want false")
	}

	session.Inmates = []*plugin.Inmate{aliased, named}
	if found, _ := session.InmateByName("agy"); found != named {
		t.Errorf("InmateByName(\"agy\") = %v, want agy",
			found)
	}
}

func TestEnsureHostFirewallFailsWhenTheStatusIsUnknown(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("this host has no packet filter support")
	}
	// Port 0 makes `hostfw.Status` and `hostfw.Install` fail before sudo.
	session := &Session{Environment: &Environment{
		Overrides: &config.Overrides{BrokerPort: 0},
		Root:      &state.Root{Path: t.TempDir()},
	}}
	network := isolator.NetworkInfo{SubnetV4: "192.168.128.0/24"}
	if !session.hostFirewallIsMissing(network) {
		t.Error("hostFirewallIsMissing = false, want true")
	}
	if err := session.ensureHostFirewall(
		context.Background(), network); err == nil {
		t.Error("ensureHostFirewall = nil, want an error")
	}
}
