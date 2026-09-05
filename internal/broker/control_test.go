package broker

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"prison/internal/broker/control"
	"prison/internal/broker/protocol"
	"prison/internal/state"
	"prison/internal/vault"
)

// TestControlAPI exercises the control endpoints: status, project
// update, environment, vault unlock, log, and shutdown.
func TestControlAPI(t *testing.T) {
	h := newHarness(t, nil)
	ctx := context.Background()

	status, err := h.client.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status.Version != "test" || status.StateRoot != h.root.Path || status.PID != os.Getpid() ||
		status.Vault != control.VaultAbsent || len(status.Projects) != 1 ||
		status.Projects[0] != h.project.ID || len(status.Listeners) != 1 || !status.Listeners[0].Bound {
		t.Fatalf("status = %+v", status)
	}
	if pid, ok := h.root.BrokerPID(); !ok || pid != os.Getpid() {
		t.Fatalf("pid file = %d, %v", pid, ok)
	}

	api := h.tunnelClient(protocol.BrokerHost)
	hostsBefore := readLines(t, api, protocol.PathHosts)
	if containsLine(hostsBefore, "example.org") {
		t.Fatal("example.org allowed before the profile named it")
	}
	err = h.client.UpdateProject(ctx, h.project.ID, control.ProjectUpdate{
		Profile: &state.Profile{Egress: []string{"example.org"}, Written: time.Now()},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !containsLine(readLines(t, api, protocol.PathHosts), "example.org") {
		t.Fatal("the profile update did not reload the allowlist")
	}
	if saved, err := h.project.Profile(); err != nil || saved == nil || len(saved.Egress) != 1 {
		t.Fatalf("profile.json = %+v, %v", saved, err)
	}
	if err := h.client.UpdateProject(ctx, "nothex", control.ProjectUpdate{}); err == nil {
		t.Fatal("a bad project id was accepted")
	}

	h.createVault(
		routeSecret("api", "api.example.com", vault.RouteSpec{BaseURLVariable: "API_URL", TokenVariable: "API_KEY", TokenPlaceholder: "sk_test_placeholder"}),
		routeSecret("clash", "api.example.com", vault.RouteSpec{BaseURLVariable: "SHARED"}),
		&vault.Secret{Name: "tok", Mode: vault.ModeExpose, Value: "exposed-value",
			Expose: &vault.ExposeSpec{Variable: "SHARED"}},
		&vault.Secret{Name: "hidden", Mode: vault.ModeExpose, Value: "not granted",
			Expose: &vault.ExposeSpec{Variable: "HIDDEN"}},
	)
	h.grant("api", "clash", "tok")
	status, _ = h.client.Status(ctx)
	if status.Vault != control.VaultLocked {
		t.Fatalf("vault = %q, want locked", status.Vault)
	}
	if lines, err := h.client.ProjectEnvironment(ctx, h.project.ID); err != nil || len(lines) != 0 {
		t.Fatalf("environment while locked = %v, %v", lines, err)
	}

	err = h.client.Unlock(ctx, "wrong")
	var controlError *control.Error
	if !errors.As(err, &controlError) || controlError.Status != http.StatusBadRequest ||
		!strings.Contains(controlError.Message, "wrong passphrase") {
		t.Fatalf("Unlock(wrong) = %v", err)
	}
	h.unlock()
	status, _ = h.client.Status(ctx)
	if status.Vault != control.VaultUnlocked {
		t.Fatalf("vault = %q, want unlocked", status.Vault)
	}
	want := []string{
		"API_KEY=sk_test_placeholder",
		"API_URL=http://" + protocol.RouteHost("api"),
		"SHARED=exposed-value",
	}
	lines, err := h.client.ProjectEnvironment(ctx, h.project.ID)
	if err != nil || strings.Join(lines, "|") != strings.Join(want, "|") {
		t.Fatalf("environment = %v, %v; want %v", lines, err, want)
	}
	if got := readLines(t, api, protocol.PathEnvironment); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("/v1/environment = %v, want %v", got, want)
	}
	if _, err := h.client.ProjectEnvironment(ctx, "0123456789ab"); err == nil {
		t.Fatal("an unknown project had an environment")
	}

	entries, err := h.client.Log(ctx, control.LogQuery{Kind: "sign", Limit: 5})
	if err != nil || entries == nil {
		t.Fatalf("Log = %v, %v", entries, err)
	}

	if err := h.client.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-h.runDone:
		if err != nil {
			t.Fatalf("Run returned %v", err)
		}
		h.runDone <- nil
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after shutdown")
	}
	if _, err := os.Lstat(h.root.BrokerSocket()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("socket after shutdown: %v", err)
	}
	if _, ok := h.root.BrokerPID(); ok {
		t.Fatal("pid file survived shutdown")
	}
	if h.client.Alive(ctx) {
		t.Fatal("the broker still answers after shutdown")
	}
}

// TestPollerPicksUpChanges checks that file changes on disk are
// detected without a control reload call.
func TestPollerPicksUpChanges(t *testing.T) {
	h := newHarness(t, func(h *harness, options *Options) {
		h.setEgressAllow("first.example")
	})
	if err := os.WriteFile(h.project.EgressAllowFile(), []byte("second.example\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(3 * time.Second)
	os.Chtimes(h.project.EgressAllowFile(), future, future)
	api := h.tunnelClient(protocol.BrokerHost)
	waitFor(t, 6*time.Second, "the poller to reload egress-allow", func() bool {
		return containsLine(readLines(t, api, protocol.PathHosts), "second.example")
	})
}

// TestEnsureRunning checks the start-or-reuse logic: stale sockets
// are removed, matching versions are reused, and version mismatches
// trigger a restart.
func TestEnsureRunning(t *testing.T) {
	directory := shortTempDir(t)
	root, err := state.OpenRoot(filepath.Join(directory, "root"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(root.BrokerSocket(), []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runDone := make(chan error, 4)
	spawns := 0
	spawnVersion := func(version string) func() error {
		return func() error {
			spawns++
			options := Options{Root: root, Version: version, Confirmer: newRecordingConfirmer()}
			go func() { runDone <- Run(ctx, options) }()
			return nil
		}
	}
	client, err := control.EnsureRunning(ctx, root, "v1", spawnVersion("v1"))
	if err != nil {
		t.Fatal(err)
	}
	if spawns != 1 {
		t.Fatalf("spawns = %d", spawns)
	}
	if _, err := control.EnsureRunning(ctx, root, "v1", spawnVersion("v1")); err != nil || spawns != 1 {
		t.Fatalf("second EnsureRunning: err %v, spawns %d", err, spawns)
	}
	replacement, err := control.EnsureRunning(ctx, root, "v2", spawnVersion("v2"))
	if err != nil {
		t.Fatal(err)
	}
	if spawns != 2 {
		t.Fatalf("spawns = %d, want 2", spawns)
	}
	status, err := replacement.Status(ctx)
	if err != nil || status.Version != "v2" {
		t.Fatalf("status = %+v, %v", status, err)
	}
	_ = client
	cancel()
	for i := 0; i < 2; i++ {
		select {
		case <-runDone:
		case <-time.After(10 * time.Second):
			t.Fatal("a broker did not stop")
		}
	}
}

// newRecordingConfirmer returns a confirmer that approves everything.
func newRecordingConfirmer() *recordingConfirmer {
	return &recordingConfirmer{deny: map[string]bool{}}
}

// readLines fetches a text endpoint and splits the body into lines.
func readLines(t *testing.T, api *http.Client, path string) []string {
	t.Helper()
	response, err := api.Get("http://x" + path)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatalf("%s: status %d", path, response.StatusCode)
	}
	body, _ := io.ReadAll(response.Body)
	text := strings.TrimSuffix(string(body), "\n")
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}

// containsLine reports whether the slice contains the exact string.
func containsLine(lines []string, wanted string) bool {
	for _, line := range lines {
		if line == wanted {
			return true
		}
	}
	return false
}
