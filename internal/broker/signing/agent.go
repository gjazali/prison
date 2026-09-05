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

// agentTimeout is the deadline for each SSH agent exchange.
const agentTimeout = 30 * time.Second

// AgentKey describes one public key held by the SSH agent.
type AgentKey struct {
	Fingerprint string
	PublicKey   string
	Comment     string
	blob        []byte
}

// Agent is a client for the SSH agent on a Unix socket. Each method
// dials, does one exchange, and closes.
type Agent struct {
	socketPath string
}

// NewAgent returns an Agent for the given socket path. An empty path
// produces an Agent whose methods always fail.
func NewAgent(socketPath string) *Agent {
	return &Agent{socketPath: socketPath}
}

// Available returns true if a socket path was given. Does not dial.
func (a *Agent) Available() bool {
	return a != nil && a.socketPath != ""
}

// Keys lists all keys held by the agent. Returns an error if the
// agent is unreachable or refuses to list keys.
func (a *Agent) Keys(ctx context.Context) ([]AgentKey, error) {
	var found []AgentKey
	err := a.withClient(ctx, func(client agent.ExtendedAgent) error {
		listed, err := client.List()
		if err != nil {
			return fmt.Errorf("the ssh agent would not list its keys: %w",
				err)
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

// FindKey returns the agent key matching the given SHA256 fingerprint.
// Returns an error if the key is not found or the agent is
// unreachable.
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

// findAgentKey finds the key matching the given fingerprint from the
// agent client. Returns an error if no key matches.
func findAgentKey(
	client agent.ExtendedAgent, fingerprint string,
) (*agent.Key, error) {
	listed, err := client.List()
	if err != nil {
		return nil, fmt.Errorf("the ssh agent would not list its keys: %w",
			err)
	}
	for _, key := range listed {
		if ssh.FingerprintSHA256(key) == fingerprint {
			return key, nil
		}
	}
	return nil, fmt.Errorf(
		"no key with fingerprint %s is loaded in the ssh agent", fingerprint)
}

// describeAgentKey converts an agent.Key into an AgentKey.
func describeAgentKey(key *agent.Key) AgentKey {
	return AgentKey{
		Fingerprint: ssh.FingerprintSHA256(key),
		PublicKey: key.Type() + " " +
			base64.StdEncoding.EncodeToString(key.Blob),
		Comment: key.Comment,
		blob:    key.Blob,
	}
}

// withClient dials the agent, runs the exchange function, and closes
// the connection. Returns errors from dialing, timeouts, context
// cancellation, or the exchange itself.
func (a *Agent) withClient(
	ctx context.Context, exchange func(agent.ExtendedAgent) error,
) error {
	if !a.Available() {
		return errors.New("SSH_AUTH_SOCK is not set in the shell that " +
			"started the broker, so prison has no ssh agent to ask")
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
		return fmt.Errorf("cannot set a deadline on the ssh agent "+
			"connection: %w", err)
	}

	// Context cancellation closes the connection to unblock synchronous
	// agent reads.
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
		return fmt.Errorf("talking to the ssh agent at %s: %w",
			a.socketPath, ctx.Err())
	}
	var networkError net.Error
	if errors.As(err, &networkError) && networkError.Timeout() {
		return fmt.Errorf("the ssh agent at %s did not answer within %s; "+
			"a key that asks for confirmation needs it given on the host: %w",
			a.socketPath, agentTimeout, err)
	}
	return err
}
