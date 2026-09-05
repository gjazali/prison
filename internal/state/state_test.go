package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newTestRoot opens a state root in a temporary directory with HOME
// set to a separate temporary directory. It returns the root.
func newTestRoot(t *testing.T) *Root {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	root, err := OpenRoot(filepath.Join(t.TempDir(), "prison"))
	if err != nil {
		t.Fatalf("OpenRoot: %v", err)
	}
	return root
}

// realPath resolves symlinks in path. It returns the cleaned
// result.
func realPath(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("EvalSymlinks(%s): %v", path, err)
	}
	return filepath.Clean(resolved)
}

func TestProjectIDMatchesKnownHashes(t *testing.T) {
	cases := map[string]string{
		"/Users/you/src/app": "ab8c5e8d2f32",
		"/tmp/My Project":    "dab36f6a753b",
	}
	for path, want := range cases {
		if got := ProjectID(path); got != want {
			t.Errorf("ProjectID(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestSlugMatchesLegacyDerivation(t *testing.T) {
	cases := []struct{ path, want string }{
		{"/src/My_Project.v2", "my-project-v2"},
		{"/src/---weird---", "weird"},
		{"/src/" + strings.Repeat("a", 40) + "BBBB", strings.Repeat("a", 40)},
		{"/src/a" + strings.Repeat("-", 50), "a"},
		{"/src/plain", "plain"},
		{"/", ""},
	}
	for _, testCase := range cases {
		if got := Slug(testCase.path); got != testCase.want {
			t.Errorf("Slug(%q) = %q, want %q", testCase.path, got, testCase.want)
		}
	}
}

func TestIsProjectID(t *testing.T) {
	valid := []string{"ab8c5e8d2f32", "000000000000", "ffffffffffff"}
	invalid := []string{"", "AB8C5E8D2F32", "ab8c5e8d2f3", "ab8c5e8d2f320", "ab8c5e8d2f3g", "projects"}
	for _, id := range valid {
		if !IsProjectID(id) {
			t.Errorf("IsProjectID(%q) = false, want true", id)
		}
	}
	for _, id := range invalid {
		if IsProjectID(id) {
			t.Errorf("IsProjectID(%q) = true, want false", id)
		}
	}
}

func TestOpenRootCreatesTheLayout(t *testing.T) {
	root := newTestRoot(t)
	for _, directory := range []string{root.Path, root.ProjectsDir(), root.InmatesDir(), root.TrustDir()} {
		info, err := os.Stat(directory)
		if err != nil {
			t.Fatalf("stat %s: %v", directory, err)
		}
		if !info.IsDir() {
			t.Fatalf("%s is not a directory", directory)
		}
		if mode := info.Mode().Perm(); mode != 0o700 {
			t.Errorf("%s has mode %o, want 700", directory, mode)
		}
	}
	if _, err := OpenRoot(root.Path); err != nil {
		t.Fatalf("reopening an existing root: %v", err)
	}
}

func TestOpenRootNamesTheRootFiles(t *testing.T) {
	root := newTestRoot(t)
	cases := map[string]string{
		root.ConfigFile():      "config.toml",
		root.EgressAllowFile(): "egress-allow",
		root.VaultFile():       "secrets.vault",
		root.BrokerSocket():    "broker.sock",
		root.BrokerLog():       "broker.log",
		root.BrokerPIDFile():   "broker.pid",
	}
	for path, want := range cases {
		if filepath.Dir(path) != root.Path || filepath.Base(path) != want {
			t.Errorf("%s is not %s in the root", path, want)
		}
	}
}

func TestProjectRefusesHomeAndFilesystemRoot(t *testing.T) {
	root := newTestRoot(t)
	home := os.Getenv("HOME")
	if _, err := root.Project(home); err == nil {
		t.Fatal("Root.Project accepted the home directory")
	} else if !strings.Contains(err.Error(), "home directory") {
		t.Errorf("error does not mention the home directory: %v", err)
	}
	if _, err := root.Project("/"); err == nil {
		t.Fatal("Root.Project accepted /")
	}
}

func TestProjectCreatesItsSubdirectories(t *testing.T) {
	root := newTestRoot(t)
	directory := filepath.Join(t.TempDir(), "App Two")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	project, err := root.Project(directory)
	if err != nil {
		t.Fatalf("Root.Project: %v", err)
	}
	if want := ProjectID(realPath(t, directory)); project.ID != want {
		t.Errorf("project id is %q, want %q", project.ID, want)
	}
	if project.Dir != filepath.Join(root.ProjectsDir(), project.ID) {
		t.Errorf("state directory is %s", project.Dir)
	}
	for _, path := range []string{project.HomesDir(), project.EmptyDir(), project.CheckpointsDir(), filepath.Join(project.Dir, "shadow")} {
		if info, err := os.Stat(path); err != nil || !info.IsDir() {
			t.Errorf("%s was not created", path)
		}
	}
	if _, err := os.Stat(project.RecordFile()); !os.IsNotExist(err) {
		t.Error("Root.Project wrote project.json")
	}
}

func TestProjectByIDAndListProjects(t *testing.T) {
	root := newTestRoot(t)
	directory := filepath.Join(t.TempDir(), "app")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	project, err := root.Project(directory)
	if err != nil {
		t.Fatalf("Root.Project: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root.ProjectsDir(), "not-a-project"), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	reopened, err := root.ProjectByID(project.ID)
	if err != nil {
		t.Fatalf("ProjectByID: %v", err)
	}
	if reopened.Dir != project.Dir {
		t.Errorf("ProjectByID gave %s, want %s", reopened.Dir, project.Dir)
	}
	if _, err := root.ProjectByID("ab8c5e8d2f32"); err == nil {
		t.Error("ProjectByID accepted an id with no state directory")
	}
	if _, err := root.ProjectByID("nope"); err == nil {
		t.Error("ProjectByID accepted a malformed id")
	}
	projects, err := root.ListProjects()
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if len(projects) != 1 || projects[0].ID != project.ID {
		t.Fatalf("ListProjects gave %d projects, want only %s", len(projects), project.ID)
	}
}

func TestLegacyLayoutDetected(t *testing.T) {
	root := newTestRoot(t)
	if root.LegacyLayoutDetected() {
		t.Fatal("a fresh root looks like the legacy layout")
	}
	if err := os.MkdirAll(filepath.Join(root.Path, "ab8c5e8d2f32"), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if !root.LegacyLayoutDetected() {
		t.Error("a project id directory in the root was not detected")
	}

	other := newTestRoot(t)
	if err := os.WriteFile(filepath.Join(other.Path, "auth-routes.json"), []byte("{}"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if !other.LegacyLayoutDetected() {
		t.Error("auth-routes.json was not detected")
	}
}

func TestClaimedPortBlocks(t *testing.T) {
	root := newTestRoot(t)
	first := openProject(t, root, "one")
	second := openProject(t, root, "two")
	third := openProject(t, root, "three")

	if err := first.SaveRecord(&ProjectRecord{Path: "/src/one", PortBlock: 40000}); err != nil {
		t.Fatalf("SaveRecord: %v", err)
	}
	if err := second.SaveRecord(&ProjectRecord{Path: "/src/two"}); err != nil {
		t.Fatalf("SaveRecord: %v", err)
	}
	blocks, err := root.ClaimedPortBlocks()
	if err != nil {
		t.Fatalf("ClaimedPortBlocks: %v", err)
	}
	if len(blocks) != 1 || blocks[first.ID] != 40000 {
		t.Fatalf("ClaimedPortBlocks gave %v", blocks)
	}

	if err := os.WriteFile(third.RecordFile(), []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := root.ClaimedPortBlocks(); err == nil {
		t.Error("a corrupt project.json was reported as no claim")
	}
}

func TestFloorAllowList(t *testing.T) {
	root := newTestRoot(t)
	patterns, err := root.FloorAllowList()
	if err != nil || patterns != nil {
		t.Fatalf("an absent file gave %v, %v", patterns, err)
	}
	if err := os.WriteFile(root.EgressAllowFile(), []byte("# nothing here\n\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	patterns, err = root.FloorAllowList()
	if err != nil || patterns != nil {
		t.Fatalf("an empty file gave %v, %v", patterns, err)
	}
	if err := os.WriteFile(root.EgressAllowFile(), []byte("example.com\nhost.test:8443\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	patterns, err = root.FloorAllowList()
	if err != nil {
		t.Fatalf("FloorAllowList: %v", err)
	}
	if len(patterns) != 2 || patterns[0].Host != "example.com" || !patterns[1].MatchesPort(8443) {
		t.Fatalf("FloorAllowList gave %v", patterns)
	}
	if err := os.WriteFile(root.EgressAllowFile(), []byte("https://example.com\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := root.FloorAllowList(); err == nil {
		t.Error("a bad pattern was accepted")
	}
}

func TestBrokerPID(t *testing.T) {
	root := newTestRoot(t)
	if _, ok := root.BrokerPID(); ok {
		t.Fatal("an absent pid file reported a pid")
	}
	if err := root.SaveBrokerPID(4321); err != nil {
		t.Fatalf("SaveBrokerPID: %v", err)
	}
	pid, ok := root.BrokerPID()
	if !ok || pid != 4321 {
		t.Fatalf("BrokerPID gave %d, %v", pid, ok)
	}
	if err := os.WriteFile(root.BrokerPIDFile(), []byte("later\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, ok := root.BrokerPID(); ok {
		t.Error("a pid file holding text reported a pid")
	}
}

func TestTrustRecords(t *testing.T) {
	root := newTestRoot(t)
	record, err := root.TrustRecord("inmate", "claude")
	if err != nil || record != nil {
		t.Fatalf("an absent record gave %v, %v", record, err)
	}
	saved := &TrustRecord{Hash: "abc123", OriginURL: "https://example.com/x.tar.gz", OriginRef: "v1"}
	if err := root.SaveTrustRecord("inmate", "claude", saved); err != nil {
		t.Fatalf("SaveTrustRecord: %v", err)
	}
	path := filepath.Join(root.TrustDir(), "inmates", "claude.json")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("%s has mode %o, want 600", path, mode)
	}
	read, err := root.TrustRecord("inmate", "claude")
	if err != nil {
		t.Fatalf("TrustRecord: %v", err)
	}
	if *read != *saved {
		t.Errorf("TrustRecord gave %+v, want %+v", *read, *saved)
	}
	if err := root.DeleteTrustRecord("inmate", "claude"); err != nil {
		t.Fatalf("DeleteTrustRecord: %v", err)
	}
	if read, err := root.TrustRecord("inmate", "claude"); err != nil || read != nil {
		t.Fatalf("after deleting, TrustRecord gave %v, %v", read, err)
	}
	if err := root.DeleteTrustRecord("inmate", "claude"); err != nil {
		t.Errorf("deleting an absent record: %v", err)
	}
	if _, err := root.TrustRecord("inmate", "../escape"); err == nil {
		t.Error("a name with a separator was accepted")
	}
	if _, err := root.TrustRecord("", "claude"); err == nil {
		t.Error("an empty kind was accepted")
	}
}

func TestWriteFileAtomicReplacesAndSetsTheMode(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "record")
	if err := WriteFileAtomic(path, []byte("first"), 0o600); err != nil {
		t.Fatalf("WriteFileAtomic: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("mode is %o, want 600", mode)
	}
	if err := WriteFileAtomic(path, []byte("second"), 0o600); err != nil {
		t.Fatalf("WriteFileAtomic: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(data) != "second" {
		t.Errorf("content is %q, want %q", data, "second")
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("the directory holds %d files, want only the written one", len(entries))
	}
}

func TestNewTokenIsSixtyFourHexCharacters(t *testing.T) {
	first, err := NewToken()
	if err != nil {
		t.Fatalf("NewToken: %v", err)
	}
	if len(first) != 64 {
		t.Fatalf("token is %d characters, want 64", len(first))
	}
	for _, c := range first {
		if !strings.ContainsRune("0123456789abcdef", c) {
			t.Fatalf("token holds %q, which is not hex", c)
		}
	}
	second, err := NewToken()
	if err != nil {
		t.Fatalf("NewToken: %v", err)
	}
	if first == second {
		t.Error("two tokens came out the same")
	}
}

func TestRecordRoundTripKeepsTheJSONShape(t *testing.T) {
	root := newTestRoot(t)
	project := openProject(t, root, "app")
	created := time.Now().Round(time.Second)
	saved := &ProjectRecord{
		Path:                "/src/app",
		Created:             created,
		TrustedConfigSHA256: "deadbeef",
		PortBlock:           40000,
		SetupSignature:      "sig",
		Box: &BoxShape{
			Image:   "prison-base",
			CPUs:    4,
			Memory:  "8G",
			Network: "prison",
			Ports:   [][2]int{{40000, 3000}},
			Inmates: []string{"claude"},
		},
	}
	if err := project.SaveRecord(saved); err != nil {
		t.Fatalf("SaveRecord: %v", err)
	}
	data, err := os.ReadFile(project.RecordFile())
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(data), "\n  \"path\": \"/src/app\",") {
		t.Errorf("project.json is not indented by two spaces:\n%s", data)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{"path", "created", "trusted_config_sha256", "port_block", "setup_signature", "box"} {
		if _, ok := raw[key]; !ok {
			t.Errorf("project.json has no %q key", key)
		}
	}
	read, err := project.Record()
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	if read.Path != saved.Path || read.PortBlock != saved.PortBlock || !read.Created.Equal(created) {
		t.Errorf("Record gave %+v", read)
	}
	if read.Box == nil || read.Box.Ports[0] != [2]int{40000, 3000} {
		t.Errorf("the box shape did not survive: %+v", read.Box)
	}
}
