package signing

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

const agentTimeout = 30 * time.Second

type Agent struct {
	socketPath string
}

func NewAgent(socketPath string) *Agent {
	return &Agent{socketPath: socketPath}
}

func (a *Agent) Available() bool {
	return a != nil && a.socketPath != ""
}

func (a *Agent) PublicKey(
	ctx context.Context, fingerprint string,
) (string, error) {
	var publicKey string
	err := a.withClient(ctx, func(client agent.ExtendedAgent) error {
		key, err := findAgentKey(client, fingerprint)
		if err != nil {
			return err
		}
		publicKey = authorizedKey(key)
		return nil
	})
	return publicKey, err
}

func findAgentKey(
	client agent.ExtendedAgent, fingerprint string,
) (*agent.Key, error) {
	listed, err := client.List()
	if err != nil {
		return nil, fmt.Errorf("cannot list the ssh agent keys: %w", err)
	}
	for _, key := range listed {
		if ssh.FingerprintSHA256(key) == fingerprint {
			return key, nil
		}
	}
	return nil, fmt.Errorf(
		"the ssh agent has no key %s", fingerprint)
}

func authorizedKey(key *agent.Key) string {
	return key.Type() + " " + base64.StdEncoding.EncodeToString(key.Blob)
}

func (a *Agent) withClient(
	ctx context.Context, exchange func(agent.ExtendedAgent) error,
) error {
	if !a.Available() {
		return errors.New("SSH_AUTH_SOCK is not set for the broker")
	}

	dialContext, cancelDial := context.WithTimeout(ctx, agentTimeout)
	defer cancelDial()

	var dialer net.Dialer
	connection, err := dialer.DialContext(dialContext, "unix", a.socketPath)
	if err != nil {
		return fmt.Errorf("cannot reach the ssh agent at %s: %w",
			a.socketPath, err)
	}
	defer connection.Close()
	if err := connection.SetDeadline(
		time.Now().Add(agentTimeout)); err != nil {
		return fmt.Errorf("cannot set the ssh agent deadline: %w", err)
	}

	// Agent reads block, so cancellation closes the connection to stop
	// them.
	finished := make(chan struct{})
	defer close(finished)
	go func() {
		select {
		case <-ctx.Done():
			connection.Close()
		case <-finished:
		}
	}()

	err = exchange(agent.NewClient(connection))
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return fmt.Errorf("ssh agent at %s: %w",
			a.socketPath, ctx.Err())
	}
	var networkError net.Error
	if errors.As(err, &networkError) && networkError.Timeout() {
		return fmt.Errorf("the ssh agent at %s did not answer in %s. "+
			"Approve the key on the host: %w", a.socketPath, agentTimeout, err)
	}
	return err
}
