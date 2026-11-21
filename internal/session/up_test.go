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

type fakeRecreateIsolator struct {
	isolator.Isolator
	exists    bool
	running   bool
	started   int
	stopped   int
	deleted   int
	created   int
	lastImage string
}

func (f *fakeRecreateIsolator) Box(ctx context.Context, name string) (isolator.BoxInfo, error) {
	return isolator.BoxInfo{Name: name, Exists: f.exists, Running: f.running}, nil
}

func (f *fakeRecreateIsolator) Start(ctx context.Context, name string) error {
	f.started++
	f.running = true
	return nil
}

func (f *fakeRecreateIsolator) Stop(ctx context.Context, name string) error {
	f.stopped++
	f.running = false
	return nil
}

func (f *fakeRecreateIsolator) Delete(ctx context.Context, name string) error {
	f.deleted++
	f.exists = false
	return nil
}

func (f *fakeRecreateIsolator) Create(ctx context.Context, spec isolator.CreateSpec) error {
	f.created++
	f.exists = true
	f.running = true
	f.lastImage = spec.Image
	return nil
}

func TestCreateOrStartBoxRecreate(t *testing.T) {
	root, err := state.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	projectDir := t.TempDir()
	project, err := root.Project(projectDir)
	if err != nil {
		t.Fatal(err)
	}
	record := &state.ProjectRecord{
		Path:           projectDir,
		SetupSignature: "old-sig",
		Box:            &state.BoxShape{Image: "prison/box:old"},
	}
	if err := project.SaveRecord(record); err != nil {
		t.Fatal(err)
	}
	fake := &fakeRecreateIsolator{exists: true, running: true}
	s := &Session{
		Environment: &Environment{
			Root:      root,
			Isolator:  fake,
			Overrides: &config.Overrides{Network: "prison", BrokerPort: 8787},
		},
		Directory: projectDir,
		Project:   project,
		Record:    record,
		BoxName:   "proj-1234567890ab.prison",
	}

	created, err := s.createOrStartBox(
		context.Background(), record, "prison/box:new", nil, nil, false, true)
	if err != nil {
		t.Fatalf("createOrStartBox(recreate): %v", err)
	}
	if !created {
		t.Fatal("created = false, want true")
	}
	if fake.stopped != 1 || fake.deleted != 1 || fake.created != 1 {
		t.Fatalf("calls: stopped=%d deleted=%d created=%d",
			fake.stopped, fake.deleted, fake.created)
	}
	if fake.lastImage != "prison/box:new" {
		t.Fatalf("created image = %q, want prison/box:new", fake.lastImage)
	}
	if record.SetupSignature != "" {
		t.Fatalf("SetupSignature = %q, want cleared", record.SetupSignature)
	}
	if record.Box == nil || record.Box.Image != "prison/box:new" {
		t.Fatalf("record.Box = %+v, want prison/box:new", record.Box)
	}
}

func TestMiseDirectoryIsPersistedAndMounted(t *testing.T) {
	root, err := state.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	projectDir := t.TempDir()
	project, err := root.Project(projectDir)
	if err != nil {
		t.Fatal(err)
	}
	s := &Session{
		Environment: &Environment{Root: root},
		Directory:   projectDir,
		Project:     project,
	}

	if err := s.prepareDirectories(nil); err != nil {
		t.Fatalf("prepareDirectories: %v", err)
	}
	if info, err := os.Stat(project.MiseDir()); err != nil || !info.IsDir() {
		t.Fatalf("MiseDir %s not created: %v", project.MiseDir(), err)
	}
	if !slices.Contains(s.PersistPaths(), MiseGuestDir) {
		t.Fatalf("PersistPaths() = %v, want %s", s.PersistPaths(), MiseGuestDir)
	}
	mounted := false
	for _, mount := range s.Mounts(nil) {
		if mount.Target == MiseGuestDir && mount.Source == project.MiseDir() && !mount.ReadOnly {
			mounted = true
		}
	}
	if !mounted {
		t.Fatalf("Mounts() = %+v, missing writable mount for %s", s.Mounts(nil), MiseGuestDir)
	}
}

func TestCreateOrStartBoxDriftPromptsInteractively(t *testing.T) {
	newSession := func(t *testing.T, running bool) (*Session, *state.ProjectRecord, *fakeRecreateIsolator) {
		t.Helper()
		root, err := state.OpenRoot(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		projectDir := t.TempDir()
		project, err := root.Project(projectDir)
		if err != nil {
			t.Fatal(err)
		}
		record := &state.ProjectRecord{
			Path:           projectDir,
			SetupSignature: "old-sig",
			Box:            &state.BoxShape{Image: "prison/box:old", Network: "prison"},
		}
		if err := project.SaveRecord(record); err != nil {
			t.Fatal(err)
		}
		fake := &fakeRecreateIsolator{exists: true, running: running}
		s := &Session{
			Environment: &Environment{
				Root:      root,
				Isolator:  fake,
				Overrides: &config.Overrides{Network: "prison", BrokerPort: 8787},
			},
			Directory: projectDir,
			Project:   project,
			Record:    record,
			BoxName:   "proj-1234567890ab.prison",
		}
		return s, record, fake
	}

	previousTerminal := stdinIsTerminal
	previousConfirm := confirmPrompt
	t.Cleanup(func() {
		stdinIsTerminal = previousTerminal
		confirmPrompt = previousConfirm
	})

	t.Run("interactive confirm recreates without starting old box", func(t *testing.T) {
		stdinIsTerminal = func() bool { return true }
		prompted := 0
		confirmPrompt = func(question string) bool {
			prompted++
			return true
		}
		s, record, fake := newSession(t, false)
		created, err := s.createOrStartBox(
			context.Background(), record, "prison/box:new", nil, nil, false, false)
		if err != nil || !created {
			t.Fatalf("createOrStartBox = %v, %v, want true, nil", created, err)
		}
		if prompted != 1 {
			t.Errorf("prompted = %d, want 1", prompted)
		}
		if fake.started != 0 || fake.deleted != 1 || fake.created != 1 {
			t.Errorf("calls: started=%d deleted=%d created=%d, want 0, 1, 1",
				fake.started, fake.deleted, fake.created)
		}
		if record.Box == nil || record.Box.Image != "prison/box:new" {
			t.Errorf("record.Box = %+v, want prison/box:new", record.Box)
		}
	})

	t.Run("interactive decline keeps existing box and starts it", func(t *testing.T) {
		stdinIsTerminal = func() bool { return true }
		prompted := 0
		confirmPrompt = func(question string) bool {
			prompted++
			return false
		}
		s, record, fake := newSession(t, false)
		created, err := s.createOrStartBox(
			context.Background(), record, "prison/box:new", nil, nil, false, false)
		if err != nil || created {
			t.Fatalf("createOrStartBox = %v, %v, want false, nil", created, err)
		}
		if prompted != 1 {
			t.Errorf("prompted = %d, want 1", prompted)
		}
		if fake.started != 1 || fake.deleted != 0 || fake.created != 0 {
			t.Errorf("calls: started=%d deleted=%d created=%d, want 1, 0, 0",
				fake.started, fake.deleted, fake.created)
		}
		if record.Box == nil || record.Box.Image != "prison/box:old" {
			t.Errorf("record.Box = %+v, want prison/box:old", record.Box)
		}
	})

	t.Run("non-interactive skips prompt and keeps existing box", func(t *testing.T) {
		stdinIsTerminal = func() bool { return false }
		prompted := 0
		confirmPrompt = func(question string) bool {
			prompted++
			return true
		}
		s, record, fake := newSession(t, true)
		created, err := s.createOrStartBox(
			context.Background(), record, "prison/box:new", nil, nil, false, false)
		if err != nil || created {
			t.Fatalf("createOrStartBox = %v, %v, want false, nil", created, err)
		}
		if prompted != 0 {
			t.Errorf("prompted = %d, want 0", prompted)
		}
		if fake.deleted != 0 || fake.created != 0 {
			t.Errorf("calls: deleted=%d created=%d, want 0, 0",
				fake.deleted, fake.created)
		}
	})
}
