package state

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func openProject(t *testing.T, root *Root, name string) *Project {
	t.Helper()
	directory := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", directory, err)
	}
	project, err := root.Project(directory)
	if err != nil {
		t.Fatalf("Root.Project(%s): %v", directory, err)
	}
	return project
}

func TestBoxNamePrefersTheRecordedPath(t *testing.T) {
	root := newTestRoot(t)
	project := openProject(t, root, "Working Copy")

	want := "working-copy-" + project.ID + ".prison.test"
	if got := project.BoxName("prison.test"); got != want {
		t.Errorf("BoxName = %q, want %q", got, want)
	}

	if err := project.SaveRecord(&ProjectRecord{Path: "/src/Other_Name"}); err != nil {
		t.Fatalf("SaveRecord: %v", err)
	}
	want = "other-name-" + project.ID + ".prison.test"
	if got := project.BoxName("prison.test"); got != want {
		t.Errorf("BoxName = %q, want %q", got, want)
	}

	byID, err := root.ProjectByID(project.ID)
	if err != nil {
		t.Fatalf("ProjectByID: %v", err)
	}
	if got := byID.BoxName("prison.test"); got != want {
		t.Errorf("BoxName = %q, want %q", got, want)
	}
}

func TestBoxNameFallsBackWhenNothingSlugs(t *testing.T) {
	root := newTestRoot(t)
	project := openProject(t, root, "app")
	if err := project.SaveRecord(&ProjectRecord{Path: "/src/___"}); err != nil {
		t.Fatalf("SaveRecord: %v", err)
	}
	want := "project-" + project.ID + ".prison.test"
	if got := project.BoxName("prison.test"); got != want {
		t.Errorf("BoxName = %q, want %q", got, want)
	}
}

func TestTokenIsCreatedOnceAtMode600(t *testing.T) {
	root := newTestRoot(t)
	project := openProject(t, root, "app")

	if _, found, err := project.TokenIfPresent(); err != nil || found {
		t.Fatalf("TokenIfPresent = %v, %v, want false, nil", found, err)
	}
	token, err := project.Token()
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if len(token) != 64 {
		t.Fatalf("len(Token) = %d, want 64", len(token))
	}
	path := filepath.Join(project.Dir, "token")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("token mode = %o, want 600", mode)
	}
	again, err := project.Token()
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if again != token {
		t.Error("Token = a new token on the second call, want the same token")
	}
	present, found, err := project.TokenIfPresent()
	if err != nil || !found || present != token {
		t.Errorf("TokenIfPresent = %q, %v, %v, want the token, true, nil", present, found, err)
	}
	if err := os.WriteFile(path, []byte("  \n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, found, err := project.TokenIfPresent(); err != nil || found {
		t.Errorf("TokenIfPresent = true for a blank file, want false")
	}
}

func TestTokenTableLooksUpEveryProject(t *testing.T) {
	root := newTestRoot(t)
	first := openProject(t, root, "one")
	second := openProject(t, root, "two")
	openProject(t, root, "three")

	firstToken, err := first.Token()
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	secondToken, err := second.Token()
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	table, err := root.TokenTable()
	if err != nil {
		t.Fatalf("TokenTable: %v", err)
	}
	if table.Len() != 2 {
		t.Errorf("Len = %d, want 2", table.Len())
	}
	if id, ok := table.Lookup(firstToken); !ok || id != first.ID {
		t.Errorf("Lookup = %q, %v, want %q, true", id, ok, first.ID)
	}
	if id, ok := table.Lookup(secondToken); !ok || id != second.ID {
		t.Errorf("Lookup = %q, %v, want %q, true", id, ok, second.ID)
	}
	if _, ok := table.Lookup(strings.Repeat("0", 64)); ok {
		t.Error("Lookup(unknown token) = true, want false")
	}
	if _, ok := table.Lookup(""); ok {
		t.Error("Lookup(\"\") = true, want false")
	}
	if _, ok := table.Lookup(firstToken[:32]); ok {
		t.Error("Lookup(token prefix) = true, want false")
	}
}

func TestGrantsRoundTripSkipsCommentsAndBlanks(t *testing.T) {
	root := newTestRoot(t)
	project := openProject(t, root, "app")

	names, err := project.Grants()
	if err != nil || names != nil {
		t.Fatalf("Grants = %v, %v, want nil, nil", names, err)
	}
	written := "# what this project may use\n\nANTHROPIC_API_KEY\n  GITHUB_TOKEN  \n\n# and no more\n"
	if err := os.WriteFile(project.grantsFile(), []byte(written), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	names, err = project.Grants()
	if err != nil {
		t.Fatalf("Grants: %v", err)
	}
	want := []string{"ANTHROPIC_API_KEY", "GITHUB_TOKEN"}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("Grants = %v, want %v", names, want)
	}

	if err := project.SetGrants(want); err != nil {
		t.Fatalf("SetGrants: %v", err)
	}
	info, err := os.Stat(project.grantsFile())
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("grants mode = %o, want 600", mode)
	}
	data, err := os.ReadFile(project.grantsFile())
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(data) != "ANTHROPIC_API_KEY\nGITHUB_TOKEN\n" {
		t.Errorf("grants = %q, want two lines", data)
	}
	names, err = project.Grants()
	if err != nil || !reflect.DeepEqual(names, want) {
		t.Errorf("Grants = %v, %v, want %v, nil", names, err, want)
	}
	if err := project.SetGrants(nil); err != nil {
		t.Fatalf("SetGrants(nil): %v", err)
	}
	if names, err := project.Grants(); err != nil || names != nil {
		t.Errorf("Grants = %v, %v, want nil, nil", names, err)
	}
}

func TestProjectEgressAllow(t *testing.T) {
	root := newTestRoot(t)
	project := openProject(t, root, "app")

	patterns, err := project.EgressAllow()
	if err != nil || patterns != nil {
		t.Fatalf("EgressAllow = %v, %v, want nil, nil", patterns, err)
	}
	content := "internal.test:8080 # the staging api\n\n*.example.com\n"
	if err := os.WriteFile(project.EgressAllowFile(), []byte(content), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	patterns, err = project.EgressAllow()
	if err != nil {
		t.Fatalf("EgressAllow: %v", err)
	}
	if len(patterns) != 2 || !patterns[0].MatchesPort(8080) || !patterns[1].Wildcard {
		t.Fatalf("EgressAllow = %v, want two patterns", patterns)
	}
}

func TestProfileRoundTrip(t *testing.T) {
	root := newTestRoot(t)
	project := openProject(t, root, "app")

	profile, err := project.Profile()
	if err != nil || profile != nil {
		t.Fatalf("Profile = %v, %v, want nil, nil", profile, err)
	}
	written := &Profile{
		Inmates: []InmateRoute{{
			Name:       "claude",
			Upstream:   "api.anthropic.com",
			PathPrefix: "/v1",
			Credentials: []CredentialSpec{{
				Variable: "ANTHROPIC_API_KEY",
				Header:   "x-api-key",
			}},
		}},
		Egress:  []string{"example.com"},
		Written: time.Now().Round(time.Second),
	}
	if err := project.SaveProfile(written); err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}
	data, err := os.ReadFile(project.ProfileFile())
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(data), `"path_prefix"`) {
		t.Errorf("profile.json = %s, want snake_case keys", data)
	}
	read, err := project.Profile()
	if err != nil {
		t.Fatalf("Profile: %v", err)
	}
	if len(read.Inmates) != 1 || read.Inmates[0].Credentials[0].Header != "x-api-key" {
		t.Errorf("Profile = %+v, want the saved profile", read)
	}
	if !reflect.DeepEqual(read.Egress, written.Egress) {
		t.Errorf("Egress = %v, want %v", read.Egress, written.Egress)
	}
}

func TestProjectPathsAndShadowFlattening(t *testing.T) {
	root := newTestRoot(t)
	project := openProject(t, root, "app")

	cases := map[string]string{
		project.RecordFile():        "project.json",
		project.ProfileFile():       "profile.json",
		project.EgressAllowFile():   "egress-allow",
		project.TrustedConfigCopy(): "config-trusted.toml",
	}
	for path, want := range cases {
		if filepath.Dir(path) != project.Dir || filepath.Base(path) != want {
			t.Errorf("path = %s, want %s in the state directory", path, want)
		}
	}
	if got, want := project.HomeDir("claude"), filepath.Join(project.HomesDir(), "claude"); got != want {
		t.Errorf("HomeDir = %s, want %s", got, want)
	}
	if got, want := project.ShadowDir("api/node_modules"), filepath.Join(project.Dir, "shadow", "api_node_modules"); got != want {
		t.Errorf("ShadowDir = %s, want %s", got, want)
	}
	if got, want := project.ShadowDir(".venv"), filepath.Join(project.Dir, "shadow", ".venv"); got != want {
		t.Errorf("ShadowDir = %s, want %s", got, want)
	}
}

func TestModTimesReportsOnlyThePresentFiles(t *testing.T) {
	root := newTestRoot(t)
	project := openProject(t, root, "app")

	times, err := project.ModTimes()
	if err != nil {
		t.Fatalf("ModTimes: %v", err)
	}
	if len(times) != 0 {
		t.Fatalf("ModTimes = %v, want empty", times)
	}
	if _, err := project.Token(); err != nil {
		t.Fatalf("Token: %v", err)
	}
	if err := project.SetGrants([]string{"ANTHROPIC_API_KEY"}); err != nil {
		t.Fatalf("SetGrants: %v", err)
	}
	if err := project.SaveProfile(&Profile{}); err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}
	times, err = project.ModTimes()
	if err != nil {
		t.Fatalf("ModTimes: %v", err)
	}
	for _, name := range []string{"token", "grants", "profile.json"} {
		if times[name].IsZero() {
			t.Errorf("ModTimes[%s] = zero, want a time", name)
		}
	}
	if _, ok := times["egress-allow"]; ok {
		t.Error("ModTimes has egress-allow, want no entry")
	}

	later := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(project.grantsFile(), later, later); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	changed, err := project.ModTimes()
	if err != nil {
		t.Fatalf("ModTimes: %v", err)
	}
	if !changed["grants"].After(times["grants"]) {
		t.Error("ModTimes[grants] = old time, want the new time")
	}
}

func TestSizeAndRemove(t *testing.T) {
	root := newTestRoot(t)
	project := openProject(t, root, "app")

	empty, err := project.Size()
	if err != nil {
		t.Fatalf("Size: %v", err)
	}
	if empty != 0 {
		t.Errorf("Size = %d, want 0", empty)
	}
	payload := []byte(strings.Repeat("x", 1024))
	if err := os.WriteFile(filepath.Join(project.CheckpointsDir(), "one"), payload, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := project.SetGrants([]string{"A"}); err != nil {
		t.Fatalf("SetGrants: %v", err)
	}
	size, err := project.Size()
	if err != nil {
		t.Fatalf("Size: %v", err)
	}
	if size != int64(len(payload)+2) {
		t.Errorf("Size = %d, want %d", size, len(payload)+2)
	}

	if err := project.Remove(); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Stat(project.Dir); !os.IsNotExist(err) {
		t.Error("Remove left the state directory")
	}
	stray := &Project{ID: "not-an-id", Dir: t.TempDir(), root: root}
	if err := stray.Remove(); err == nil {
		t.Error("Remove(not a project) error = nil, want an error")
	}
}
