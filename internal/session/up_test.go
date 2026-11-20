package session

import (
	"context"
	"io"
	"os"
	"runtime"
	"slices"
	"strings"
	"testing"

	"prison/internal/config"
	"prison/internal/isolator"
	"prison/internal/plugin"
	"prison/internal/state"
	"prison/internal/vault"
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

func TestInBoxLoginProfileEnvironmentAndWarning(t *testing.T) {
	root, err := state.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	projectDir := t.TempDir()
	project, err := root.Project(projectDir)
	if err != nil {
		t.Fatal(err)
	}
	inmate := &plugin.Inmate{
		Name: "antigravity-cli",
		Manifest: plugin.Manifest{
			Auth: &plugin.AuthTable{
				Upstream:        "generativelanguage.googleapis.com",
				PathPrefix:      "/",
				BaseURLVariable: "GOOGLE_GEMINI_BASE_URL",
				TokenVariable:   "GEMINI_API_KEY",
				Credentials: []plugin.Credential{{
					Variable: "GEMINI_API_KEY",
					Header:   "x-goog-api-key",
				}},
				InBox: &plugin.InBoxAuthTable{
					Hosts: []string{"accounts.google.com", "oauth2.googleapis.com"},
				},
			},
			Egress: plugin.EgressTable{
				Hosts: []string{"generativelanguage.googleapis.com"},
			},
		},
	}
	s := &Session{
		Environment: &Environment{Root: root, HomeDir: t.TempDir()},
		Project:     project,
		Inmates:     []*plugin.Inmate{inmate},
	}

	// Brokered mode is the default.
	if !s.hasAuthInmates() {
		t.Fatal("hasAuthInmates() = false, want true")
	}
	brokerProfile := s.Profile()
	if len(brokerProfile.Inmates) != 1 {
		t.Fatalf("broker profile inmates = %+v, want 1", brokerProfile.Inmates)
	}
	if slices.Contains(brokerProfile.Egress, "accounts.google.com") {
		t.Fatalf("broker egress unexpectedly includes in-box host: %v", brokerProfile.Egress)
	}

	if err := project.SetInmateAuthProfile(inmate.Name, vault.InBoxInmateProfile); err != nil {
		t.Fatal(err)
	}
	if s.hasAuthInmates() {
		t.Fatal("hasAuthInmates() = true in in-box mode, want false")
	}
	inBoxProfile := s.Profile()
	if len(inBoxProfile.Inmates) != 0 {
		t.Fatalf("in-box profile inmates = %+v, want none", inBoxProfile.Inmates)
	}
	if !slices.Contains(inBoxProfile.Egress, "accounts.google.com") ||
		!slices.Contains(inBoxProfile.Egress, "oauth2.googleapis.com") ||
		!slices.Contains(inBoxProfile.Egress, "generativelanguage.googleapis.com") {
		t.Fatalf("in-box profile egress = %v", inBoxProfile.Egress)
	}

	env, err := s.ExecEnvironment(context.Background(), nil, "")
	if err != nil {
		t.Fatalf("ExecEnvironment: %v", err)
	}
	for _, entry := range env {
		if strings.HasPrefix(entry, "GOOGLE_GEMINI_BASE_URL=") ||
			strings.HasPrefix(entry, "GEMINI_API_KEY=") {
			t.Fatalf("ExecEnvironment leaked broker auth var %q in in-box mode", entry)
		}
	}

	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	previousStderr := os.Stderr
	os.Stderr = writer
	s.warnAboutInBoxLogin()
	writer.Close()
	os.Stderr = previousStderr
	warningBytes, err := io.ReadAll(reader)
	reader.Close()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(warningBytes), "antigravity-cli inmate uses in-box login") {
		t.Fatalf("warning output = %q", string(warningBytes))
	}
}
