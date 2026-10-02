// Package vault stores secrets and per-host certificate authorities
// in an encrypted file at `~/.prison/secrets.vault`.
package vault

import "time"

const (
	ModeRoute  = "route"
	ModeSign   = "sign"
	ModeExpose = "expose"
)

const (
	AlgorithmSSHAgent   = "ssh-agent"
	AlgorithmHMACSHA256 = "hmac-sha256"
)

const ReservedSecretName = "prison"

type Document struct {
	Version     int                   `json:"version"`
	Secrets     map[string]*Secret    `json:"secrets"`
	Authorities map[string]*Authority `json:"authorities"`
}

// Secret is one vault record. Only the spec that matches `Mode` is set.
// `Value` is empty for `ssh-agent` secrets.
type Secret struct {
	Name        string      `json:"name"`
	Description string      `json:"description,omitempty"`
	Mode        string      `json:"mode"`
	Created     time.Time   `json:"created"`
	Value       string      `json:"value,omitempty"`
	Route       *RouteSpec  `json:"route,omitempty"`
	Sign        *SignSpec   `json:"sign,omitempty"`
	Expose      *ExposeSpec `json:"expose,omitempty"`
}

// RouteSpec allows all methods when `Methods` is empty. A zero
// `RateLimit` means no limit.
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

// SignSpec names an agent key by its SHA256 `Fingerprint`. `Namespace`
// is the SSHSIG namespace.
type SignSpec struct {
	Algorithm   string `json:"algorithm"`
	Fingerprint string `json:"fingerprint,omitempty"`
	Namespace   string `json:"namespace,omitempty"`
	RateLimit   int    `json:"rate_limit,omitempty"`
}

type ExposeSpec struct {
	Variable string `json:"variable"`
}

type Authority struct {
	KeyPEM         string    `json:"key"`
	CertificatePEM string    `json:"certificate"`
	Created        time.Time `json:"created"`
}
