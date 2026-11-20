package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"prison/internal/broker/control"
	"prison/internal/plugin"
	"prison/internal/session"
	"prison/internal/state"
	"prison/internal/vault"
)

func TestResolveInmateCredentialVariable(t *testing.T) {
	single := &plugin.Inmate{
		Name: "opencode",
		Manifest: plugin.Manifest{
			Auth: &plugin.AuthTable{
				Credentials: []plugin.Credential{{Variable: "OPENCODE_API_KEY"}},
			},
		},
	}
	multi := &plugin.Inmate{
		Name: "claude-code",
		Manifest: plugin.Manifest{
			Auth: &plugin.AuthTable{
				Credentials: []plugin.Credential{
					{Variable: "ANTHROPIC_API_KEY"},
					{Variable: "CLAUDE_CODE_OAUTH_TOKEN"},
				},
			},
		},
	}

	if got, err := resolveInmateCredentialVariable(single, "", ""); err != nil || got != "OPENCODE_API_KEY" {
		t.Fatalf("single auto = %q, %v", got, err)
	}
	if got, err := resolveInmateCredentialVariable(multi, "CLAUDE_CODE_OAUTH_TOKEN", ""); err != nil || got != "CLAUDE_CODE_OAUTH_TOKEN" {
		t.Fatalf("multi explicit = %q, %v", got, err)
	}
	if got, err := resolveInmateCredentialVariable(multi, "", "CLAUDE_CODE_OAUTH_TOKEN"); err != nil || got != "CLAUDE_CODE_OAUTH_TOKEN" {
		t.Fatalf("multi from-env match = %q, %v", got, err)
	}
	if _, err := resolveInmateCredentialVariable(multi, "BOGUS", ""); err == nil {
		t.Fatal("invalid variable accepted")
	}
}

func TestDescribeBrokerInmateCredentials(t *testing.T) {
	got := describeBrokerInmateCredentials([]control.InmateCredential{
		{Inmate: "claude-code", Profile: "default", Variable: "CLAUDE_CODE_OAUTH_TOKEN"},
		{Inmate: "claude-code", Profile: "work", Variable: "ANTHROPIC_API_KEY"},
	})
	want := "claude-code (CLAUDE_CODE_OAUTH_TOKEN), claude-code/work (ANTHROPIC_API_KEY)"
	if got != want {
		t.Errorf("describeBrokerInmateCredentials = %q, want %q", got, want)
	}
}

func TestDescribeStatusInmates(t *testing.T) {
	root, err := state.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "proj")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	project, err := root.Project(dir)
	if err != nil {
		t.Fatal(err)
	}
	current := &session.Session{
		Project:     project,
		InmateNames: []string{"claude-code", "opencode"},
	}
	if got := describeStatusInmates(current); got != "claude-code opencode" {
		t.Fatalf("without override = %q", got)
	}
	if err := project.SetInmateAuthProfile("claude-code", "work"); err != nil {
		t.Fatal(err)
	}
	if got := describeStatusInmates(current); got != "claude-code (work) opencode" {
		t.Fatalf("with override = %q", got)
	}
}

func TestInmateAuthEndToEnd(t *testing.T) {
	rootDir := t.TempDir()
	projectDir := filepath.Join(t.TempDir(), "work-repo")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	root, err := state.OpenRoot(rootDir)
	if err != nil {
		t.Fatal(err)
	}
	created, err := vault.Create(root.VaultFile(), "secret-pass")
	if err != nil {
		t.Fatal(err)
	}
	created.Lock()

	t.Setenv("PRISON_ROOT", rootDir)
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "oauth-personal-value")
	t.Setenv("WORK_ANTHROPIC_KEY", "sk-ant-work-value")
	previousDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(projectDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previousDir) })

	runWithPassphrase := func(args ...string) (string, error) {
		t.Helper()
		reader, writer, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := writer.WriteString("secret-pass\n"); err != nil {
			t.Fatal(err)
		}
		writer.Close()
		duped, err := syscall.Dup(int(reader.Fd()))
		reader.Close()
		if err != nil {
			t.Fatal(err)
		}

		secretPassphraseDescriptor = -1
		secretPassphraseFromDescriptor.read = false
		secretPassphraseFromDescriptor.value = ""

		cmd := newRootCommand()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetArgs(append(args, "--passphrase-fd", strconv.Itoa(duped)))
		err = cmd.Execute()
		if !secretPassphraseFromDescriptor.read {
			_ = syscall.Close(duped)
		}
		return out.String(), err
	}

	output, err := runWithPassphrase("inmate", "auth", "set", "claude",
		"--from-environment", "CLAUDE_CODE_OAUTH_TOKEN")
	if err != nil {
		t.Fatalf("set default: %v", err)
	}
	if !strings.Contains(output, "saved claude-code credential (CLAUDE_CODE_OAUTH_TOKEN)") {
		t.Fatalf("set default output = %q", output)
	}

	output, err = runWithPassphrase("inmate", "auth", "set", "claude-code",
		"--profile", "work", "--variable", "ANTHROPIC_API_KEY",
		"--from-environment", "WORK_ANTHROPIC_KEY")
	if err != nil {
		t.Fatalf("set work: %v", err)
	}
	if !strings.Contains(output, "saved claude-code credential (ANTHROPIC_API_KEY, profile work)") {
		t.Fatalf("set work output = %q", output)
	}

	if _, err := runWithPassphrase("inmate", "auth", "use", "claude", "work"); err != nil {
		t.Fatalf("use work: %v", err)
	}
	project, err := root.Project(projectDir)
	if err != nil {
		t.Fatal(err)
	}
	profiles, err := project.InmateAuthProfiles()
	if err != nil || profiles["claude-code"] != "work" {
		t.Fatalf("InmateAuthProfiles = %v, %v", profiles, err)
	}

	if _, err := runWithPassphrase("inmate", "auth", "use", "claude", "nonexistent"); err == nil {
		t.Fatal("use nonexistent succeeded, want error")
	}

	output, err = runWithPassphrase("inmate", "auth", "list", "--json")
	if err != nil {
		t.Fatalf("list --json: %v", err)
	}
	if strings.Contains(output, "oauth-personal-value") || strings.Contains(output, "sk-ant-work-value") {
		t.Fatalf("list --json leaked a secret value: %s", output)
	}
	var listed []inmateCredentialJSON
	if err := json.Unmarshal([]byte(output), &listed); err != nil {
		t.Fatalf("list --json parse: %v (%s)", err, output)
	}
	if len(listed) != 2 || listed[0].Profile != "default" || listed[1].Profile != "work" {
		t.Fatalf("listed = %+v", listed)
	}

	clearCmd := newRootCommand()
	clearCmd.SetArgs([]string{"inmate", "auth", "clear", "claude"})
	if err := clearCmd.Execute(); err != nil {
		t.Fatalf("clear: %v", err)
	}
	profiles, err = project.InmateAuthProfiles()
	if err != nil || len(profiles) != 0 {
		t.Fatalf("after clear: %v, %v", profiles, err)
	}

	output, err = runWithPassphrase("inmate", "auth", "rm", "claude-code", "--profile", "work")
	if err != nil {
		t.Fatalf("rm work: %v", err)
	}
	if !strings.Contains(output, "removed claude-code credentials (profile work)") {
		t.Fatalf("rm work output = %q", output)
	}

	t.Setenv("OAUTH_CLIENT_SECRET", "top-secret-client")
	output, err = runWithPassphrase("inmate", "auth", "set", "claude-code",
		"--profile", "oauth",
		"--variable", "CLAUDE_CODE_OAUTH_TOKEN",
		"--from-environment", "CLAUDE_CODE_OAUTH_TOKEN",
		"--token-url", "https://oauth2.example/token",
		"--client-id", "my-client-id",
		"--client-secret-from-environment", "OAUTH_CLIENT_SECRET")
	if err != nil {
		t.Fatalf("set oauth: %v", err)
	}
	output, err = runWithPassphrase("inmate", "auth", "list", "--json")
	if err != nil {
		t.Fatalf("list oauth --json: %v", err)
	}
	if strings.Contains(output, "top-secret-client") || strings.Contains(output, "oauth-personal-value") {
		t.Fatalf("list --json leaked secrets: %s", output)
	}
	var oauthListed []inmateCredentialJSON
	if err := json.Unmarshal([]byte(output), &oauthListed); err != nil {
		t.Fatalf("list oauth --json parse: %v", err)
	}
	foundOAuth := false
	for _, item := range oauthListed {
		if item.Profile == "oauth" && item.OAuth && item.TokenURL == "https://oauth2.example/token" {
			foundOAuth = true
		}
	}
	if !foundOAuth {
		t.Fatalf("oauth entry missing in %+v", oauthListed)
	}
}
