package session

import (
	"testing"

	"prison/internal/cage"
)

// TestRefuseSudoOnOpenNetwork checks that sudo is refused on a
// non-host-only network, allowed on a host-only one, and ignored
// when sudo is not requested.
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
				t.Fatalf("err = %v, wantErr = %v", err, tt.wantErr)
			}
		})
	}
}
