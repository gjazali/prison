// Package fake implements `isolator.Isolator` in memory for tests. It logs
// every call, and tests can script failures.
package fake

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"

	"prison/internal/isolator"
)

const DefaultGateway = "192.168.128.1"

const DefaultSubnetV4 = "192.168.128.0/24"

type Isolator struct {
	Boxes     map[string]isolator.BoxInfo
	Networks  map[string]isolator.NetworkInfo
	Images    map[string]bool
	LogOutput map[string]string
	Domains   map[string]bool

	Calls []string
	// Fail maps a method name, such as `Create`, to the error that it
	// returns.
	Fail map[string]error
	// ExecHandler sets the result of `Exec`. Nil means exit status 0.
	ExecHandler func(spec isolator.ExecSpec) (int, error)

	Unavailable bool

	CreateSpecs []isolator.CreateSpec
	ExecSpecs   []isolator.ExecSpec
	BuildSpecs  []isolator.BuildSpec

	// RouteIsInstalled is what the route helper reports. `Install` sets it.
	RouteIsInstalled bool

	addressCounter int
}

func New() *Isolator {
	return &Isolator{
		Boxes:     map[string]isolator.BoxInfo{},
		Networks:  map[string]isolator.NetworkInfo{},
		Images:    map[string]bool{},
		LogOutput: map[string]string{},
		Domains:   map[string]bool{},
		Fail:      map[string]error{},
	}
}

func (fakeIsolator *Isolator) record(method string, arguments ...string) error {
	line := method
	if len(arguments) > 0 {
		line += " " + strings.Join(arguments, " ")
	}
	fakeIsolator.Calls = append(fakeIsolator.Calls, line)
	return fakeIsolator.Fail[method]
}

func (fakeIsolator *Isolator) nextAddress() string {
	fakeIsolator.addressCounter++
	return fmt.Sprintf("192.168.128.%d", fakeIsolator.addressCounter+1)
}

func (fakeIsolator *Isolator) Require(ctx context.Context) error {
	if err := fakeIsolator.record("Require"); err != nil {
		return err
	}
	if fakeIsolator.Unavailable {
		return fmt.Errorf("fake isolator is unavailable")
	}
	return nil
}

func (fakeIsolator *Isolator) Box(
	ctx context.Context, name string,
) (isolator.BoxInfo, error) {
	if err := fakeIsolator.record("Box", name); err != nil {
		return isolator.BoxInfo{}, err
	}
	if info, present := fakeIsolator.Boxes[name]; present {
		return info, nil
	}
	return isolator.BoxInfo{Name: name}, nil
}

func (fakeIsolator *Isolator) ListBoxes(
	ctx context.Context,
) ([]isolator.BoxInfo, error) {
	if err := fakeIsolator.record("ListBoxes"); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(fakeIsolator.Boxes))
	for name := range fakeIsolator.Boxes {
		names = append(names, name)
	}
	sort.Strings(names)
	boxes := make([]isolator.BoxInfo, 0, len(names))
	for _, name := range names {
		boxes = append(boxes, fakeIsolator.Boxes[name])
	}
	return boxes, nil
}

func (fakeIsolator *Isolator) Create(
	ctx context.Context, spec isolator.CreateSpec,
) error {
	if err := fakeIsolator.record("Create", spec.Name); err != nil {
		return err
	}
	if _, present := fakeIsolator.Boxes[spec.Name]; present {
		return fmt.Errorf("box %s already exists", spec.Name)
	}
	fakeIsolator.CreateSpecs = append(fakeIsolator.CreateSpecs, spec)
	fakeIsolator.Boxes[spec.Name] = isolator.BoxInfo{
		Name:    spec.Name,
		Exists:  true,
		Running: true,
		Network: spec.Network,
		Address: fakeIsolator.nextAddress(),
		Image:   spec.Image,
	}
	return nil
}

func (fakeIsolator *Isolator) Start(ctx context.Context, name string) error {
	if err := fakeIsolator.record("Start", name); err != nil {
		return err
	}
	info, present := fakeIsolator.Boxes[name]
	if !present {
		return fmt.Errorf("box %s does not exist", name)
	}
	if info.Running {
		return nil
	}
	info.Running = true
	info.Address = fakeIsolator.nextAddress()
	fakeIsolator.Boxes[name] = info
	return nil
}

func (fakeIsolator *Isolator) Stop(ctx context.Context, name string) error {
	if err := fakeIsolator.record("Stop", name); err != nil {
		return err
	}
	info, present := fakeIsolator.Boxes[name]
	if !present {
		return fmt.Errorf("box %s does not exist", name)
	}
	info.Running = false
	info.Address = ""
	fakeIsolator.Boxes[name] = info
	return nil
}

func (fakeIsolator *Isolator) Delete(ctx context.Context, name string) error {
	if err := fakeIsolator.record("Delete", name); err != nil {
		return err
	}
	delete(fakeIsolator.Boxes, name)
	return nil
}

func (fakeIsolator *Isolator) Logs(
	ctx context.Context, name string, w io.Writer,
) error {
	if err := fakeIsolator.record("Logs", name); err != nil {
		return err
	}
	if text := fakeIsolator.LogOutput[name]; text != "" {
		if _, err := io.WriteString(w, text); err != nil {
			return err
		}
	}
	return nil
}

func (fakeIsolator *Isolator) Exec(
	ctx context.Context, spec isolator.ExecSpec,
) (int, error) {
	if err := fakeIsolator.record("Exec", spec.Box); err != nil {
		return 0, err
	}
	fakeIsolator.ExecSpecs = append(fakeIsolator.ExecSpecs, spec)
	info, present := fakeIsolator.Boxes[spec.Box]
	if !present || !info.Running {
		return 0, fmt.Errorf("box %s is not running", spec.Box)
	}
	if fakeIsolator.ExecHandler == nil {
		return 0, nil
	}
	return fakeIsolator.ExecHandler(spec)
}

func (fakeIsolator *Isolator) ImageExists(
	ctx context.Context, tag string,
) (bool, error) {
	if err := fakeIsolator.record("ImageExists", tag); err != nil {
		return false, err
	}
	return fakeIsolator.Images[tag], nil
}

func (fakeIsolator *Isolator) Build(
	ctx context.Context, spec isolator.BuildSpec,
) error {
	if err := fakeIsolator.record("Build", spec.Tag); err != nil {
		return err
	}
	fakeIsolator.BuildSpecs = append(fakeIsolator.BuildSpecs, spec)
	fakeIsolator.Images[spec.Tag] = true
	return nil
}

func (fakeIsolator *Isolator) ReleaseBuilder(ctx context.Context) error {
	return fakeIsolator.record("ReleaseBuilder")
}

func (fakeIsolator *Isolator) Network(
	ctx context.Context, name string,
) (isolator.NetworkInfo, error) {
	if err := fakeIsolator.record("Network", name); err != nil {
		return isolator.NetworkInfo{}, err
	}
	if info, present := fakeIsolator.Networks[name]; present {
		return info, nil
	}
	return isolator.NetworkInfo{Name: name}, nil
}

func (fakeIsolator *Isolator) EnsureNetwork(
	ctx context.Context, name string,
) error {
	if err := fakeIsolator.record("EnsureNetwork", name); err != nil {
		return err
	}
	if _, present := fakeIsolator.Networks[name]; present {
		return nil
	}
	fakeIsolator.Networks[name] = isolator.NetworkInfo{
		Name:     name,
		Exists:   true,
		HostOnly: true,
		Gateway:  DefaultGateway,
		SubnetV4: DefaultSubnetV4,
	}
	return nil
}

func (fakeIsolator *Isolator) DNS() isolator.DNSDomain {
	return dnsHelper{owner: fakeIsolator}
}

func (fakeIsolator *Isolator) Route() isolator.HostRoute {
	return routeHelper{owner: fakeIsolator}
}

func (fakeIsolator *Isolator) Doctor(ctx context.Context, w io.Writer) {
	_ = fakeIsolator.record("Doctor")
	fmt.Fprintf(w, "  fake isolator, %d boxes, %d networks\n",
		len(fakeIsolator.Boxes), len(fakeIsolator.Networks))
}

type dnsHelper struct {
	owner *Isolator
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
	owner *Isolator
}

func (helper routeHelper) Describe(
	ctx context.Context, gateway string,
) (*isolator.RouteInfo, error) {
	if err := helper.owner.record("Route.Describe", gateway); err != nil {
		return nil, err
	}
	return &isolator.RouteInfo{
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
	_ isolator.Isolator  = (*Isolator)(nil)
	_ isolator.DNSDomain = dnsHelper{}
	_ isolator.HostRoute = routeHelper{}
)
