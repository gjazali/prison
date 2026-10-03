package session

import (
	"testing"

	"prison/internal/plugin"
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
