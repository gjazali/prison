// Package vault stores secrets and per-host certificate authorities
// in an encrypted file at ~/.prison/secrets.vault.
package vault

import "time"

// Secret modes.
const (
	ModeRoute  = "route"
	ModeSign   = "sign"
	ModeExpose = "expose"
)

// Signing algorithms.
const (
	AlgorithmSSHAgent   = "ssh-agent"
	AlgorithmHMACSHA256 = "hmac-sha256"
)

// ReservedSecretName is the name the broker reserves for itself.
const ReservedSecretName = "prison"

// Document is the decrypted content of the vault.
type Document struct {
	Version     int                   `json:"version"`
	Secrets     map[string]*Secret    `json:"secrets"`
	Authorities map[string]*Authority `json:"authorities"`
}

// Secret is one vault record. One of Route, Sign, or Expose is set,
// matching Mode. Value is empty for ssh-agent secrets.
type Secret struct {
	Name        string      `json:"name"`
	Description string      `json:"description,omitempty"`
	Mode        string      `json:"mode"`
	Confirm     bool        `json:"confirm"`
	Created     time.Time   `json:"created"`
	Value       string      `json:"value,omitempty"`
	Route       *RouteSpec  `json:"route,omitempty"`
	Sign        *SignSpec   `json:"sign,omitempty"`
	Expose      *ExposeSpec `json:"expose,omitempty"`
}

// RouteSpec holds the fields of a route secret. Empty Methods means
// all methods. Zero RateLimit means no limit.
type RouteSpec struct {
	Upstream         string   `json:"upstream"`
	PathPrefix       string   `json:"path_prefix"`
	Header           string   `json:"header"`
	Prefix           string   `json:"prefix,omitempty"`
	BaseURLVariable  string   `json:"base_url_variable,omitempty"`
	TokenVariable    string   `json:"token_variable,omitempty"`
	TokenPlaceholder string   `json:"token_placeholder,omitempty"`
	Methods          []string `json:"methods,omitempty"`
	RateLimit        int      `json:"rate_limit,omitempty"`
	Intercept        bool     `json:"intercept"`
}

// SignSpec holds the fields of a signing secret. Fingerprint is the
// SHA256 hash of an agent key. Namespace is the SSHSIG namespace.
type SignSpec struct {
	Algorithm   string `json:"algorithm"`
	Fingerprint string `json:"fingerprint,omitempty"`
	Namespace   string `json:"namespace,omitempty"`
	RateLimit   int    `json:"rate_limit,omitempty"`
}

// ExposeSpec holds the variable name an exposed secret is set as.
type ExposeSpec struct {
	Variable string `json:"variable"`
}

// Authority is a per-host certificate authority. Both fields are PEM.
type Authority struct {
	KeyPEM         string    `json:"key"`
	CertificatePEM string    `json:"certificate"`
	Created        time.Time `json:"created"`
}
