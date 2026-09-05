package guest

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"prison/internal/broker/protocol"
)

// TestParseSSHSignArguments checks argument parsing for the forms
// git and a person use.
func TestParseSSHSignArguments(t *testing.T) {
	cases := []struct {
		in   []string
		want sshSignOptions
	}{
		{
			in:   []string{"--print-public-key"},
			want: sshSignOptions{printPublicKey: true, namespace: "git"},
		},
		{
			in: []string{"-Y", "sign", "-n", "git", "-f", "/k.pub",
				"/tmp/commit"},
			want: sshSignOptions{mode: "sign", namespace: "git",
				keyFile: "/k.pub", target: "/tmp/commit"},
		},
		{
			in: []string{"-Y", "sign", "-q", "-O", "-n", "file", "-f",
				"/k.pub"},
			want: sshSignOptions{mode: "sign", namespace: "file",
				keyFile: "/k.pub"},
		},
		{
			in:   []string{"-Y", "verify", "-f", "/allowed", "-n", "git"},
			want: sshSignOptions{mode: "verify", namespace: "git", keyFile: "/allowed"},
		},
		{
			in:   nil,
			want: sshSignOptions{namespace: "git"},
		},
	}
	for _, c := range cases {
		if got := parseSSHSignArguments(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("parse %q = %+v, want %+v", c.in, got, c.want)
		}
	}
}

// TestChooseSigningKey checks matching by key material, the
// single-key fallback, and refusal when neither applies.
func TestChooseSigningKey(t *testing.T) {
	keys := []signingKey{
		{Secret: "hmac", Algorithm: "hmac-sha256"},
		{Secret: "work", PublicKey: "ssh-ed25519 AAAAwork host-comment"},
		{Secret: "home", PublicKey: "ssh-ed25519 AAAAhome other"},
	}
	if key, ok := chooseSigningKey(keys, "ssh-ed25519 AAAAhome"); !ok ||
		key.Secret != "home" {
		t.Fatalf("match: %+v, %v", key, ok)
	}
	if _, ok := chooseSigningKey(keys, "ssh-ed25519 AAAAnone"); ok {
		t.Fatal("two candidates and no match must refuse")
	}
	if _, ok := chooseSigningKey(keys, ""); ok {
		t.Fatal("two candidates and no key file must refuse")
	}
	only := keys[:2]
	if key, ok := chooseSigningKey(only, "ssh-ed25519 AAAAnone"); !ok ||
		key.Secret != "work" {
		t.Fatalf("single candidate fallback: %+v, %v", key, ok)
	}
	if _, ok := chooseSigningKey(keys[:1], ""); ok {
		t.Fatal("a key without a public half is not a candidate")
	}
	if got := publicKeyPrefix("ssh-rsa AAAA a comment\nsecond line"); got !=
		"ssh-rsa AAAA" {
		t.Fatalf("publicKeyPrefix = %q", got)
	}
}

// fakeBrokerAPI serves /v1/keys and /v1/sign on loopback. Records
// the last sign request.
type fakeBrokerAPI struct {
	server   *httptest.Server
	keys     []signingKey
	lastSign signRequest
	refuse   string
}

// newFakeBrokerAPI starts a fake broker API with the given keys.
func newFakeBrokerAPI(t *testing.T, keys []signingKey) *fakeBrokerAPI {
	t.Helper()
	api := &fakeBrokerAPI{keys: keys}
	mux := http.NewServeMux()
	mux.HandleFunc(protocol.PathKeys, func(w http.ResponseWriter,
		r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": api.keys})
	})
	mux.HandleFunc(protocol.PathSign, func(w http.ResponseWriter,
		r *http.Request) {
		api.lastSign = signRequest{}
		if err := json.NewDecoder(r.Body).Decode(&api.lastSign); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if api.refuse != "" {
			w.WriteHeader(http.StatusForbidden)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error": map[string]string{"message": api.refuse}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{
			"signature": "SIG(" + api.lastSign.Secret + "," +
				api.lastSign.Namespace + "," + api.lastSign.Data + ")"})
	})
	api.server = httptest.NewServer(mux)
	t.Cleanup(api.server.Close)
	return api
}

// client returns a boxClient pointed at this fake server.
func (api *fakeBrokerAPI) client() *boxClient {
	return &boxClient{baseURL: api.server.URL,
		httpClient: &http.Client{Timeout: 5 * time.Second}}
}

// TestRunSSHSign checks end-to-end: public key printing, verify
// refusal, file signing with key matching, and broker refusal.
func TestRunSSHSign(t *testing.T) {
	api := newFakeBrokerAPI(t, []signingKey{
		{Secret: "work", PublicKey: "ssh-ed25519 AAAAwork host", Available: true},
		{Secret: "home", PublicKey: "ssh-ed25519 AAAAhome other", Available: true},
	})
	var stdout, stderr bytes.Buffer
	if code := runSSHSign([]string{"--print-public-key"}, api.client(),
		&stdout, &stderr); code != 0 ||
		stdout.String() != "ssh-ed25519 AAAAwork host\n" {
		t.Fatalf("print: code %d stdout %q stderr %q", code, stdout.String(),
			stderr.String())
	}
	stdout.Reset()
	if code := runSSHSign([]string{"-Y", "verify"}, api.client(), &stdout,
		&stderr); code != 2 {
		t.Fatalf("verify: code %d", code)
	}
	directory := t.TempDir()
	keyFile := filepath.Join(directory, "id.pub")
	target := filepath.Join(directory, "commit")
	_ = os.WriteFile(keyFile, []byte("ssh-ed25519 AAAAhome me@box\n"), 0o644)
	_ = os.WriteFile(target, []byte("tree abc\n"), 0o644)
	stdout.Reset()
	code := runSSHSign([]string{"-Y", "sign", "-n", "git", "-f", keyFile,
		target}, api.client(), &stdout, &stderr)
	if code != 0 || stdout.Len() != 0 {
		t.Fatalf("sign: code %d stdout %q stderr %q", code, stdout.String(),
			stderr.String())
	}
	signature, err := os.ReadFile(target + ".sig")
	wantData := base64.StdEncoding.EncodeToString([]byte("tree abc\n"))
	if err != nil || string(signature) != "SIG(home,git,"+wantData+")\n" {
		t.Fatalf("signature file %q, %v", signature, err)
	}
	if api.lastSign.Secret != "home" || api.lastSign.Namespace != "git" {
		t.Fatalf("broker saw %+v", api.lastSign)
	}
	stdout.Reset()
	stderr.Reset()
	api.refuse = "the vault is locked"
	code = runSSHSign([]string{"-Y", "sign", "-n", "git", "-f", keyFile,
		target}, api.client(), &stdout, &stderr)
	if code != 1 || !strings.Contains(stderr.String(), "the vault is locked") {
		t.Fatalf("refusal: code %d stderr %q", code, stderr.String())
	}
}

// TestRunSign checks the plain signing shim: payload from a file,
// namespace from the environment, and refusal errors.
func TestRunSign(t *testing.T) {
	api := newFakeBrokerAPI(t, nil)
	directory := t.TempDir()
	payload := filepath.Join(directory, "payload")
	_ = os.WriteFile(payload, []byte{0, 1, 2}, 0o644)
	t.Setenv("PRISON_SIGN_NAMESPACE", "file")
	var stdout, stderr bytes.Buffer
	code := runSign([]string{"hmac", payload}, api.client(),
		strings.NewReader(""), &stdout, &stderr)
	if code != 0 || stdout.String() != "SIG(hmac,file,AAEC)\n" {
		t.Fatalf("file: code %d stdout %q stderr %q", code, stdout.String(),
			stderr.String())
	}
	t.Setenv("PRISON_SIGN_NAMESPACE", "")
	stdout.Reset()
	code = runSign([]string{"hmac"}, api.client(), strings.NewReader("hi"),
		&stdout, &stderr)
	if code != 0 || stdout.String() != "SIG(hmac,,aGk=)\n" {
		t.Fatalf("stdin: code %d stdout %q", code, stdout.String())
	}
	if api.lastSign.Namespace != "" {
		t.Fatalf("an empty namespace was sent as %q", api.lastSign.Namespace)
	}
	api.refuse = "no such secret"
	stderr.Reset()
	if code := runSign([]string{"hmac"}, api.client(), strings.NewReader(""),
		&stdout, &stderr); code != 1 ||
		!strings.Contains(stderr.String(), "no such secret") {
		t.Fatalf("refusal: code %d stderr %q", code, stderr.String())
	}
	if code := runSign(nil, api.client(), strings.NewReader(""), &stdout,
		&stderr); code != 2 {
		t.Fatalf("usage: code %d", code)
	}
}
