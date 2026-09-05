package signing

import (
	"bytes"
	"context"
	"crypto/sha512"
	"encoding/base64"
	"fmt"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// sshsigMagic is the magic bytes that open SSHSIG data and signature
// blobs.
const sshsigMagic = "SSHSIG"

// sshsigVersion is the SSHSIG blob version.
const sshsigVersion uint32 = 1

// sshsigHashAlgorithm is the digest applied to the message before
// signing.
const sshsigHashAlgorithm = "sha512"

// defaultNamespace is the SSHSIG namespace used when none is given.
const defaultNamespace = "git"

// armourLineLength is the base64 line width in PEM-style armour.
const armourLineLength = 70

// armourHeader and armourFooter frame the PEM-style signature text.
const (
	armourHeader = "-----BEGIN SSH SIGNATURE-----\n"
	armourFooter = "-----END SSH SIGNATURE-----\n"
)

// SSHSignature holds one SSHSIG signature. Armored is the PEM-style
// text ready for a `.sig` file.
type SSHSignature struct {
	Armored   []byte
	PublicKey string
	Namespace string
}

// sshsigSignedData is the data structure the agent signs, after the
// magic prefix.
type sshsigSignedData struct {
	Namespace     string
	Reserved      string
	HashAlgorithm string
	Hash          []byte
}

// sshsigBlob is the armoured signature blob structure, after the magic
// prefix.
type sshsigBlob struct {
	Version       uint32
	PublicKey     []byte
	Namespace     string
	Reserved      string
	HashAlgorithm string
	Signature     []byte
}

// SignSSHSIG signs a message via the SSH agent and returns the armoured
// SSHSIG signature. Takes a context, key fingerprint, namespace (empty
// defaults to "git"), and message bytes. Returns an error if the agent
// is unavailable or refuses to sign.
func (a *Agent) SignSSHSIG(
	ctx context.Context, fingerprint, namespace string, message []byte,
) (*SSHSignature, error) {
	if namespace == "" {
		namespace = defaultNamespace
	}

	var result *SSHSignature
	err := a.withClient(ctx, func(client agent.ExtendedAgent) error {
		key, err := findAgentKey(client, fingerprint)
		if err != nil {
			return err
		}

		signature, err := signWithAgent(
			client, key, signedData(namespace, message))
		if err != nil {
			return fmt.Errorf("the ssh agent refused to sign with %s; a "+
				"key that asks for confirmation needs it given on the "+
				"host: %w", fingerprint, err)
		}

		result = &SSHSignature{
			Armored:   armour(signatureBlob(key.Blob, namespace, signature)),
			PublicKey: describeAgentKey(key).PublicKey,
			Namespace: namespace,
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// signWithAgent requests a signature from the agent. Uses
// rsa-sha2-512 for RSA keys, plain signing for other types.
func signWithAgent(
	client agent.ExtendedAgent, key *agent.Key, data []byte,
) (*ssh.Signature, error) {
	switch key.Type() {
	case ssh.KeyAlgoRSA, ssh.CertAlgoRSAv01:
		return client.SignWithFlags(key, data, agent.SignatureFlagRsaSha512)
	default:
		return client.Sign(key, data)
	}
}

// signedData builds the bytes the agent signs: the magic prefix
// followed by namespace, hash algorithm, and the SHA-512 digest of
// the message.
func signedData(namespace string, message []byte) []byte {
	digest := sha512.Sum512(message)
	tail := ssh.Marshal(sshsigSignedData{
		Namespace:     namespace,
		Reserved:      "",
		HashAlgorithm: sshsigHashAlgorithm,
		Hash:          digest[:],
	})
	return append([]byte(sshsigMagic), tail...)
}

// signatureBlob builds the binary SSHSIG blob from the public key,
// namespace, and signature.
func signatureBlob(
	publicKeyBlob []byte, namespace string, signature *ssh.Signature,
) []byte {
	tail := ssh.Marshal(sshsigBlob{
		Version:       sshsigVersion,
		PublicKey:     publicKeyBlob,
		Namespace:     namespace,
		Reserved:      "",
		HashAlgorithm: sshsigHashAlgorithm,
		Signature:     ssh.Marshal(signature),
	})
	return append([]byte(sshsigMagic), tail...)
}

// armour encodes a blob as PEM-style SSH SIGNATURE text.
func armour(blob []byte) []byte {
	encoded := base64.StdEncoding.EncodeToString(blob)
	var out bytes.Buffer
	out.WriteString(armourHeader)
	for len(encoded) > 0 {
		lineLength := min(armourLineLength, len(encoded))
		out.WriteString(encoded[:lineLength])
		out.WriteByte('\n')
		encoded = encoded[lineLength:]
	}
	out.WriteString(armourFooter)
	return out.Bytes()
}
