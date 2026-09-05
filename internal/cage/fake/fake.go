// Package fake implements cage.Cage in memory for testing. Boxes,
// networks, and images live in maps. Calls are logged and failures
// can be scripted.
package fake

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"

	"prison/internal/cage"
)

// DefaultGateway is the gateway address for fake networks.
const DefaultGateway = "192.168.128.1"

// DefaultSubnetV4 is the IPv4 subnet for fake networks.
const DefaultSubnetV4 = "192.168.128.0/24"

// Cage is an in-memory cage. Construct with New and set the fields a
// test needs.
type Cage struct {
	// Boxes holds every box the cage knows, keyed by name.
	Boxes map[string]cage.BoxInfo
	// Networks holds every network the cage knows, keyed by name.
	Networks map[string]cage.NetworkInfo
	// Images records which image tags are present, keyed by tag.
	Images map[string]bool
	// LogOutput is what Logs writes for a box, keyed by box name.
	LogOutput map[string]string
	// Domains records which DNS domains are registered.
	Domains map[string]bool

	// Calls is the ordered log of method calls.
	Calls []string
	// Fail makes a named method return the given error. Keys are
	// method names like "Create".
	Fail map[string]error
	// ExecHandler decides what Exec does. Nil means exit 0.
	ExecHandler func(spec cage.ExecSpec) (int, error)

	// DeclaredCapabilities is what Capabilities returns. It also
	// controls whether DNS and Route return nil.
	DeclaredCapabilities cage.Capabilities
	// Unavailable makes Available return false and Require fail.
	Unavailable bool

	// CreateSpecs, ExecSpecs, and BuildSpecs record every spec passed
	// in, in call order.
	CreateSpecs []cage.CreateSpec
	ExecSpecs   []cage.ExecSpec
	BuildSpecs  []cage.BuildSpec

	// RouteIsInstalled is what the route helper reports. Install sets
	// it to true.
	RouteIsInstalled bool

	addressCounter int
}

// New returns a fake cage with empty maps and all capabilities
// enabled. Returns a ready-to-use Cage.
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

// record appends a line to the call log. Returns the scripted error
// for the method, or nil.
func (fakeCage *Cage) record(method string, arguments ...string) error {
	line := method
	if len(arguments) > 0 {
		line += " " + strings.Join(arguments, " ")
	}
	fakeCage.Calls = append(fakeCage.Calls, line)
	return fakeCage.Fail[method]
}

// nextAddress returns the next guest address in the fake subnet.
func (fakeCage *Cage) nextAddress() string {
	fakeCage.addressCounter++
	return fmt.Sprintf("192.168.128.%d", fakeCage.addressCounter+1)
}

// Name returns the cage's identifier.
func (fakeCage *Cage) Name() string { return "fake" }

// Description returns a short summary of the cage.
func (fakeCage *Cage) Description() string {
	return "in-memory cage for tests"
}

// Capabilities returns DeclaredCapabilities.
func (fakeCage *Cage) Capabilities() cage.Capabilities {
	return fakeCage.DeclaredCapabilities
}

// Available returns true unless Unavailable is set.
func (fakeCage *Cage) Available() bool { return !fakeCage.Unavailable }

// Require returns an error if the cage is unavailable or the test
// scripted a failure.
func (fakeCage *Cage) Require(ctx context.Context) error {
	if err := fakeCage.record("Require"); err != nil {
		return err
	}
	if fakeCage.Unavailable {
		return fmt.Errorf("fake cage is unavailable")
	}
	return nil
}

// Box returns info about a box by name. Unknown names come back with
// Exists false.
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

// ListBoxes returns all known boxes sorted by name.
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

// Create records the spec and adds a running box. Returns an error if
// the name already exists.
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

// Start marks a box running and assigns it an address. Does nothing
// if already running.
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

// Stop marks a box stopped and clears its address. Does nothing if
// already stopped.
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

// Delete removes a box. Deleting a missing box succeeds.
func (fakeCage *Cage) Delete(ctx context.Context, name string) error {
	if err := fakeCage.record("Delete", name); err != nil {
		return err
	}
	delete(fakeCage.Boxes, name)
	return nil
}

// Logs writes the LogOutput for the named box to w.
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

// Exec records the spec and calls ExecHandler. Without a handler the
// command exits 0.
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

// ImageExists reports whether the tag is present.
func (fakeCage *Cage) ImageExists(
	ctx context.Context, tag string,
) (bool, error) {
	if err := fakeCage.record("ImageExists", tag); err != nil {
		return false, err
	}
	return fakeCage.Images[tag], nil
}

// Build records the spec and marks the tag present.
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

// Network returns info about a network by name. Unknown names come
// back with Exists false.
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

// EnsureNetwork creates a host-only network if it does not exist.
// Does nothing if it is already there.
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

// DNS returns the fake domain registry, or nil if the capability is
// off.
func (fakeCage *Cage) DNS() cage.DNSDomain {
	if !fakeCage.DeclaredCapabilities.DNSDomain {
		return nil
	}
	return dnsHelper{owner: fakeCage}
}

// Route returns the fake route helper, or nil if the capability is
// off.
func (fakeCage *Cage) Route() cage.HostRoute {
	if !fakeCage.DeclaredCapabilities.RouteRepair {
		return nil
	}
	return routeHelper{owner: fakeCage}
}

// Doctor writes a summary line to w.
func (fakeCage *Cage) Doctor(ctx context.Context, w io.Writer) {
	_ = fakeCage.record("Doctor")
	fmt.Fprintf(w, "  fake cage, %d boxes, %d networks\n",
		len(fakeCage.Boxes), len(fakeCage.Networks))
}

// dnsHelper is the fake's cage.DNSDomain implementation.
type dnsHelper struct {
	owner *Cage
}

// Exists reports whether the domain is registered.
func (helper dnsHelper) Exists(
	ctx context.Context, domain string,
) (bool, error) {
	if err := helper.owner.record("DNS.Exists", domain); err != nil {
		return false, err
	}
	return helper.owner.Domains[domain], nil
}

// Describe returns lines explaining what registering the domain does.
func (helper dnsHelper) Describe(domain string) []string {
	_ = helper.owner.record("DNS.Describe", domain)
	return []string{"runs:   fake register " + domain}
}

// Register marks the domain registered.
func (helper dnsHelper) Register(
	ctx context.Context, domain string,
) error {
	if err := helper.owner.record("DNS.Register", domain); err != nil {
		return err
	}
	helper.owner.Domains[domain] = true
	return nil
}

// RepairHint returns lines to print when a domain stops resolving.
func (helper dnsHelper) RepairHint(domain string) []string {
	_ = helper.owner.record("DNS.RepairHint", domain)
	return []string{"fake repair " + domain}
}

// routeHelper is the fake's cage.HostRoute implementation.
type routeHelper struct {
	owner *Cage
}

// Describe returns a fixed /24 route over a fake interface.
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

// Installed returns the RouteIsInstalled flag.
func (helper routeHelper) Installed(
	ctx context.Context, gateway string,
) (bool, error) {
	if err := helper.owner.record("Route.Installed", gateway); err != nil {
		return false, err
	}
	return helper.owner.RouteIsInstalled, nil
}

// Command returns the route add command string.
func (helper routeHelper) Command(
	ctx context.Context, gateway string,
) (string, error) {
	if err := helper.owner.record("Route.Command", gateway); err != nil {
		return "", err
	}
	return "sudo route -n add -net 192.168.128.0/24 -interface bridge0", nil
}

// Install sets RouteIsInstalled to true.
func (helper routeHelper) Install(
	ctx context.Context, gateway string,
) error {
	if err := helper.owner.record("Route.Install", gateway); err != nil {
		return err
	}
	helper.owner.RouteIsInstalled = true
	return nil
}

// Compile-time interface checks.
var (
	_ cage.Cage      = (*Cage)(nil)
	_ cage.DNSDomain = dnsHelper{}
	_ cage.HostRoute = routeHelper{}
)
