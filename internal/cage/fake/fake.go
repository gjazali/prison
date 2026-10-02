// Package fake implements `cage.Cage` in memory for tests. It logs every call,
// and tests can script failures.
package fake

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"

	"prison/internal/cage"
)

const DefaultGateway = "192.168.128.1"

const DefaultSubnetV4 = "192.168.128.0/24"

type Cage struct {
	Boxes     map[string]cage.BoxInfo
	Networks  map[string]cage.NetworkInfo
	Images    map[string]bool
	LogOutput map[string]string
	Domains   map[string]bool

	Calls []string
	// Fail maps a method name, such as `Create`, to the error that it
	// returns.
	Fail map[string]error
	// ExecHandler sets the result of `Exec`. Nil means exit status 0.
	ExecHandler func(spec cage.ExecSpec) (int, error)

	// DeclaredCapabilities also controls whether `DNS` and `Route` return nil.
	DeclaredCapabilities cage.Capabilities
	// Unavailable makes `Available` return false and `Require` fail.
	Unavailable bool

	CreateSpecs []cage.CreateSpec
	ExecSpecs   []cage.ExecSpec
	BuildSpecs  []cage.BuildSpec

	// RouteIsInstalled is what the route helper reports. `Install` sets it.
	RouteIsInstalled bool

	addressCounter int
}

func New() *Cage {
	return &Cage{
		Boxes:     map[string]cage.BoxInfo{},
		Networks:  map[string]cage.NetworkInfo{},
		Images:    map[string]bool{},
		LogOutput: map[string]string{},
		Domains:   map[string]bool{},
		Fail:      map[string]error{},
		DeclaredCapabilities: cage.Capabilities{
			Isolation:       cage.IsolationVM,
			GuestAddresses:  true,
			GuestHostnames:  true,
			DNSDomain:       true,
			RouteRepair:     true,
			HostOnlyNetwork: true,
			HostFirewall:    true,
		},
	}
}

func (fakeCage *Cage) record(method string, arguments ...string) error {
	line := method
	if len(arguments) > 0 {
		line += " " + strings.Join(arguments, " ")
	}
	fakeCage.Calls = append(fakeCage.Calls, line)
	return fakeCage.Fail[method]
}

func (fakeCage *Cage) nextAddress() string {
	fakeCage.addressCounter++
	return fmt.Sprintf("192.168.128.%d", fakeCage.addressCounter+1)
}

func (fakeCage *Cage) Name() string { return "fake" }

func (fakeCage *Cage) Description() string {
	return "in-memory cage for tests"
}

func (fakeCage *Cage) Capabilities() cage.Capabilities {
	return fakeCage.DeclaredCapabilities
}

func (fakeCage *Cage) Available() bool { return !fakeCage.Unavailable }

func (fakeCage *Cage) Require(ctx context.Context) error {
	if err := fakeCage.record("Require"); err != nil {
		return err
	}
	if fakeCage.Unavailable {
		return fmt.Errorf("fake cage is unavailable")
	}
	return nil
}

func (fakeCage *Cage) Box(
	ctx context.Context, name string,
) (cage.BoxInfo, error) {
	if err := fakeCage.record("Box", name); err != nil {
		return cage.BoxInfo{}, err
	}
	if info, present := fakeCage.Boxes[name]; present {
		return info, nil
	}
	return cage.BoxInfo{Name: name}, nil
}

func (fakeCage *Cage) ListBoxes(
	ctx context.Context,
) ([]cage.BoxInfo, error) {
	if err := fakeCage.record("ListBoxes"); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(fakeCage.Boxes))
	for name := range fakeCage.Boxes {
		names = append(names, name)
	}
	sort.Strings(names)
	boxes := make([]cage.BoxInfo, 0, len(names))
	for _, name := range names {
		boxes = append(boxes, fakeCage.Boxes[name])
	}
	return boxes, nil
}

func (fakeCage *Cage) Create(
	ctx context.Context, spec cage.CreateSpec,
) error {
	if err := fakeCage.record("Create", spec.Name); err != nil {
		return err
	}
	if _, present := fakeCage.Boxes[spec.Name]; present {
		return fmt.Errorf("box %s already exists", spec.Name)
	}
	fakeCage.CreateSpecs = append(fakeCage.CreateSpecs, spec)
	fakeCage.Boxes[spec.Name] = cage.BoxInfo{
		Name:    spec.Name,
		Exists:  true,
		Running: true,
		Network: spec.Network,
		Address: fakeCage.nextAddress(),
		Image:   spec.Image,
	}
	return nil
}

func (fakeCage *Cage) Start(ctx context.Context, name string) error {
	if err := fakeCage.record("Start", name); err != nil {
		return err
	}
	info, present := fakeCage.Boxes[name]
	if !present {
		return fmt.Errorf("box %s does not exist", name)
	}
	if info.Running {
		return nil
	}
	info.Running = true
	info.Address = fakeCage.nextAddress()
	fakeCage.Boxes[name] = info
	return nil
}

func (fakeCage *Cage) Stop(ctx context.Context, name string) error {
	if err := fakeCage.record("Stop", name); err != nil {
		return err
	}
	info, present := fakeCage.Boxes[name]
	if !present {
		return fmt.Errorf("box %s does not exist", name)
	}
	info.Running = false
	info.Address = ""
	fakeCage.Boxes[name] = info
	return nil
}

func (fakeCage *Cage) Delete(ctx context.Context, name string) error {
	if err := fakeCage.record("Delete", name); err != nil {
		return err
	}
	delete(fakeCage.Boxes, name)
	return nil
}

func (fakeCage *Cage) Logs(
	ctx context.Context, name string, w io.Writer,
) error {
	if err := fakeCage.record("Logs", name); err != nil {
		return err
	}
	if text := fakeCage.LogOutput[name]; text != "" {
		if _, err := io.WriteString(w, text); err != nil {
			return err
		}
	}
	return nil
}

func (fakeCage *Cage) Exec(
	ctx context.Context, spec cage.ExecSpec,
) (int, error) {
	if err := fakeCage.record("Exec", spec.Box); err != nil {
		return 0, err
	}
	fakeCage.ExecSpecs = append(fakeCage.ExecSpecs, spec)
	info, present := fakeCage.Boxes[spec.Box]
	if !present || !info.Running {
		return 0, fmt.Errorf("box %s is not running", spec.Box)
	}
	if fakeCage.ExecHandler == nil {
		return 0, nil
	}
	return fakeCage.ExecHandler(spec)
}

func (fakeCage *Cage) ImageExists(
	ctx context.Context, tag string,
) (bool, error) {
	if err := fakeCage.record("ImageExists", tag); err != nil {
		return false, err
	}
	return fakeCage.Images[tag], nil
}

func (fakeCage *Cage) Build(
	ctx context.Context, spec cage.BuildSpec,
) error {
	if err := fakeCage.record("Build", spec.Tag); err != nil {
		return err
	}
	fakeCage.BuildSpecs = append(fakeCage.BuildSpecs, spec)
	fakeCage.Images[spec.Tag] = true
	return nil
}

func (fakeCage *Cage) ReleaseBuilder(ctx context.Context) error {
	return fakeCage.record("ReleaseBuilder")
}

func (fakeCage *Cage) Network(
	ctx context.Context, name string,
) (cage.NetworkInfo, error) {
	if err := fakeCage.record("Network", name); err != nil {
		return cage.NetworkInfo{}, err
	}
	if info, present := fakeCage.Networks[name]; present {
		return info, nil
	}
	return cage.NetworkInfo{Name: name}, nil
}

func (fakeCage *Cage) EnsureNetwork(
	ctx context.Context, name string,
) error {
	if err := fakeCage.record("EnsureNetwork", name); err != nil {
		return err
	}
	if _, present := fakeCage.Networks[name]; present {
		return nil
	}
	fakeCage.Networks[name] = cage.NetworkInfo{
		Name:     name,
		Exists:   true,
		HostOnly: true,
		Gateway:  DefaultGateway,
		SubnetV4: DefaultSubnetV4,
	}
	return nil
}

func (fakeCage *Cage) DNS() cage.DNSDomain {
	if !fakeCage.DeclaredCapabilities.DNSDomain {
		return nil
	}
	return dnsHelper{owner: fakeCage}
}

func (fakeCage *Cage) Route() cage.HostRoute {
	if !fakeCage.DeclaredCapabilities.RouteRepair {
		return nil
	}
	return routeHelper{owner: fakeCage}
}

func (fakeCage *Cage) Doctor(ctx context.Context, w io.Writer) {
	_ = fakeCage.record("Doctor")
	fmt.Fprintf(w, "  fake cage, %d boxes, %d networks\n",
		len(fakeCage.Boxes), len(fakeCage.Networks))
}

type dnsHelper struct {
	owner *Cage
}

func (helper dnsHelper) Exists(
	ctx context.Context, domain string,
) (bool, error) {
	if err := helper.owner.record("DNS.Exists", domain); err != nil {
		return false, err
	}
	return helper.owner.Domains[domain], nil
}

func (helper dnsHelper) Describe(domain string) []string {
	_ = helper.owner.record("DNS.Describe", domain)
	return []string{"runs:   fake register " + domain}
}

func (helper dnsHelper) Register(
	ctx context.Context, domain string,
) error {
	if err := helper.owner.record("DNS.Register", domain); err != nil {
		return err
	}
	helper.owner.Domains[domain] = true
	return nil
}

func (helper dnsHelper) RepairHint(domain string) []string {
	_ = helper.owner.record("DNS.RepairHint", domain)
	return []string{"fake repair " + domain}
}

type routeHelper struct {
	owner *Cage
}

func (helper routeHelper) Describe(
	ctx context.Context, gateway string,
) (*cage.RouteInfo, error) {
	if err := helper.owner.record("Route.Describe", gateway); err != nil {
		return nil, err
	}
	return &cage.RouteInfo{
		Network:   "192.168.128.0",
		Prefix:    24,
		Interface: "bridge0",
	}, nil
}

func (helper routeHelper) Installed(
	ctx context.Context, gateway string,
) (bool, error) {
	if err := helper.owner.record("Route.Installed", gateway); err != nil {
		return false, err
	}
	return helper.owner.RouteIsInstalled, nil
}

func (helper routeHelper) Command(
	ctx context.Context, gateway string,
) (string, error) {
	if err := helper.owner.record("Route.Command", gateway); err != nil {
		return "", err
	}
	return "sudo route -n add -net 192.168.128.0/24 -interface bridge0", nil
}

func (helper routeHelper) Install(
	ctx context.Context, gateway string,
) error {
	if err := helper.owner.record("Route.Install", gateway); err != nil {
		return err
	}
	helper.owner.RouteIsInstalled = true
	return nil
}

var (
	_ cage.Cage      = (*Cage)(nil)
	_ cage.DNSDomain = dnsHelper{}
	_ cage.HostRoute = routeHelper{}
)
