package control

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"

	"prison/internal/broker/brokerlog"
	"prison/internal/state"
)

// Timing for liveness checks and startup polling.
const (
	aliveTimeout   = 2 * time.Second
	pollInterval   = 100 * time.Millisecond
	startupTimeout = 5 * time.Second
)

// baseURL is the placeholder authority for requests. The transport
// dials the socket instead.
const baseURL = "http://prison-broker"

// Client talks to a broker over its Unix socket.
type Client struct {
	socketPath string
	http       *http.Client
}

// NewClient returns a client for the broker at socketPath. No
// connection is made until a method is called.
func NewClient(socketPath string) *Client {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, "unix", socketPath)
		},
		MaxIdleConns: 2,
	}
	return &Client{
		socketPath: socketPath,
		http:       &http.Client{Transport: transport},
	}
}

// SocketPath returns the socket path the client dials.
func (client *Client) SocketPath() string {
	return client.socketPath
}

// Alive reports whether the broker answers within two seconds. Any
// failure returns false.
func (client *Client) Alive(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, aliveTimeout)
	defer cancel()
	_, err := client.Status(ctx)
	return err == nil
}

// Status fetches the broker's status. Returns the status and an
// error.
func (client *Client) Status(ctx context.Context) (*Status, error) {
	var status Status
	if err := client.do(ctx, http.MethodGet, "/status", nil, &status); err != nil {
		return nil, err
	}
	return &status, nil
}

// Listen asks the broker to bind its TCP port on an address. Takes a
// context and an address string. Returns the listener state and an
// error. Bound may still be false while the broker retries.
func (client *Client) Listen(ctx context.Context, address string) (*Listener, error) {
	var listener Listener
	err := client.do(ctx, http.MethodPost, "/listen",
		ListenRequest{Address: address}, &listener)
	if err != nil {
		return nil, err
	}
	return &listener, nil
}

// UpdateProject pushes a project's profile and credentials to the
// broker and reloads it. Takes a context, project ID, and update.
// Returns an error.
func (client *Client) UpdateProject(ctx context.Context, projectID string,
	update ProjectUpdate) error {
	return client.do(ctx, http.MethodPut, "/projects/"+url.PathEscape(projectID),
		update, nil)
}

// ProjectEnvironment returns the sorted NAME=value pairs an exec
// should set for the project. Empty when the vault is locked.
func (client *Client) ProjectEnvironment(ctx context.Context,
	projectID string) ([]string, error) {
	var environment Environment
	err := client.do(ctx, http.MethodGet,
		"/projects/"+url.PathEscape(projectID)+"/environment", nil, &environment)
	if err != nil {
		return nil, err
	}
	return environment.Variables, nil
}

// Unlock opens the vault with the given passphrase. A wrong
// passphrase returns an *Error with status 400.
func (client *Client) Unlock(ctx context.Context, passphrase string) error {
	return client.do(ctx, http.MethodPost, "/vault/unlock",
		UnlockRequest{Passphrase: passphrase}, nil)
}

// Reload tells the broker to re-read all project state. Returns an
// error.
func (client *Client) Reload(ctx context.Context) error {
	return client.do(ctx, http.MethodPost, "/reload", nil, nil)
}

// Log reads the broker log, filtered by query. Returns the matching
// entries and an error.
func (client *Client) Log(ctx context.Context, query LogQuery) ([]brokerlog.Entry, error) {
	values := url.Values{}
	if query.Kind != "" {
		values.Set("kind", query.Kind)
	}
	if query.Project != "" {
		values.Set("project", query.Project)
	}
	if query.Secret != "" {
		values.Set("secret", query.Secret)
	}
	if query.Inmate != "" {
		values.Set("inmate", query.Inmate)
	}
	if query.Limit > 0 {
		values.Set("limit", strconv.Itoa(query.Limit))
	}
	path := "/log"
	if encoded := values.Encode(); encoded != "" {
		path += "?" + encoded
	}
	var entries []brokerlog.Entry
	if err := client.do(ctx, http.MethodGet, path, nil, &entries); err != nil {
		return nil, err
	}
	return entries, nil
}

// Shutdown asks the broker to exit cleanly. Returns once the broker
// acknowledges, not once it has exited.
func (client *Client) Shutdown(ctx context.Context) error {
	return client.do(ctx, http.MethodPost, "/shutdown", nil, nil)
}

// do sends one request with body encoded as JSON when it is not nil and
// decodes a 2xx answer into out when out is not nil. A non-2xx answer
// becomes an *Error carrying the broker's message; a transport failure
// is returned wrapped with the socket path.
func (client *Client) do(ctx context.Context, method, path string,
	body, out any) error {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("cannot encode the request to the broker: %w", err)
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, baseURL+path, reader)
	if err != nil {
		return fmt.Errorf("cannot build the request to the broker: %w", err)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.http.Do(request)
	if err != nil {
		return fmt.Errorf("cannot reach the broker at %s: %w", client.socketPath, err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return decodeError(response)
	}
	if out == nil {
		io.Copy(io.Discard, response.Body)
		return nil
	}
	if err := json.NewDecoder(response.Body).Decode(out); err != nil {
		return fmt.Errorf("the broker's answer to %s %s is not the JSON "+
			"prison expects: %w", method, path, err)
	}
	return nil
}

// decodeError turns a non-2xx response into an *Error, falling back to
// a message built from the status when the body is not the error shape.
func decodeError(response *http.Response) error {
	var envelope ErrorResponse
	data, _ := io.ReadAll(io.LimitReader(response.Body, 64<<10))
	if json.Unmarshal(data, &envelope) == nil && envelope.Error.Message != "" {
		return &Error{Status: response.StatusCode, Message: envelope.Error.Message}
	}
	return &Error{Status: response.StatusCode}
}

// EnsureRunning returns a client for a broker of the given version on
// root's socket, starting one when needed. A live broker of the same
// version is reused; one of another version is asked to shut down and
// waited for; a dead socket file is removed. spawn is then called to
// start the daemon, and the socket is polled every 100 milliseconds for
// up to five seconds. It returns an error when the old broker will not
// stop, spawn fails, or the new broker does not answer in time.
func EnsureRunning(ctx context.Context, root *state.Root, version string,
	spawn func() error) (*Client, error) {
	socketPath := root.BrokerSocket()
	client := NewClient(socketPath)
	if client.Alive(ctx) {
		status, err := client.Status(ctx)
		if err == nil && status.Version == version {
			return client, nil
		}
		if err := client.Shutdown(ctx); err != nil {
			return nil, fmt.Errorf("a broker from another prison version is "+
				"running and would not stop: %w; `prison broker stop` may help", err)
		}
		if !waitUntil(ctx, startupTimeout, func() bool { return !client.Alive(ctx) }) {
			return nil, fmt.Errorf("a broker from another prison version is "+
				"still answering on %s after being asked to stop; "+
				"`prison broker stop` may help", socketPath)
		}
	}
	if err := os.Remove(socketPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("cannot remove the stale broker socket %s: %w",
			socketPath, err)
	}
	if err := spawn(); err != nil {
		return nil, fmt.Errorf("cannot start the broker: %w", err)
	}
	if !waitUntil(ctx, startupTimeout, func() bool { return client.Alive(ctx) }) {
		return nil, fmt.Errorf("the broker did not start answering on %s "+
			"within %s; its log under %s may say why", socketPath,
			startupTimeout, root.Path)
	}
	return client, nil
}

// waitUntil polls condition every pollInterval until it is true, the
// timeout passes, or ctx ends, and reports whether it became true.
func waitUntil(ctx context.Context, timeout time.Duration,
	condition func() bool) bool {
	deadline := time.Now().Add(timeout)
	for {
		if condition() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(pollInterval):
		}
	}
}
