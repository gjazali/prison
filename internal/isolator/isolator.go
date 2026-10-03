package isolator

import (
	"context"
	"io"
)

// BoxInfo describes one box. `Address` and `Network` are empty when the box is
// not running or the value is unknown.
type BoxInfo struct {
	Name    string
	Exists  bool
	Running bool
	Network string
	Address string
	Image   string
}

// NetworkInfo describes a box network. `Gateway` is the host address that
// guests use.
type NetworkInfo struct {
	Name     string
	Exists   bool
	HostOnly bool
	Gateway  string
	SubnetV4 string
	SubnetV6 string
}

type Mount struct {
	Source   string
	Target   string
	ReadOnly bool
}

type PortMapping struct {
	Host  int
	Guest int
}

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

// ExecSpec uses the matching stream of the prison process for a nil stream.
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

// BuildSpec describes one image build. `Dockerfile` is relative to `Context`.
// An empty `Dockerfile` selects the default of the isolator.
type BuildSpec struct {
	Tag        string
	Context    string
	Dockerfile string
	Args       map[string]string
	Output     io.Writer
}

type RouteInfo struct {
	Network   string
	Prefix    int
	Interface string
}

// DNSDomain registers a local domain so that box names resolve on the host.
type DNSDomain interface {
	Exists(ctx context.Context, domain string) (bool, error)
	Describe(domain string) []string
	Register(ctx context.Context, domain string) error
	RepairHint(domain string) []string
}

// HostRoute repairs the host route to a box network when apple-container
// loses it.
type HostRoute interface {
	Describe(ctx context.Context, gateway string) (*RouteInfo, error)
	Installed(ctx context.Context, gateway string) (bool, error)
	Command(ctx context.Context, gateway string) (string, error)
	Install(ctx context.Context, gateway string) error
}

// Base is the part of `Isolator` that every OS shares. `Create` and `Start`
// return only when the box accepts `Exec`. `Exec` returns an error only when
// the command cannot run. `ReleaseBuilder` frees the resources that the
// isolator keeps between builds.
type Base interface {
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
	ReleaseBuilder(ctx context.Context) error

	Network(ctx context.Context, name string) (NetworkInfo, error)
	EnsureNetwork(ctx context.Context, name string) error

	Doctor(ctx context.Context, w io.Writer)
}
