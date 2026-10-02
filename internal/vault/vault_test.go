package vault

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const testPassphrase = "correct horse battery staple"

func newTestVault(t *testing.T) (*Vault, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "secrets.vault")
	vault, err := Create(path, testPassphrase)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return vault, path
}

func routeSecret(name string) *Secret {
	return &Secret{
		Name:  name,
		Mode:  ModeRoute,
		Value: "sk-" + name,
		Route: &RouteSpec{
			Upstream:   "api.example.com",
			PathPrefix: "/v1",
			Header:     "Authorization",
			Prefix:     "Bearer ",
			Methods:    []string{"post", "GET", "post"},
			RateLimit:  10,
		},
	}
}

func exposeSecret(name string) *Secret {
	variable := "EXPOSED_" + strings.ToUpper(strings.ReplaceAll(name, "-", "_"))
	return &Secret{
		Name:   name,
		Mode:   ModeExpose,
		Value:  "value-" + name,
		Expose: &ExposeSpec{Variable: variable},
	}
}

func readContainerFile(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	return parsed
}

func writeContainerFile(t *testing.T, path string, parsed map[string]any) {
	t.Helper()
	raw, err := json.MarshalIndent(parsed, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent: %v", err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

func TestCreateRefusals(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.vault")
	if _, err := Create(path, ""); err == nil {
		t.Fatal("Create(\"\") = nil, want an error")
	}
	if Exists(path) {
		t.Fatal("Exists = true, want false")
	}
	if _, err := Create(path, testPassphrase); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !Exists(path) {
		t.Fatal("Exists = false, want true")
	}
	if _, err := Create(path, testPassphrase); err == nil {
		t.Fatal("Create = nil, want an error")
	}
}

func TestRoundTrip(t *testing.T) {
	vault, path := newTestVault(t)
	if replaced, err := vault.PutSecret(routeSecret("openai")); err != nil {
		t.Fatalf("PutSecret: %v", err)
	} else if replaced {
		t.Fatal("PutSecret = true, want false")
	}
	if _, err := vault.PutSecret(exposeSecret("alpha")); err != nil {
		t.Fatalf("PutSecret: %v", err)
	}
	if replaced, err := vault.PutSecret(routeSecret("openai")); err != nil {
		t.Fatalf("PutSecret again: %v", err)
	} else if !replaced {
		t.Fatal("PutSecret = false, want true")
	}
	authority := &Authority{KeyPEM: "key", CertificatePEM: "certificate"}
	if err := vault.SetAuthority("api.example.com", authority); err != nil {
		t.Fatalf("SetAuthority: %v", err)
	}

	reopened, err := Open(path, testPassphrase)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	secrets := reopened.Secrets()
	if len(secrets) != 2 || secrets[0].Name != "alpha" ||
		secrets[1].Name != "openai" {
		t.Fatalf("Secrets = %v, want alpha, openai", secrets)
	}
	openai, found := reopened.Secret("openai")
	if !found {
		t.Fatal("Secret(openai) = false, want true")
	}
	if openai.Value != "sk-openai" || openai.Route == nil ||
		strings.Join(openai.Route.Methods, ",") != "GET,POST" ||
		openai.Created.IsZero() {
		t.Fatalf("Secret(openai) = %+v, want the stored route", openai)
	}
	openai.Route.Methods[0] = "DELETE"
	again, _ := reopened.Secret("openai")
	if again.Route.Methods[0] != "GET" {
		t.Fatalf("Methods[0] = %q, want GET", again.Route.Methods[0])
	}
	stored, found := reopened.Authority("api.example.com")
	if !found || stored.KeyPEM != "key" || stored.Created.IsZero() {
		t.Fatalf("Authority = %+v, %v, want the stored key", stored, found)
	}
	if _, found := reopened.Authority("other.example.com"); found {
		t.Fatal("Authority(other.example.com) = true, want false")
	}

	if deleted, err := reopened.DeleteSecret("alpha"); err != nil || !deleted {
		t.Fatalf("DeleteSecret = %v, %v, want true, nil", deleted, err)
	}
	if deleted, err := reopened.DeleteSecret("alpha"); err != nil || deleted {
		t.Fatalf("DeleteSecret = %v, %v, want false, nil", deleted, err)
	}
	if _, found := reopened.Secret("alpha"); found {
		t.Fatal("Secret(alpha) = true, want false")
	}

	parsed := readContainerFile(t, path)
	if parsed["version"] != float64(2) {
		t.Errorf("version = %v, want 2", parsed["version"])
	}
	kdf, _ := parsed["kdf"].(map[string]any)
	if kdf["name"] != "argon2id" || kdf["time"] != float64(3) ||
		kdf["memory_kib"] != float64(65536) || kdf["threads"] != float64(1) {
		t.Errorf("kdf = %v, want argon2id, 3, 65536, 1", kdf)
	}
	for _, field := range []string{"salt", "nonce", "ciphertext"} {
		if text, _ := parsed[field].(string); text == "" {
			t.Errorf("%s = \"\", want a value", field)
		}
	}
	raw, _ := os.ReadFile(path)
	if !strings.HasPrefix(string(raw), "{\n  \"version\": 2,") {
		t.Errorf("file = %q, want two-space indent", raw[:24])
	}
}

func TestFileMode(t *testing.T) {
	vault, path := newTestVault(t)
	if _, err := vault.PutSecret(exposeSecret("alpha")); err != nil {
		t.Fatalf("PutSecret: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, want 600", info.Mode().Perm())
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Fatalf("ReadDir = %v, want only the vault", entries)
	}
}

func TestSaltReusedNonceFresh(t *testing.T) {
	vault, path := newTestVault(t)
	before := readContainerFile(t, path)
	if _, err := vault.PutSecret(exposeSecret("alpha")); err != nil {
		t.Fatalf("PutSecret: %v", err)
	}
	after := readContainerFile(t, path)
	if before["salt"] != after["salt"] {
		t.Errorf("salt = %v, want %v", after["salt"], before["salt"])
	}
	if before["nonce"] == after["nonce"] {
		t.Error("nonce = previous nonce, want a new one")
	}
}

func TestWrongPassphrase(t *testing.T) {
	_, path := newTestVault(t)
	_, err := Open(path, "not it")
	if !errors.Is(err, ErrWrongPassphrase) {
		t.Fatalf("Open = %v, want ErrWrongPassphrase", err)
	}
	if err.Error() != "wrong passphrase or tampered vault" {
		t.Fatalf("Open = %q, want \"wrong passphrase or tampered vault\"",
			err.Error())
	}
}

func TestTampering(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(parsed map[string]any)
		wantErr error
		wantMsg string
	}{
		{
			name: "kdf time",
			mutate: func(parsed map[string]any) {
				parsed["kdf"].(map[string]any)["time"] = 1
			},
			wantErr: ErrWrongPassphrase,
		},
		{
			name: "salt",
			mutate: func(parsed map[string]any) {
				salt := []byte(parsed["salt"].(string))
				if salt[0] == 'A' {
					salt[0] = 'B'
				} else {
					salt[0] = 'A'
				}
				parsed["salt"] = string(salt)
			},
			wantErr: ErrWrongPassphrase,
		},
		{
			name: "ciphertext",
			mutate: func(parsed map[string]any) {
				text := []byte(parsed["ciphertext"].(string))
				if text[3] == 'A' {
					text[3] = 'B'
				} else {
					text[3] = 'A'
				}
				parsed["ciphertext"] = string(text)
			},
			wantErr: ErrWrongPassphrase,
		},
		{
			name: "version",
			mutate: func(parsed map[string]any) {
				parsed["version"] = 1
			},
			wantMsg: "is a version 1 vault",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, path := newTestVault(t)
			parsed := readContainerFile(t, path)
			c.mutate(parsed)
			writeContainerFile(t, path, parsed)
			_, err := Open(path, testPassphrase)
			if err == nil {
				t.Fatal("Open = nil, want an error")
			}
			if c.wantErr != nil && !errors.Is(err, c.wantErr) {
				t.Fatalf("Open = %v, want %v", err, c.wantErr)
			}
			if c.wantMsg != "" && !strings.Contains(err.Error(), c.wantMsg) {
				t.Fatalf("Open = %v, want %q", err, c.wantMsg)
			}
		})
	}
}

func TestReloadSeesExternalWrite(t *testing.T) {
	first, path := newTestVault(t)
	second, err := Open(path, testPassphrase)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, found := first.Secret("alpha"); found {
		t.Fatal("Secret(alpha) = true, want false")
	}
	if _, err := second.PutSecret(exposeSecret("alpha")); err != nil {
		t.Fatalf("PutSecret: %v", err)
	}
	if _, found := first.Secret("alpha"); !found {
		t.Fatal("Secret(alpha) = false, want true")
	}
	if err := first.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if err := first.Reload(); err == nil {
		t.Fatal("Reload = nil, want an error")
	}
	if secrets := first.Secrets(); len(secrets) != 0 {
		t.Fatalf("len(Secrets) = %d, want 0",
			len(secrets))
	}
}

func TestLock(t *testing.T) {
	vault, _ := newTestVault(t)
	vault.Lock()
	if err := vault.Reload(); !errors.Is(err, ErrLocked) {
		t.Fatalf("Reload = %v, want ErrLocked", err)
	}
	_, err := vault.PutSecret(exposeSecret("alpha"))
	if !errors.Is(err, ErrLocked) {
		t.Fatalf("PutSecret = %v, want ErrLocked", err)
	}
	if _, found := vault.Secret("alpha"); found {
		t.Fatal("Secret(alpha) = true, want false")
	}
}

func TestConcurrentReadersDuringExternalWrites(t *testing.T) {
	reader, path := newTestVault(t)
	writer, err := Open(path, testPassphrase)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	const writes = 20
	const readers = 8
	var group sync.WaitGroup
	stop := make(chan struct{})

	for index := 0; index < readers; index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				reader.Secrets()
				reader.Secret("external-5")
				reader.Authority("api.example.com")
			}
		}()
	}
	group.Add(1)
	go func() {
		defer group.Done()
		for index := 0; index < writes; index++ {
			name := "external-" + string(rune('a'+index%26))
			if _, err := writer.PutSecret(exposeSecret(name)); err != nil {
				t.Errorf("PutSecret: %v", err)
				return
			}
		}
	}()
	for index := 0; index < writes; index++ {
		if _, err := reader.PutSecret(exposeSecret("local")); err != nil {
			t.Fatalf("PutSecret: %v", err)
		}
	}
	close(stop)
	group.Wait()

	if err := reader.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if _, err := Open(path, testPassphrase); err != nil {
		t.Fatalf("Open: %v", err)
	}
}

func TestValidateSecret(t *testing.T) {
	cases := []struct {
		name   string
		secret *Secret
		fails  bool
	}{
		{name: "route ok", secret: routeSecret("openai")},
		{name: "expose ok", secret: exposeSecret("alpha")},
		{name: "sign hmac ok", secret: &Secret{
			Name: "hooks", Mode: ModeSign, Value: "k",
			Sign: &SignSpec{Algorithm: AlgorithmHMACSHA256}}},
		{name: "sign agent ok", secret: &Secret{
			Name: "git", Mode: ModeSign,
			Sign: &SignSpec{Algorithm: AlgorithmSSHAgent,
				Fingerprint: "SHA256:" + strings.Repeat("a", 43)}}},
		{name: "nil", secret: nil, fails: true},
		{name: "uppercase name", secret: mutate(routeSecret("Openai"), nil),
			fails: true},
		{name: "name starts with digit", secret: routeSecret("1x"),
			fails: true},
		{name: "name too long", secret: routeSecret(strings.Repeat("a", 33)),
			fails: true},
		{name: "reserved name", secret: routeSecret("prison"), fails: true},
		{name: "unknown mode", secret: mutate(routeSecret("x"),
			func(s *Secret) { s.Mode = "other" }), fails: true},
		{name: "spec missing", secret: mutate(routeSecret("x"),
			func(s *Secret) { s.Route = nil }), fails: true},
		{name: "extra spec", secret: mutate(routeSecret("x"),
			func(s *Secret) { s.Expose = &ExposeSpec{Variable: "A"} }),
			fails: true},
		{name: "route no upstream", secret: mutate(routeSecret("x"),
			func(s *Secret) { s.Route.Upstream = "" }), fails: true},
		{name: "route upstream scheme", secret: mutate(routeSecret("x"),
			func(s *Secret) { s.Route.Upstream = "https://api.example.com" }),
			fails: true},
		{name: "route upstream path", secret: mutate(routeSecret("x"),
			func(s *Secret) { s.Route.Upstream = "api.example.com/v1" }),
			fails: true},
		{name: "route upstream bad port", secret: mutate(routeSecret("x"),
			func(s *Secret) { s.Route.Upstream = "api.example.com:x" }),
			fails: true},
		{name: "route prefix", secret: mutate(routeSecret("x"),
			func(s *Secret) { s.Route.PathPrefix = "v1" }), fails: true},
		{name: "route no header", secret: mutate(routeSecret("x"),
			func(s *Secret) { s.Route.Header = "" }), fails: true},
		{name: "route bad base url variable", secret: mutate(routeSecret("x"),
			func(s *Secret) { s.Route.BaseURLVariable = "1X" }), fails: true},
		{name: "route bad token variable", secret: mutate(routeSecret("x"),
			func(s *Secret) {
				s.Route.TokenVariable = "1X"
				s.Route.TokenPlaceholder = "fake"
			}), fails: true},
		{name: "route token variable without placeholder", secret: mutate(routeSecret("x"),
			func(s *Secret) { s.Route.TokenVariable = "API_KEY" }), fails: true},
		{name: "route token placeholder without variable", secret: mutate(routeSecret("x"),
			func(s *Secret) { s.Route.TokenPlaceholder = "fake" }), fails: true},
		{name: "route token placeholder with newline", secret: mutate(routeSecret("x"),
			func(s *Secret) {
				s.Route.TokenVariable = "API_KEY"
				s.Route.TokenPlaceholder = "fake\nkey"
			}), fails: true},
		{name: "route token variable with placeholder", secret: mutate(routeSecret("x"),
			func(s *Secret) {
				s.Route.TokenVariable = "API_KEY"
				s.Route.TokenPlaceholder = "sk_test_placeholder"
			}), fails: false},
		{name: "route bad method", secret: mutate(routeSecret("x"),
			func(s *Secret) { s.Route.Methods = []string{"GE T"} }),
			fails: true},
		{name: "route negative rate", secret: mutate(routeSecret("x"),
			func(s *Secret) { s.Route.RateLimit = -1 }), fails: true},
		{name: "route empty value", secret: mutate(routeSecret("x"),
			func(s *Secret) { s.Value = "" }), fails: true},
		{name: "sign bad algorithm", secret: &Secret{
			Name: "x", Mode: ModeSign, Value: "k",
			Sign: &SignSpec{Algorithm: "rsa"}}, fails: true},
		{name: "sign agent no fingerprint", secret: &Secret{
			Name: "x", Mode: ModeSign,
			Sign: &SignSpec{Algorithm: AlgorithmSSHAgent}}, fails: true},
		{name: "sign agent md5 fingerprint", secret: &Secret{
			Name: "x", Mode: ModeSign,
			Sign: &SignSpec{Algorithm: AlgorithmSSHAgent,
				Fingerprint: "MD5:aa:bb"}}, fails: true},
		{name: "sign agent with value", secret: &Secret{
			Name: "x", Mode: ModeSign, Value: "k",
			Sign: &SignSpec{Algorithm: AlgorithmSSHAgent,
				Fingerprint: "SHA256:" + strings.Repeat("a", 43)}},
			fails: true},
		{name: "sign hmac no value", secret: &Secret{
			Name: "x", Mode: ModeSign,
			Sign: &SignSpec{Algorithm: AlgorithmHMACSHA256}}, fails: true},
		{name: "sign negative rate", secret: &Secret{
			Name: "x", Mode: ModeSign, Value: "k",
			Sign: &SignSpec{Algorithm: AlgorithmHMACSHA256, RateLimit: -1}},
			fails: true},
		{name: "expose bad variable", secret: mutate(exposeSecret("x"),
			func(s *Secret) { s.Expose.Variable = "9X" }), fails: true},
		{name: "expose dash variable", secret: mutate(exposeSecret("x"),
			func(s *Secret) { s.Expose.Variable = "A-B" }), fails: true},
		{name: "expose newline", secret: mutate(exposeSecret("x"),
			func(s *Secret) { s.Value = "a\nb" }), fails: true},
		{name: "expose empty value", secret: mutate(exposeSecret("x"),
			func(s *Secret) { s.Value = "" }), fails: true},
	}
	for _, c := range cases {
		err := ValidateSecret(c.secret)
		if c.fails && err == nil {
			t.Errorf("%s: ValidateSecret = nil, want an error", c.name)
		}
		if !c.fails && err != nil {
			t.Errorf("%s: ValidateSecret = %v, want nil", c.name, err)
		}
	}

	normalised := routeSecret("x")
	normalised.Route.Upstream = "API.Example.COM.:443"
	if err := ValidateSecret(normalised); err != nil {
		t.Fatalf("ValidateSecret: %v", err)
	}
	if normalised.Route.Upstream != "api.example.com" {
		t.Errorf("upstream = %q, want api.example.com",
			normalised.Route.Upstream)
	}
	if strings.Join(normalised.Route.Methods, ",") != "GET,POST" {
		t.Errorf("methods = %v, want [GET POST]", normalised.Route.Methods)
	}
	normalised.Route.Upstream = "Api.Example.com:8443"
	normalised.Route.Methods = []string{" ", ""}
	if err := ValidateSecret(normalised); err != nil {
		t.Fatalf("ValidateSecret: %v", err)
	}
	if normalised.Route.Upstream != "api.example.com:8443" {
		t.Errorf("upstream = %q, want api.example.com:8443",
			normalised.Route.Upstream)
	}
	if normalised.Route.Methods != nil {
		t.Errorf("methods = %v, want nil", normalised.Route.Methods)
	}
}

func mutate(secret *Secret, change func(*Secret)) *Secret {
	if change != nil {
		change(secret)
	}
	return secret
}

func TestDigest(t *testing.T) {
	if Digest("") != "-" {
		t.Errorf("Digest(\"\") = %q, want -", Digest(""))
	}
	got := Digest("abc")
	want := "sha256:ba7816bf8f01"
	if got != want {
		t.Errorf("Digest(abc) = %q, want %q", got, want)
	}
}

func TestSplitUpstream(t *testing.T) {
	cases := []struct {
		in   string
		host string
		port int
	}{
		{in: "api.example.com", host: "api.example.com", port: 443},
		{in: "API.Example.COM.", host: "api.example.com", port: 443},
		{in: "api.example.com:8443", host: "api.example.com", port: 8443},
		{in: "Api.Example.com.:80", host: "api.example.com", port: 80},
		{in: "localhost:1", host: "localhost", port: 1},
		{in: "api.example.com:0", host: "api.example.com:0", port: 443},
		{in: "api.example.com:x", host: "api.example.com:x", port: 443},
	}
	for _, c := range cases {
		host, port := SplitUpstream(c.in)
		if host != c.host || port != c.port {
			t.Errorf("SplitUpstream(%q) = %q, %d, want %q, %d",
				c.in, host, port, c.host, c.port)
		}
	}
}

func TestPutSecretStampsCreated(t *testing.T) {
	vault, _ := newTestVault(t)
	given := exposeSecret("dated")
	given.Created = time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	if _, err := vault.PutSecret(given); err != nil {
		t.Fatalf("PutSecret: %v", err)
	}
	stored, _ := vault.Secret("dated")
	if !stored.Created.Equal(given.Created) {
		t.Errorf("Created = %v, want %v", stored.Created, given.Created)
	}
	fresh := exposeSecret("fresh")
	if _, err := vault.PutSecret(fresh); err != nil {
		t.Fatalf("PutSecret: %v", err)
	}
	if fresh.Created.IsZero() {
		t.Error("Created = zero, want a time")
	}
}
