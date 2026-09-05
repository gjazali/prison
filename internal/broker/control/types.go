// Package control defines the broker's control API types, a typed
// client, and the logic for starting or reusing a broker.
package control

import (
	"fmt"

	"prison/internal/state"
)

// Vault status strings used by Status.
const (
	VaultAbsent   = "absent"
	VaultLocked   = "locked"
	VaultUnlocked = "unlocked"
)

// Status is the response body of GET /status.
type Status struct {
	Version             string     `json:"version"`
	StateRoot           string     `json:"state_root"`
	PID                 int        `json:"pid"`
	Listeners           []Listener `json:"listeners"`
	Vault               string     `json:"vault"`
	Projects            []string   `json:"projects"`
	CredentialVariables []string   `json:"credential_variables"`
}

// Listener describes one TCP listener the broker was asked to bind.
type Listener struct {
	Address string `json:"address"`
	Port    int    `json:"port,omitempty"`
	Bound   bool   `json:"bound"`
	Error   string `json:"error,omitempty"`
}

// ListenRequest is the request body of POST /listen.
type ListenRequest struct {
	Address string `json:"address"`
}

// ProjectUpdate is the request body of PUT /projects/{id}. A nil
// Profile leaves profile.json alone. Credentials are merged into
// memory and never written to disk.
type ProjectUpdate struct {
	Profile     *state.Profile    `json:"profile,omitempty"`
	Credentials map[string]string `json:"credentials,omitempty"`
}

// Environment is the response body of GET /projects/{id}/environment.
// Variables holds NAME=value strings sorted by name.
type Environment struct {
	Variables []string `json:"variables"`
}

// UnlockRequest is the request body of POST /vault/unlock.
type UnlockRequest struct {
	Passphrase string `json:"passphrase"`
}

// LogQuery filters log entries for GET /log. Empty strings match
// everything. A zero Limit returns all matches.
type LogQuery struct {
	Kind    string
	Project string
	Secret  string
	Inmate  string
	Limit   int
}

// ErrorResponse is the response body for non-2xx answers.
type ErrorResponse struct {
	Error ErrorDetail `json:"error"`
}

// ErrorDetail holds the message inside an ErrorResponse.
type ErrorDetail struct {
	Message string `json:"message"`
}

// Error represents a non-2xx response from the broker.
type Error struct {
	Status  int
	Message string
}

// Error returns the broker's error message.
func (err *Error) Error() string {
	if err.Message == "" {
		return fmt.Sprintf("the broker answered %d", err.Status)
	}
	return err.Message
}
