package session

import (
	"testing"

	"prison/internal/cage"
	"prison/internal/plugin"
)

func TestRefuseSudoOnOpenNetwork(t *testing.T) {
	cases := []struct {
		name     string
		sudo     bool
		hostOnly bool
		wantErr  bool
	}{
		{"open network with sudo", true, false, true},
		{"host-only network with sudo", true, true, false},
		{"open network without sudo", false, false, false},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			session := &Session{}
			err := session.refuseSudoOnOpenNetwork(tt.sudo,
				cage.NetworkInfo{Name: "net", HostOnly: tt.hostOnly})
			if (err != nil) != tt.wantErr {
				t.Fatalf("refuseSudoOnOpenNetwork = %v, want error %v", err,
					tt.wantErr)
			}
		})
	}
}

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
