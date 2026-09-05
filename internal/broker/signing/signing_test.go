package signing

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// TestHMACSHA256KnownVector checks HMACSHA256 against RFC 4231
// test case 2.
func TestHMACSHA256KnownVector(t *testing.T) {
	got := hex.EncodeToString(HMACSHA256(
		[]byte("Jefe"), []byte("what do ya want for nothing?")))
	want := "5bdcc146bf60754e6a042426089575c75a003f089d2739839dec58b964ec3843"
	if got != want {
		t.Fatalf("HMACSHA256 = %s, want %s", got, want)
	}
}

// TestUnavailableAgent checks that an Agent with no socket path
// reports unavailable and returns a helpful error.
func TestUnavailableAgent(t *testing.T) {
	unavailable := NewAgent("")
	if unavailable.Available() {
		t.Fatal("Available() = true for an empty socket path")
	}
	_, err := unavailable.Keys(context.Background())
	if err == nil || !strings.Contains(err.Error(), "SSH_AUTH_SOCK") {
		t.Fatalf("Keys() error = %v, want one naming SSH_AUTH_SOCK", err)
	}
}

// TestKeysListsLoadedKeys checks that Keys returns each key with
// its fingerprint, public key text, and comment.
func TestKeysListsLoadedKeys(t *testing.T) {
	signer, keyring := newEd25519Keyring(t, "alice@example")
	testAgent := NewAgent(serveKeyring(t, keyring))

	keys, err := testAgent.Keys(context.Background())
	if err != nil {
		t.Fatalf("Keys() error = %v", err)
	}
	if len(keys) != 1 {
		t.Fatalf("Keys() returned %d keys, want 1", len(keys))
	}
	wantFingerprint := ssh.FingerprintSHA256(signer.PublicKey())
	if keys[0].Fingerprint != wantFingerprint {
		t.Errorf("Fingerprint = %s, want %s", keys[0].Fingerprint,
			wantFingerprint)
	}
	wantPublicKey := strings.TrimSpace(
		string(ssh.MarshalAuthorizedKey(signer.PublicKey())))
	if keys[0].PublicKey != wantPublicKey {
		t.Errorf("PublicKey = %q, want %q", keys[0].PublicKey, wantPublicKey)
	}
	if keys[0].Comment != "alice@example" {
		t.Errorf("Comment = %q, want %q", keys[0].Comment, "alice@example")
	}
}

// TestFindKeyMissing checks the error for a missing fingerprint and
// a successful lookup for a present one.
func TestFindKeyMissing(t *testing.T) {
	signer, keyring := newEd25519Keyring(t, "")
	testAgent := NewAgent(serveKeyring(t, keyring))

	missing := "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	_, err := testAgent.FindKey(context.Background(), missing)
	want := "no key with fingerprint " + missing +
		" is loaded in the ssh agent"
	if err == nil || err.Error() != want {
		t.Fatalf("FindKey() error = %v, want %q", err, want)
	}

	found, err := testAgent.FindKey(
		context.Background(), ssh.FingerprintSHA256(signer.PublicKey()))
	if err != nil {
		t.Fatalf("FindKey() error = %v", err)
	}
	if !bytes.Equal(found.blob, signer.PublicKey().Marshal()) {
		t.Fatal("FindKey() returned a key with the wrong blob")
	}
}

// TestFingerprintMatchesSSHKeygen checks the fingerprint format and,
// when ssh-keygen is available, verifies it matches.
func TestFingerprintMatchesSSHKeygen(t *testing.T) {
	signer, keyring := newEd25519Keyring(t, "")
	testAgent := NewAgent(serveKeyring(t, keyring))
	keys, err := testAgent.Keys(context.Background())
	if err != nil {
		t.Fatalf("Keys() error = %v", err)
	}
	fingerprint := keys[0].Fingerprint

	if !strings.HasPrefix(fingerprint, "SHA256:") ||
		strings.HasSuffix(fingerprint, "=") || len(fingerprint) != 50 {
		t.Fatalf("Fingerprint %q is not SHA256: plus 43 base64 characters",
			fingerprint)
	}

	sshKeygen, err := exec.LookPath("ssh-keygen")
	if err != nil {
		t.Skip("ssh-keygen is not on PATH")
	}
	publicKeyPath := filepath.Join(t.TempDir(), "key.pub")
	if err := os.WriteFile(publicKeyPath,
		ssh.MarshalAuthorizedKey(signer.PublicKey()), 0o600); err != nil {
		t.Fatal(err)
	}
	output, err := exec.Command(sshKeygen, "-lf", publicKeyPath).Output()
	if err != nil {
		t.Fatalf("ssh-keygen -lf failed: %v", err)
	}
	fields := strings.Fields(string(output))
	if len(fields) < 2 || fields[1] != fingerprint {
		t.Fatalf("ssh-keygen -lf printed %q, want fingerprint %s",
			string(output), fingerprint)
	}
}

// TestSignSSHSIG signs a message with ed25519 and RSA keys. Verifies
// each signature structurally and, when ssh-keygen is available,
// with `ssh-keygen -Y verify`.
func TestSignSSHSIG(t *testing.T) {
	cases := []struct {
		name       string
		newKey     func(t *testing.T) testKey
		wantFormat string
	}{
		{"ed25519", newEd25519Key, ssh.KeyAlgoED25519},
		{"rsa", newRSAKey, ssh.KeyAlgoRSASHA512},
	}
	message := []byte("tree deadbeef\nauthor prison\n\nsigned commit\n")

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			key := testCase.newKey(t)
			signer := key.signer
			keyring := agent.NewKeyring()
			if err := keyring.Add(agent.AddedKey{
				PrivateKey: key.privateKey, Comment: "test",
			}); err != nil {
				t.Fatal(err)
			}
			testAgent := NewAgent(serveKeyring(t, keyring))
			fingerprint := ssh.FingerprintSHA256(signer.PublicKey())

			signature, err := testAgent.SignSSHSIG(
				context.Background(), fingerprint, "", message)
			if err != nil {
				t.Fatalf("SignSSHSIG() error = %v", err)
			}
			if signature.Namespace != "git" {
				t.Errorf("Namespace = %q, want git", signature.Namespace)
			}
			wantPublicKey := strings.TrimSpace(
				string(ssh.MarshalAuthorizedKey(signer.PublicKey())))
			if signature.PublicKey != wantPublicKey {
				t.Errorf("PublicKey = %q, want %q", signature.PublicKey,
					wantPublicKey)
			}

			checkArmourShape(t, signature.Armored)
			parsedSignature := verifyStructurally(
				t, signature.Armored, signer.PublicKey(), "git", message)
			if parsedSignature.Format != testCase.wantFormat {
				t.Errorf("signature format = %s, want %s",
					parsedSignature.Format, testCase.wantFormat)
			}
			verifyWithSSHKeygen(t, signature, message)
		})
	}
}

// TestSignSSHSIGMissingKey checks that signing with an unknown
// fingerprint returns an error.
func TestSignSSHSIGMissingKey(t *testing.T) {
	_, keyring := newEd25519Keyring(t, "")
	testAgent := NewAgent(serveKeyring(t, keyring))
	_, err := testAgent.SignSSHSIG(context.Background(),
		"SHA256:nope", "git", []byte("x"))
	if err == nil || !strings.Contains(err.Error(), "no key with fingerprint") {
		t.Fatalf("SignSSHSIG() error = %v, want a missing key error", err)
	}
}

// TestSignSSHSIGRefused checks the error when the agent lists a key
// but refuses to sign, as happens when a host confirmation prompt is
// declined.
func TestSignSSHSIGRefused(t *testing.T) {
	signer, keyring := newEd25519Keyring(t, "")
	testAgent := NewAgent(serveKeyring(t, refusingAgent{keyring}))
	fingerprint := ssh.FingerprintSHA256(signer.PublicKey())

	_, err := testAgent.SignSSHSIG(
		context.Background(), fingerprint, "git", []byte("x"))
	if err == nil {
		t.Fatal("SignSSHSIG() succeeded against a refusing agent")
	}
	if strings.Contains(err.Error(), "no key with fingerprint") {
		t.Fatalf("SignSSHSIG() error = %v, want a refusal", err)
	}
	if !strings.Contains(err.Error(), "confirmation") ||
		!strings.Contains(err.Error(), fingerprint) {
		t.Fatalf("SignSSHSIG() error = %v, want a confirmation hint "+
			"naming the key", err)
	}
}

// refusingAgent wraps an agent to list keys but refuse all sign
// requests.
type refusingAgent struct {
	agent.Agent
}

// Sign always returns an error.
func (refusingAgent) Sign(ssh.PublicKey, []byte) (*ssh.Signature, error) {
	return nil, errors.New("user declined")
}

// TestContextCancellation checks that a cancelled context unblocks
// an exchange with an unresponsive agent.
func TestContextCancellation(t *testing.T) {
	socketPath := shortSocketPath(t)
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	go func() {
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			defer connection.Close()
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(),
		200*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err = NewAgent(socketPath).Keys(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Keys() error = %v, want context.DeadlineExceeded", err)
	}
	if time.Since(started) > 5*time.Second {
		t.Fatal("Keys() did not return promptly after cancellation")
	}
}

// checkArmourShape checks that armored has BEGIN/END framing and
// base64 lines of at most 70 columns.
func checkArmourShape(t *testing.T, armored []byte) {
	t.Helper()
	text := string(armored)
	if !strings.HasPrefix(text, armourHeader) ||
		!strings.HasSuffix(text, armourFooter) {
		t.Fatalf("armour is not framed by BEGIN and END lines:\n%s", text)
	}
	body := strings.TrimSuffix(strings.TrimPrefix(text, armourHeader),
		armourFooter)
	lines := strings.Split(strings.TrimSuffix(body, "\n"), "\n")
	for index, line := range lines {
		if len(line) == 0 || len(line) > armourLineLength {
			t.Fatalf("armour line %d has %d columns", index+1, len(line))
		}
		if index < len(lines)-1 && len(line) != armourLineLength {
			t.Fatalf("armour line %d is short but not last", index+1)
		}
	}
}

// parsedSSHSIG holds the decoded fields of an armoured SSHSIG
// signature.
type parsedSSHSIG struct {
	Version       uint32
	PublicKey     []byte
	Namespace     string
	Reserved      string
	HashAlgorithm string
	Signature     []byte
}

// verifyStructurally decodes the armoured blob, checks each SSHSIG
// field, and verifies the inner signature against the public key.
// Returns the parsed signature.
func verifyStructurally(
	t *testing.T, armored []byte, publicKey ssh.PublicKey,
	namespace string, message []byte,
) *ssh.Signature {
	t.Helper()
	body := strings.TrimSuffix(strings.TrimPrefix(string(armored),
		armourHeader), armourFooter)
	blob, err := base64.StdEncoding.DecodeString(
		strings.ReplaceAll(body, "\n", ""))
	if err != nil {
		t.Fatalf("armour body is not base64: %v", err)
	}
	if !bytes.HasPrefix(blob, []byte(sshsigMagic)) {
		t.Fatalf("blob does not start with %q", sshsigMagic)
	}

	var parsed parsedSSHSIG
	if err := ssh.Unmarshal(blob[len(sshsigMagic):], &parsed); err != nil {
		t.Fatalf("blob does not parse as SSHSIG: %v", err)
	}
	if parsed.Version != 1 {
		t.Errorf("version = %d, want 1", parsed.Version)
	}
	if !bytes.Equal(parsed.PublicKey, publicKey.Marshal()) {
		t.Error("public key blob does not match the signing key")
	}
	if parsed.Namespace != namespace {
		t.Errorf("namespace = %q, want %q", parsed.Namespace, namespace)
	}
	if parsed.Reserved != "" {
		t.Errorf("reserved = %q, want empty", parsed.Reserved)
	}
	if parsed.HashAlgorithm != "sha512" {
		t.Errorf("hash algorithm = %q, want sha512", parsed.HashAlgorithm)
	}

	var innerSignature ssh.Signature
	if err := ssh.Unmarshal(parsed.Signature, &innerSignature); err != nil {
		t.Fatalf("inner signature does not parse: %v", err)
	}
	if err := publicKey.Verify(
		signedData(namespace, message), &innerSignature); err != nil {
		t.Fatalf("signature does not verify over the signed data: %v", err)
	}
	return &innerSignature
}

// verifyWithSSHKeygen runs `ssh-keygen -Y verify` to check the
// signature. Skips if ssh-keygen is not on PATH.
func verifyWithSSHKeygen(
	t *testing.T, signature *SSHSignature, message []byte,
) {
	t.Helper()
	sshKeygen, err := exec.LookPath("ssh-keygen")
	if err != nil {
		t.Log("ssh-keygen is not on PATH; structural check only")
		return
	}
	directory := t.TempDir()
	allowedSignersPath := filepath.Join(directory, "allowed_signers")
	signaturePath := filepath.Join(directory, "sig.sig")
	if err := os.WriteFile(allowedSignersPath,
		[]byte("test "+signature.PublicKey+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		signaturePath, signature.Armored, 0o600); err != nil {
		t.Fatal(err)
	}

	command := exec.Command(sshKeygen, "-Y", "verify",
		"-f", allowedSignersPath, "-I", "test",
		"-n", signature.Namespace, "-s", signaturePath)
	command.Stdin = bytes.NewReader(message)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("ssh-keygen -Y verify rejected the signature: %v\n%s",
			err, output)
	}
}

// testKey pairs a private key with its ssh.Signer for loading into
// a keyring and public key comparison.
type testKey struct {
	privateKey any
	signer     ssh.Signer
}

// newEd25519Key generates a fresh Ed25519 test key.
func newEd25519Key(t *testing.T) testKey {
	t.Helper()
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return wrapTestKey(t, privateKey)
}

// newRSAKey generates a fresh 2048-bit RSA test key.
func newRSAKey(t *testing.T) testKey {
	t.Helper()
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return wrapTestKey(t, privateKey)
}

// wrapTestKey wraps a private key into a testKey with its
// ssh.Signer.
func wrapTestKey(t *testing.T, privateKey any) testKey {
	t.Helper()
	signer, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	return testKey{privateKey: privateKey, signer: signer}
}

// newEd25519Keyring returns an Ed25519 signer and a keyring loaded
// with its private key.
func newEd25519Keyring(
	t *testing.T, comment string,
) (ssh.Signer, agent.Agent) {
	t.Helper()
	key := newEd25519Key(t)
	keyring := agent.NewKeyring()
	if err := keyring.Add(agent.AddedKey{
		PrivateKey: key.privateKey, Comment: comment,
	}); err != nil {
		t.Fatal(err)
	}
	return key.signer, keyring
}

// serveKeyring serves the keyring over a Unix socket. Returns the
// socket path. Closed on cleanup.
func serveKeyring(t *testing.T, keyring agent.Agent) string {
	t.Helper()
	socketPath := shortSocketPath(t)
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	go func() {
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer connection.Close()
				agent.ServeAgent(keyring, connection)
			}()
		}
	}()
	return socketPath
}

// shortSocketPath returns a socket path short enough for macOS's
// 104-byte limit. Falls back to os.MkdirTemp when t.TempDir is too
// long.
func shortSocketPath(t *testing.T) string {
	t.Helper()
	socketPath := filepath.Join(t.TempDir(), "agent.sock")
	if len(socketPath) < 100 {
		return socketPath
	}
	directory, err := os.MkdirTemp("", "prison-signing")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(directory) })
	return filepath.Join(directory, "agent.sock")
}
