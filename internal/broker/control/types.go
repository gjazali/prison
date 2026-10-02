// Package control holds the broker's control API types and client. It
// also starts a broker or reuses a running one.
package control

import (
	"fmt"

	"prison/internal/state"
)

const (
	VaultAbsent   = "absent"
	VaultLocked   = "locked"
	VaultUnlocked = "unlocked"
)

type Status struct {
	Version             string     `json:"version"`
	StateRoot           string     `json:"state_root"`
	PID                 int        `json:"pid"`
	Listeners           []Listener `json:"listeners"`
	Vault               string     `json:"vault"`
	Projects            []string   `json:"projects"`
	CredentialVariables []string   `json:"credential_variables"`
}

type Listener struct {
	Address string `json:"address"`
	Port    int    `json:"port,omitempty"`
	Bound   bool   `json:"bound"`
	Error   string `json:"error,omitempty"`
}

type PortForward struct {
	Host  int `json:"host"`
	Guest int `json:"guest"`
}

// PublishRequest makes the broker forward host loopback ports to a box.
type PublishRequest struct {
	Address string        `json:"address"`
	Ports   []PortForward `json:"ports"`
}

type ListenRequest struct {
	Address string `json:"address"`
}

// ProjectUpdate leaves `profile.json` alone when Profile is nil. The
// broker keeps Credentials in memory only.
type ProjectUpdate struct {
	Profile     *state.Profile    `json:"profile,omitempty"`
	Credentials map[string]string `json:"credentials,omitempty"`
}

type Environment struct {
	Variables []string `json:"variables"`
}

type UnlockRequest struct {
	Passphrase string `json:"passphrase"`
}

type LogQuery struct {
	Kind    string
	Project string
	Secret  string
	Inmate  string
	Limit   int
}

type ErrorResponse struct {
	Error ErrorDetail `json:"error"`
}

type ErrorDetail struct {
	Message string `json:"message"`
}

type Error struct {
	Status  int
	Message string
}

func (err *Error) Error() string {
	if err.Message == "" {
		return fmt.Sprintf("the broker returned status %d", err.Status)
	}
	return err.Message
}
