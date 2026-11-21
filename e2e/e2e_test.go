//go:build e2e

// Package e2e tests the compiled `prison` CLI end-to-end on macOS and Linux
// with a real microVM isolator, real broker, real host firewall, and real
// ssh-agent. It focuses on cross-component flows that unit and driver-only
// integration tests do not cover together.
package e2e

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

const (
	testNetworkName = "prison-e2e"
	testDomain      = "prison"
	testPassphrase  = "prison-e2e-passphrase-2026"
)

// TestE2EBoxIsolationConfigAndLifecycle exercises the full CLI stack on a
// real microVM box: prison.toml trust & invalidation, [setup] &
// `prison setup --force`, `prison verify` (git hooks/config, host/in-box
// firewalls, shadow mounts), egress allowlist/blocking, loopback port
// publishing, lifecycle (`down`/`up`/`--recreate`), and `[box] sudo` toggle.
func TestE2EBoxIsolationConfigAndLifecycle(t *testing.T) {
	harness := newHarness(t, 18787, 41000, 41049)

	// Start a local HTTP server for deterministic egress testing.
	egressServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "egress-ok")
	}))
	t.Cleanup(egressServer.Close)
	_, egressPort, err := net.SplitHostPort(egressServer.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	loopbackHost := resolveLoopbackDomain(t)

	projectDir := harness.newProjectDir("core")
	initGitRepo(t, projectDir)
	writeTestFile(t, filepath.Join(projectDir, "app.txt"), "initial-v1\n")
	writeTestFile(t, filepath.Join(projectDir, "shadow_dir", "host_only.txt"), "host-secret\n")
	writeExecutable(t, filepath.Join(projectDir, ".git", "hooks", "pre-commit"), "#!/bin/sh\nexit 0\n")
	runHostCmd(t, projectDir, "git", "add", "app.txt")
	runHostCmd(t, projectDir, "git", "commit", "-q", "-m", "initial")

	configPath := filepath.Join(projectDir, "prison.toml")
	baseConfig := fmt.Sprintf(`[box]
cpus = 2
memory = "1G"
sudo = false
ports = [8123]
shadow = ["shadow_dir"]

[setup]
commands = [
  "echo setup_uid=$(id -u) > /tmp/setup-marker",
  "date +%%s%%N >> /tmp/setup-runs",
]

[egress]
hosts = ["%s:%s"]

[environment]
E2E_CUSTOM_VAR = "hello_prison_42"
`, loopbackHost, egressPort)
	writeTestFile(t, configPath, baseConfig)

	t.Cleanup(func() {
		_, _ = harness.run(projectDir, nil, "rm", "--state", "--yes")
	})

	t.Run("TrustAndUp", func(t *testing.T) {
		statusOut, _ := harness.run(projectDir, nil, "status")
		lower := strings.ToLower(statusOut)
		if !strings.Contains(lower, "untrusted") && !strings.Contains(lower, "not trusted") {
			t.Fatalf("status before trust = %q, want untrusted indicator", statusOut)
		}

		harness.mustRun(t, projectDir, nil, "trust", "--yes")
		harness.mustRun(t, projectDir, nil, "up")

		wantMarker := fmt.Sprintf("setup_uid=%d", os.Getuid())
		marker := strings.TrimSpace(harness.mustRun(t, projectDir, nil, "run", "cat /tmp/setup-marker"))
		if marker != wantMarker {
			t.Errorf("setup marker = %q, want %s", marker, wantMarker)
		}

		envVal := harness.mustRun(t, projectDir, nil, "run", `printf %s "$E2E_CUSTOM_VAR"`)
		if envVal != "hello_prison_42" {
			t.Errorf("E2E_CUSTOM_VAR = %q, want hello_prison_42", envVal)
		}

		if out, err := harness.run(projectDir, nil, "run", "sudo -n id"); err == nil {
			t.Errorf("sudo -n id succeeded with box.sudo=false: %s", out)
		}
	})

	t.Run("VerifyAndShadowIsolation", func(t *testing.T) {
		verifyOut := harness.mustRun(t, projectDir, nil, "verify")
		for _, want := range []string{
			"PASS  git hooks",
			"PASS  git config",
			"PASS  host filesystem",
			"PASS  direct egress",
			"PASS  broker",
			"PASS  host firewall",
			"PASS  in-box firewall",
			"PASS  published ports",
			"PASS  shadowed paths",
			"the box is isolated",
		} {
			if !strings.Contains(verifyOut, want) {
				t.Errorf("prison verify missing %q:\n%s", want, verifyOut)
			}
		}
		if strings.Contains(verifyOut, "FAIL") {
			t.Errorf("prison verify reported failure:\n%s", verifyOut)
		}

		harness.mustRun(t, projectDir, nil, "run", "echo guest-shadow > /workspace/shadow_dir/guest_only.txt")
		visible := strings.TrimSpace(harness.mustRun(t, projectDir, nil, "run",
			"test -f /workspace/shadow_dir/host_only.txt && echo VISIBLE || echo HIDDEN"))
		if visible != "HIDDEN" {
			t.Errorf("host_only.txt visibility inside box = %q, want HIDDEN", visible)
		}
		if _, err := os.Stat(filepath.Join(projectDir, "shadow_dir", "guest_only.txt")); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("guest_only.txt leaked into host shadow_dir (err=%v)", err)
		}
	})

	t.Run("EgressAndPortPublishing", func(t *testing.T) {
		allowedURL := fmt.Sprintf("http://%s:%s/", loopbackHost, egressPort)
		body := strings.TrimSpace(harness.mustRun(t, projectDir, nil, "run",
			fmt.Sprintf("curl -fsS --max-time 10 %s", allowedURL)))
		if body != "egress-ok" {
			t.Errorf("allowed egress body = %q, want egress-ok", body)
		}

		if out, err := harness.run(projectDir, nil, "run",
			"curl -fsS --max-time 5 https://example.org"); err == nil {
			t.Errorf("disallowed egress unexpectedly succeeded: %s", out)
		}

		if out, err := harness.run(projectDir, nil, "run",
			`curl -fsS --noproxy '*' --max-time 3 https://1.1.1.1`); err == nil {
			t.Errorf("direct internet bypass unexpectedly succeeded: %s", out)
		}

		// Start a lightweight responder inside the box on guest port 8123.
		portsOut := harness.mustRun(t, projectDir, nil, "ports")
		urlMatch := regexp.MustCompile(`http://127\.0\.0\.1:\d+`).FindString(portsOut)
		if urlMatch == "" {
			t.Fatalf("could not parse published port URL from:\n%s", portsOut)
		}
		harness.mustRun(t, projectDir, nil, "run",
			`perl -MIO::Socket::INET -e 'exit if fork; my $s=IO::Socket::INET->new(LocalAddr=>"0.0.0.0",LocalPort=>8123,Listen=>5,ReuseAddr=>1) or die $!; for(1..3){ my $c=$s->accept or last; <$c>; print $c "HTTP/1.1 200 OK\r\nContent-Length: 14\r\nConnection: close\r\n\r\nprison-port-ok"; close $c; }'`)

		var gotBody string
		var lastErr error
		client := &http.Client{Timeout: 3 * time.Second}
		for range 10 {
			resp, err := client.Get(urlMatch)
			if err == nil {
				raw, readErr := io.ReadAll(resp.Body)
				resp.Body.Close()
				if readErr == nil && resp.StatusCode == http.StatusOK {
					gotBody = string(raw)
					lastErr = nil
					break
				}
				lastErr = fmt.Errorf("status=%d err=%v", resp.StatusCode, readErr)
			} else {
				lastErr = err
			}
			time.Sleep(250 * time.Millisecond)
		}
		if lastErr != nil || gotBody != "prison-port-ok" {
			t.Errorf("published port %s -> body=%q err=%v, want prison-port-ok", urlMatch, gotBody, lastErr)
		}
	})

	t.Run("LifecycleSetupAndSudoRecreate", func(t *testing.T) {
		harness.mustRun(t, projectDir, nil, "down")
		if statusOut := harness.mustRun(t, projectDir, nil, "status"); !strings.Contains(strings.ToLower(statusOut), "stopped") {
			t.Errorf("status after down = %q, want stopped", statusOut)
		}

		harness.mustRun(t, projectDir, nil, "up")
		runs := strings.TrimSpace(harness.mustRun(t, projectDir, nil, "run", "wc -l < /tmp/setup-runs"))
		if runs != "1" {
			t.Errorf("setup runs after resume = %q, want 1", runs)
		}

		harness.mustRun(t, projectDir, nil, "setup", "--force")
		runs = strings.TrimSpace(harness.mustRun(t, projectDir, nil, "run", "wc -l < /tmp/setup-runs"))
		if runs != "2" {
			t.Errorf("setup runs after --force = %q, want 2", runs)
		}

		// Flip sudo = true, confirm trust invalidation, re-trust, recreate, verify sudo works.
		writeTestFile(t, configPath, strings.Replace(baseConfig, "sudo = false", "sudo = true", 1))
		driftOut, _ := harness.run(projectDir, nil, "status")
		lower := strings.ToLower(driftOut)
		if !strings.Contains(lower, "untrusted") && !strings.Contains(lower, "not trusted") && !strings.Contains(lower, "changed") {
			t.Errorf("status after editing prison.toml = %q, want trust invalidation", driftOut)
		}

		harness.mustRun(t, projectDir, nil, "trust", "--yes")
		harness.mustRun(t, projectDir, nil, "up", "--recreate")
		sudoUID := strings.TrimSpace(harness.mustRun(t, projectDir, nil, "run", "sudo id -u"))
		if sudoUID != "0" {
			t.Errorf("sudo id -u after recreate with sudo=true = %q, want 0", sudoUID)
		}
	})
}

// TestE2ESecretsEndToEnd exercises all secret modes (`expose`, `route`,
// `route --intercept`, `sign` via ssh-agent), live revocation, and audit logs
// through a real microVM and real broker against local HTTPS httptest servers.
func TestE2ESecretsEndToEnd(t *testing.T) {
	loopbackHost := resolveLoopbackDomain(t)

	// Start a local HTTPS echo server with a freshly generated test CA trusted
	// by the isolated broker via SSL_CERT_FILE.
	echoServer, caBundlePath := startLocalTLSEchoServer(t, []string{"localhost", loopbackHost})
	_, echoPort, err := net.SplitHostPort(echoServer.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}

	// Start an in-process ssh-agent with an ephemeral Ed25519 key.
	agentSocket, keyFingerprint, authorizedPubKey := startTestSSHAgent(t)

	harness := newHarness(t, 18788, 41050, 41099)
	harness.extraEnv = append(harness.extraEnv,
		"SSL_CERT_FILE="+caBundlePath,
		"SSH_AUTH_SOCK="+agentSocket,
	)

	projectDir := harness.newProjectDir("secrets")
	initGitRepo(t, projectDir)
	writeTestFile(t, filepath.Join(projectDir, "README.md"), "sign test\n")

	t.Cleanup(func() {
		_, _ = harness.run(projectDir, nil, "rm", "--state", "--yes")
	})

	// 1. Initialize encrypted vault and add secrets for every mode.
	harness.mustRunSecret(t, projectDir, "", "secret", "init")

	harness.mustRunSecret(t, projectDir, "route-secret-777",
		"secret", "add", "test-route",
		"--mode", "route",
		"--upstream", "localhost:"+echoPort,
		"--path-prefix", "/headers",
		"--header", "X-Prison-Route",
		"--header-prefix", "Bearer ",
		"--base-url-variable", "ROUTE_API_BASE",
		"--token-variable", "ROUTE_API_TOKEN",
		"--token-placeholder", "dummy_placeholder_000",
	)

	harness.mustRunSecret(t, projectDir, "intercept-secret-888",
		"secret", "add", "test-intercept",
		"--mode", "route",
		"--upstream", loopbackHost+":"+echoPort,
		"--path-prefix", "/headers",
		"--header", "X-Prison-Intercept",
		"--intercept",
	)

	harness.mustRunSecret(t, projectDir, "exposed-secret-999",
		"secret", "add", "test-expose",
		"--mode", "expose",
		"--variable", "TEST_EXPOSED_VAR",
	)

	harness.mustRunSecret(t, projectDir, "",
		"secret", "add", "test-sign",
		"--mode", "sign",
		"--algorithm", "ssh-agent",
		"--fingerprint", keyFingerprint,
	)

	for _, name := range []string{"test-route", "test-intercept", "test-expose", "test-sign"} {
		harness.mustRunSecret(t, projectDir, "", "secret", "grant", name)
	}

	// Boot once to start the broker, unlock vault, then restart so the guest
	// fetches the intercept CA bundle and /v1/hosts on boot.
	harness.mustRun(t, projectDir, nil, "up")
	harness.mustRunSecret(t, projectDir, "", "secret", "unlock")
	harness.mustRun(t, projectDir, nil, "down")
	harness.mustRun(t, projectDir, nil, "up")

	t.Run("ExposeMode", func(t *testing.T) {
		got := harness.mustRun(t, projectDir, nil, "run", `printf %s "$TEST_EXPOSED_VAR"`)
		if got != "exposed-secret-999" {
			t.Errorf("TEST_EXPOSED_VAR = %q, want exposed-secret-999", got)
		}
	})

	t.Run("RouteMode", func(t *testing.T) {
		token := harness.mustRun(t, projectDir, nil, "run", `printf %s "$ROUTE_API_TOKEN"`)
		if token != "dummy_placeholder_000" {
			t.Errorf("ROUTE_API_TOKEN = %q, want dummy_placeholder_000", token)
		}

		raw := harness.mustRun(t, projectDir, nil, "run",
			`curl -fsS --max-time 15 -H "Authorization: Bearer attacker-fake" "$ROUTE_API_BASE/headers"`)
		echo := parseEchoPayload(t, raw)
		if got := echo.Headers.Get("X-Prison-Route"); got != "Bearer route-secret-777" {
			t.Errorf("X-Prison-Route = %q, want %q", got, "Bearer route-secret-777")
		}
		if auth := echo.Headers.Get("Authorization"); auth != "" {
			t.Errorf("Authorization was not stripped: %q", auth)
		}
	})

	t.Run("RouteInterceptAndPathPrefix", func(t *testing.T) {
		raw := harness.mustRun(t, projectDir, nil, "run",
			fmt.Sprintf("curl -fsS --max-time 15 https://%s:%s/headers", loopbackHost, echoPort))
		echo := parseEchoPayload(t, raw)
		if got := echo.Headers.Get("X-Prison-Intercept"); got != "intercept-secret-888" {
			t.Errorf("X-Prison-Intercept = %q, want intercept-secret-888", got)
		}

		status := strings.TrimSpace(harness.mustRun(t, projectDir, nil, "run",
			fmt.Sprintf(`curl -s -o /dev/null -w "%%{http_code}" --max-time 10 https://%s:%s/other`, loopbackHost, echoPort)))
		if status != "404" {
			t.Errorf("intercepted request outside --path-prefix returned HTTP %s, want 404", status)
		}
	})

	t.Run("SignModeSSHAgent", func(t *testing.T) {
		signScript := strings.Join([]string{
			"set -eu",
			`PUB=$(/home/dev/.prison/bin/prison-ssh-sign --print-public-key)`,
			`git config --global user.name "Prison Signer"`,
			`git config --global user.email "signer@example.com"`,
			`git config --global gpg.format ssh`,
			`git config --global gpg.ssh.program /home/dev/.prison/bin/prison-ssh-sign`,
			`git config --global user.signingkey "$PUB"`,
			`git add README.md`,
			`git commit -S -m "signed inside prison"`,
		}, "; ")
		harness.mustRun(t, projectDir, nil, "run", signScript)

		allowedSigners := filepath.Join(harness.rootDir, "allowed_signers")
		writeTestFile(t, allowedSigners, fmt.Sprintf("signer@example.com %s\n", authorizedPubKey))
		out, err := runHostCmdErr(projectDir, "git", "-c", "gpg.ssh.allowedSignersFile="+allowedSigners,
			"verify-commit", "HEAD")
		if err != nil {
			t.Fatalf("git verify-commit HEAD failed: %v\n%s", err, out)
		}
	})

	t.Run("RevokeAndAuditLog", func(t *testing.T) {
		harness.mustRun(t, projectDir, nil, "secret", "revoke", "test-intercept")
		out, err := harness.run(projectDir, nil, "run",
			fmt.Sprintf(`curl -fsS --max-time 10 https://%s:%s/headers`, loopbackHost, echoPort))
		if err == nil {
			t.Errorf("revoked intercept secret still succeeded: %s", out)
		}

		logOut := harness.mustRun(t, projectDir, nil, "secret", "log")
		for _, want := range []string{"test-route", "test-intercept", "test-sign"} {
			if !strings.Contains(logOut, want) {
				t.Errorf("prison secret log missing %q:\n%s", want, logOut)
			}
		}
	})
}

type e2eHarness struct {
	t          *testing.T
	binary     string
	rootDir    string
	workDir    string
	brokerPort int
	portBase   int
	portLimit  int
	extraEnv   []string
}

func newHarness(t *testing.T, brokerPort, portBase, portLimit int) *e2eHarness {
	t.Helper()
	requirePlatformPrereqs(t)

	binary := resolvePrisonBinary(t)
	workDir, err := os.MkdirTemp(tempBaseDir(), "prison-e2e-")
	if err != nil {
		t.Fatal(err)
	}
	rootDir := filepath.Join(workDir, "root")
	if err := os.MkdirAll(rootDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Preserve any pre-existing host firewall state so running e2e tests on a
	// machine with an active Prison installation restores it cleanly.
	restoreFW := saveHostFirewall()

	h := &e2eHarness{
		t:          t,
		binary:     binary,
		rootDir:    rootDir,
		workDir:    workDir,
		brokerPort: brokerPort,
		portBase:   portBase,
		portLimit:  portLimit,
	}

	t.Cleanup(func() {
		_, _ = h.run(workDir, nil, "broker", "stop")
		cleanupTestNetwork(testNetworkName)
		restoreFW()
		_ = os.RemoveAll(workDir)
	})

	return h
}

func (h *e2eHarness) newProjectDir(name string) string {
	h.t.Helper()
	dir := filepath.Join(h.workDir, "proj-"+name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		h.t.Fatal(err)
	}
	return dir
}

func (h *e2eHarness) env() []string {
	base := append([]string{}, os.Environ()...)
	base = append(base,
		"PRISON_ROOT="+h.rootDir,
		"PRISON_NETWORK="+testNetworkName,
		"PRISON_DOMAIN="+testDomain,
		"PRISON_BROKER_PORT="+strconv.Itoa(h.brokerPort),
		"PRISON_PORT_BASE="+strconv.Itoa(h.portBase),
		"PRISON_PORT_LIMIT="+strconv.Itoa(h.portLimit),
	)
	return append(base, h.extraEnv...)
}

func (h *e2eHarness) run(dir string, stdin io.Reader, args ...string) (string, error) {
	cmd := exec.Command(h.binary, args...)
	cmd.Dir = dir
	cmd.Env = h.env()
	if stdin != nil {
		cmd.Stdin = stdin
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (h *e2eHarness) mustRun(t *testing.T, dir string, stdin io.Reader, args ...string) string {
	t.Helper()
	out, err := h.run(dir, stdin, args...)
	if err != nil {
		t.Fatalf("prison %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

// mustRunSecret runs a `prison secret ...` command supplying the vault
// passphrase via `--passphrase-fd 3` and optional secret value on stdin.
func (h *e2eHarness) mustRunSecret(t *testing.T, dir, stdinValue string, args ...string) string {
	t.Helper()
	passReader, passWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(passWriter, testPassphrase+"\n"); err != nil {
		passReader.Close()
		passWriter.Close()
		t.Fatal(err)
	}
	passWriter.Close()
	defer passReader.Close()

	fullArgs := append([]string{}, args...)
	fullArgs = append(fullArgs, "--passphrase-fd", "3")

	cmd := exec.Command(h.binary, fullArgs...)
	cmd.Dir = dir
	cmd.Env = h.env()
	cmd.ExtraFiles = []*os.File{passReader}
	if stdinValue != "" {
		cmd.Stdin = strings.NewReader(stdinValue)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("prison %s failed: %v\n%s", strings.Join(fullArgs, " "), err, string(out))
	}
	return string(out)
}

func resolvePrisonBinary(t *testing.T) string {
	t.Helper()
	if override := os.Getenv("PRISON_BINARY"); override != "" {
		abs, err := filepath.Abs(override)
		if err != nil {
			t.Fatal(err)
		}
		return abs
	}
	candidate, err := filepath.Abs(filepath.Join("..", "bin", "prison"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(candidate); err != nil {
		skipOutsideCI(t, fmt.Sprintf("%s not found; run `make build` or set PRISON_BINARY", candidate))
	}
	return candidate
}

func skipOutsideCI(t *testing.T, reason string) {
	t.Helper()
	if os.Getenv("CI") != "" {
		t.Fatal(reason)
	}
	t.Skip(reason)
}

func resolveLoopbackDomain(t *testing.T) string {
	t.Helper()
	for _, host := range []string{"127.0.0.1.nip.io", "127-0-0-1.sslip.io"} {
		addrs, err := net.LookupHost(host)
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			if addr == "127.0.0.1" {
				return host
			}
		}
	}
	t.Skip("no DNS name resolving to 127.0.0.1 is available for loopback intercept test")
	return ""
}

type echoPayload struct {
	Path    string      `json:"path"`
	Headers http.Header `json:"headers"`
}

func parseEchoPayload(t *testing.T, raw string) echoPayload {
	t.Helper()
	var payload echoPayload
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatalf("invalid echo JSON %q: %v", raw, err)
	}
	return payload
}

// startLocalTLSEchoServer starts a local HTTPS server signed by a freshly
// minted CA and returns the server plus a CA bundle file path suitable for
// SSL_CERT_FILE.
func startLocalTLSEchoServer(t *testing.T, dnsNames []string) (*httptest.Server, string) {
	t.Helper()

	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "prison-e2e-test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}

	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leafTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: dnsNames[0]},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     dnsNames,
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTemplate, caCert, &leafKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}

	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(echoPayload{
			Path:    r.URL.Path,
			Headers: r.Header,
		})
	}))
	server.TLS = &tls.Config{
		Certificates: []tls.Certificate{{
			Certificate: [][]byte{leafDER, caDER},
			PrivateKey:  leafKey,
		}},
	}
	server.StartTLS()
	t.Cleanup(server.Close)

	var bundle bytes.Buffer
	if systemCAs, err := os.ReadFile(systemCABundlePath()); err == nil {
		bundle.Write(systemCAs)
		bundle.WriteByte('\n')
	}
	_ = pem.Encode(&bundle, &pem.Block{Type: "CERTIFICATE", Bytes: caDER})

	bundlePath := filepath.Join(t.TempDir(), "ca-bundle.pem")
	writeTestFile(t, bundlePath, bundle.String())
	return server, bundlePath
}

// startTestSSHAgent serves an in-process ssh-agent over a Unix socket with a
// fresh Ed25519 key and returns (socketPath, sha256Fingerprint, authorizedKey).
func startTestSSHAgent(t *testing.T) (string, string, string) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}

	keyring := agent.NewKeyring()
	if err := keyring.Add(agent.AddedKey{
		PrivateKey: priv,
		Comment:    "prison-e2e-sign",
	}); err != nil {
		t.Fatal(err)
	}

	socketPath := filepath.Join(t.TempDir(), "agent.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(socketPath, 0o600); err != nil {
		listener.Close()
		t.Fatal(err)
	}

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				_ = agent.ServeAgent(keyring, c)
			}(conn)
		}
	}()
	t.Cleanup(func() { listener.Close() })

	return socketPath, ssh.FingerprintSHA256(sshPub), strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub)))
}

func initGitRepo(t *testing.T, dir string) {
	t.Helper()
	runHostCmd(t, dir, "git", "init", "-q")
	runHostCmd(t, dir, "git", "config", "user.name", "Prison E2E")
	runHostCmd(t, dir, "git", "config", "user.email", "e2e@example.com")
}

func runHostCmd(t *testing.T, dir, name string, args ...string) string {
	t.Helper()
	out, err := runHostCmdErr(dir, name, args...)
	if err != nil {
		t.Fatalf("%s %s failed: %v\n%s", name, strings.Join(args, " "), err, out)
	}
	return out
}

func runHostCmdErr(dir, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeExecutable(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}

func readOptionalFile(path string) ([]byte, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	return data, true
}
