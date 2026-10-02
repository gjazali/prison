package state

import "time"

// ProjectRecord is the contents of `project.json`.
type ProjectRecord struct {
	Path                string    `json:"path"`
	Created             time.Time `json:"created"`
	TrustedConfigSHA256 string    `json:"trusted_config_sha256,omitempty"`
	PortBlock           int       `json:"port_block,omitempty"`
	SetupSignature      string    `json:"setup_signature,omitempty"`
	Box                 *BoxShape `json:"box,omitempty"`
}

// BoxShape records the values that a box was created with. Each port pair
// holds a host port and then a guest port.
type BoxShape struct {
	Image   string   `json:"image"`
	CPUs    int      `json:"cpus"`
	Memory  string   `json:"memory"`
	Sudo    bool     `json:"sudo"`
	Network string   `json:"network"`
	Shadow  []string `json:"shadow,omitempty"`
	Ports   [][2]int `json:"ports,omitempty"`
	Inmates []string `json:"inmates,omitempty"`
}

// Profile is the contents of `profile.json`, which the broker reads.
type Profile struct {
	Inmates []InmateRoute `json:"inmates"`
	Egress  []string      `json:"egress"`
	Written time.Time     `json:"written"`
}

type InmateRoute struct {
	Name        string           `json:"name"`
	Upstream    string           `json:"upstream"`
	PathPrefix  string           `json:"path_prefix,omitempty"`
	Credentials []CredentialSpec `json:"credentials,omitempty"`
}

type CredentialSpec struct {
	Variable string `json:"variable"`
	Header   string `json:"header"`
	Prefix   string `json:"prefix,omitempty"`
}
