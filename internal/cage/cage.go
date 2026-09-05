// Package cage defines the interface for container backends that run
// boxes.
package cage

import (
	"context"
	"io"
)

// Isolation modes for how a backend separates boxes from the host.
const (
	IsolationVM        = "vm"
	IsolationNamespace = "namespace"
)

// Capabilities describes what a backend can do. Missing capabilities
// cause commands to be hidden or refused.
type Capabilities struct {
	Isolation       string
	GuestAddresses  bool
	GuestHostnames  bool
	DNSDomain       bool
	RouteRepair     bool
	HostOnlyNetwork bool
	HostFirewall    bool
}

// BoxInfo describes one box. Address and Network are empty when
// unknown or not running.
type BoxInfo struct {
	Name    string
	Exists  bool
	Running bool
	Network string
	Address string
	Image   string
}

// NetworkInfo describes a box network. Gateway is the host address
// guests use. Subnets are CIDR strings or empty.
type NetworkInfo struct {
	Name     string
	Exists   bool
	HostOnly bool
	Gateway  string
	SubnetV4 string
	SubnetV6 string
}

// Mount is one bind mount from host to guest.
type Mount struct {
	Source   string
	Target   string
	ReadOnly bool
}

// PortMapping maps a host port to a guest port.
type PortMapping struct {
	Host  int
	Guest int
}

// CreateSpec holds the parameters for creating a box.
type CreateSpec struct {
	Name         string
	Image        string
	Network      string
	CPUs         int
	Memory       string
	Mounts       []Mount
	Ports        []PortMapping
	Environment  []string
	Capabilities []string
	Command      []string
}

// ExecSpec holds the parameters for running a command in a box.
// Nil streams default to the process's own.
type ExecSpec struct {
	Box         string
	Command     []string
	TTY         bool
	Interactive bool
	WorkDir     string
	UID         int
	GID         int
	Environment []string
	Stdin       io.Reader
	Stdout      io.Writer
	Stderr      io.Writer
}

// BuildSpec builds one image from a Dockerfile context on disk.
// Dockerfile names the file within the context, relative to it, and is
// empty when the backend's own default is wanted.
type BuildSpec struct {
	Tag        string
	Context    string
	Dockerfile string
	Args       map[string]string
	Output     io.Writer
}

// RouteInfo describes the host route to a box network.
type RouteInfo struct {
	Network   string
	Prefix    int
	Interface string
}

// DNSDomain registers a local domain so box names resolve on the host.
// Cages without the capability return nil from Cage.DNS.
type DNSDomain interface {
	Exists(ctx context.Context, domain string) (bool, error)
	Describe(domain string) []string
	Register(ctx context.Context, domain string) error
	RepairHint(domain string) []string
}

// HostRoute repairs the host's route to a box network when the backend
// loses it. Cages without the capability return nil from Cage.Route.
type HostRoute interface {
	Describe(ctx context.Context, gateway string) (*RouteInfo, error)
	Installed(ctx context.Context, gateway string) (bool, error)
	Command(ctx context.Context, gateway string) (string, error)
	Install(ctx context.Context, gateway string) error
}

// Cage is a container backend. Create and Start return only once the
// box accepts Exec. Exec returns the command's exit status and an error
// only when the command could not be run at all.
type Cage interface {
	Name() string
	Description() string
	Capabilities() Capabilities
	Available() bool
	Require(ctx context.Context) error

	Box(ctx context.Context, name string) (BoxInfo, error)
	ListBoxes(ctx context.Context) ([]BoxInfo, error)
	Create(ctx context.Context, spec CreateSpec) error
	Start(ctx context.Context, name string) error
	Stop(ctx context.Context, name string) error
	Delete(ctx context.Context, name string) error
	Logs(ctx context.Context, name string, w io.Writer) error
	Exec(ctx context.Context, spec ExecSpec) (int, error)

	ImageExists(ctx context.Context, tag string) (bool, error)
	Build(ctx context.Context, spec BuildSpec) error

	Network(ctx context.Context, name string) (NetworkInfo, error)
	EnsureNetwork(ctx context.Context, name string) error

	DNS() DNSDomain
	Route() HostRoute
	Doctor(ctx context.Context, w io.Writer)
}
