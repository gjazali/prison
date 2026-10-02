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

const (
	aliveTimeout   = 2 * time.Second
	pollInterval   = 100 * time.Millisecond
	startupTimeout = 5 * time.Second
)

// baseURL is a placeholder because the transport always dials the
// socket.
const baseURL = "http://prison-broker"

type Client struct {
	socketPath string
	http       *http.Client
}

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

func (client *Client) SocketPath() string {
	return client.socketPath
}

func (client *Client) Alive(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, aliveTimeout)
	defer cancel()
	_, err := client.Status(ctx)
	return err == nil
}

func (client *Client) Status(ctx context.Context) (*Status, error) {
	var status Status
	if err := client.do(ctx, http.MethodGet, "/status", nil, &status); err != nil {
		return nil, err
	}
	return &status, nil
}

// Listen can return a listener with Bound false while the broker
// retries in the background.
func (client *Client) Listen(ctx context.Context, address string) (*Listener, error) {
	var listener Listener
	err := client.do(ctx, http.MethodPost, "/listen",
		ListenRequest{Address: address}, &listener)
	if err != nil {
		return nil, err
	}
	return &listener, nil
}

func (client *Client) UpdateProject(ctx context.Context, projectID string,
	update ProjectUpdate) error {
	return client.do(ctx, http.MethodPut, "/projects/"+url.PathEscape(projectID),
		update, nil)
}

// ProjectEnvironment returns no variables while the vault is locked.
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

// Unlock returns an `*Error` with status 400 for a wrong passphrase.
func (client *Client) Unlock(ctx context.Context, passphrase string) error {
	return client.do(ctx, http.MethodPost, "/vault/unlock",
		UnlockRequest{Passphrase: passphrase}, nil)
}

func (client *Client) Reload(ctx context.Context) error {
	return client.do(ctx, http.MethodPost, "/reload", nil, nil)
}

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

// Publish replaces the forwarded ports of a box.
func (client *Client) Publish(ctx context.Context, box string,
	request PublishRequest) error {
	return client.do(ctx, http.MethodPut, "/publish/"+url.PathEscape(box),
		request, nil)
}

func (client *Client) Unpublish(ctx context.Context, box string) error {
	return client.do(ctx, http.MethodDelete,
		"/publish/"+url.PathEscape(box), nil, nil)
}

// Shutdown returns when the broker acknowledges the request. The broker
// can still be running at that time.
func (client *Client) Shutdown(ctx context.Context) error {
	return client.do(ctx, http.MethodPost, "/shutdown", nil, nil)
}

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
	if out == nil || response.StatusCode == http.StatusNoContent {
		io.Copy(io.Discard, response.Body)
		return nil
	}
	if err := json.NewDecoder(response.Body).Decode(out); err != nil {
		return fmt.Errorf("the broker response to %s %s is not valid JSON: %w",
			method, path, err)
	}
	return nil
}

func decodeError(response *http.Response) error {
	var envelope ErrorResponse
	data, _ := io.ReadAll(io.LimitReader(response.Body, 64<<10))
	if json.Unmarshal(data, &envelope) == nil && envelope.Error.Message != "" {
		return &Error{Status: response.StatusCode, Message: envelope.Error.Message}
	}
	return &Error{Status: response.StatusCode}
}

// EnsureRunning reuses a live broker of the same version. It stops a
// broker of another version and calls spawn to start a new one.
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
			return nil, fmt.Errorf("a broker from another prison version "+
				"did not stop: %w. Run `prison broker stop`", err)
		}
		if !waitUntil(ctx, startupTimeout, func() bool { return !client.Alive(ctx) }) {
			return nil, fmt.Errorf("a broker from another prison version "+
				"did not stop on %s. Run `prison broker stop`", socketPath)
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
		return nil, fmt.Errorf("the broker did not start on %s in %s. "+
			"Read the broker log in %s", socketPath, startupTimeout, root.Path)
	}
	return client, nil
}

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
