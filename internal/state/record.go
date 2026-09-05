package state

import "time"

// ProjectRecord is the contents of project.json. It holds what prison
// knows about a project.
type ProjectRecord struct {
	Path                string    `json:"path"`
	Created             time.Time `json:"created"`
	TrustedConfigSHA256 string    `json:"trusted_config_sha256,omitempty"`
	PortBlock           int       `json:"port_block,omitempty"`
	SetupSignature      string    `json:"setup_signature,omitempty"`
	Box                 *BoxShape `json:"box,omitempty"`
}

// BoxShape records the settings a box was created with. Ports pairs
// host and guest port numbers.
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

// Profile is the contents of profile.json. It holds what the broker
// needs to know about a project.
type Profile struct {
	Inmates []InmateRoute `json:"inmates"`
	Egress  []string      `json:"egress"`
	Written time.Time     `json:"written"`
}

// InmateRoute describes one enabled inmate for the broker. It maps a
// name to an upstream host, an optional path prefix, and credentials.
type InmateRoute struct {
	Name        string           `json:"name"`
	Upstream    string           `json:"upstream"`
	PathPrefix  string           `json:"path_prefix,omitempty"`
	Credentials []CredentialSpec `json:"credentials,omitempty"`
}

// CredentialSpec ties a host variable to the header and optional
// prefix that carry it upstream.
type CredentialSpec struct {
	Variable string `json:"variable"`
	Header   string `json:"header"`
	Prefix   string `json:"prefix,omitempty"`
}
