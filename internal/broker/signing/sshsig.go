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

const sshsigMagic = "SSHSIG"

const sshsigVersion uint32 = 1

const sshsigHashAlgorithm = "sha512"

const defaultNamespace = "git"

const armourLineLength = 70

const (
	armourHeader = "-----BEGIN SSH SIGNATURE-----\n"
	armourFooter = "-----END SSH SIGNATURE-----\n"
)

// SSHSignature holds Armored text that is ready for a `.sig` file.
type SSHSignature struct {
	Armored   []byte
	PublicKey string
	Namespace string
}

type sshsigSignedData struct {
	Namespace     string
	Reserved      string
	HashAlgorithm string
	Hash          []byte
}

type sshsigBlob struct {
	Version       uint32
	PublicKey     []byte
	Namespace     string
	Reserved      string
	HashAlgorithm string
	Signature     []byte
}

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
			return fmt.Errorf("the ssh agent did not sign with %s. "+
				"Approve the key on the host: %w", fingerprint, err)
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

// signWithAgent avoids SHA-1 `ssh-rsa` signatures for RSA keys.
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
