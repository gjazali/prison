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

type AgentKey struct {
	Fingerprint string
	PublicKey   string
	Comment     string
	blob        []byte
}

type Agent struct {
	socketPath string
}

func NewAgent(socketPath string) *Agent {
	return &Agent{socketPath: socketPath}
}

func (a *Agent) Available() bool {
	return a != nil && a.socketPath != ""
}

func (a *Agent) Keys(ctx context.Context) ([]AgentKey, error) {
	var found []AgentKey
	err := a.withClient(ctx, func(client agent.ExtendedAgent) error {
		listed, err := client.List()
		if err != nil {
			return fmt.Errorf("cannot list the ssh agent keys: %w", err)
		}
		found = make([]AgentKey, 0, len(listed))
		for _, key := range listed {
			found = append(found, describeAgentKey(key))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return found, nil
}

func (a *Agent) FindKey(
	ctx context.Context, fingerprint string,
) (*AgentKey, error) {
	var found *AgentKey
	err := a.withClient(ctx, func(client agent.ExtendedAgent) error {
		key, err := findAgentKey(client, fingerprint)
		if err != nil {
			return err
		}
		described := describeAgentKey(key)
		found = &described
		return nil
	})
	if err != nil {
		return nil, err
	}
	return found, nil
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

func describeAgentKey(key *agent.Key) AgentKey {
	return AgentKey{
		Fingerprint: ssh.FingerprintSHA256(key),
		PublicKey: key.Type() + " " +
			base64.StdEncoding.EncodeToString(key.Blob),
		Comment: key.Comment,
		blob:    key.Blob,
	}
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
