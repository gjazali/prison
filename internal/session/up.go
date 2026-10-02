package session

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"prison"
	"prison/internal/broker/control"
	"prison/internal/cage"
	"prison/internal/config"
	"prison/internal/hostfw"
	"prison/internal/image"
	"prison/internal/plugin"
	"prison/internal/state"
	"prison/internal/ui"
	"prison/internal/vault"
)

type UpResult struct {
	Client   *control.Client
	Image    string
	Ports    []cage.PortMapping
	Network  cage.NetworkInfo
	Created  bool
	Shadow   []string
	Probe    ProbeResult
	Gateway  string
	Unlocked bool
}

func (s *Session) Up(ctx context.Context) (*UpResult, error) {
	if err := s.Cage.Require(ctx); err != nil {
		return nil, err
	}
	if s.Cage.Capabilities().Isolation != cage.IsolationVM {
		ui.Warn("the %s cage shares one kernel with the host", s.Cage.Name())
	}
	s.WarnAboutUntrustedConfiguration()

	record, err := s.ensureRecord()
	if err != nil {
		return nil, err
	}
	result := &UpResult{}

	client, err := s.ensureBroker(ctx)
	if err != nil {
		return nil, err
	}
	result.Client = client

	result.Unlocked, err = s.unlockVaultIfNeeded(ctx, client)
	if err != nil {
		return nil, err
	}

	network, err := s.ensureNetwork(ctx)
	if err != nil {
		return nil, err
	}
	result.Network = network
	result.Gateway = network.Gateway

	sudo := config.ResolveSudo(s.Overrides, s.Config)
	if err := s.refuseSudoOnOpenNetwork(sudo, network); err != nil {
		return nil, err
	}

	s.ensureHostDNS(ctx)
	firewallIsInPlace := s.ensureHostFirewall(ctx, network)
	s.warnAboutSudoInBox(sudo, firewallIsInPlace)

	ports, err := s.ResolvePorts()
	if err != nil {
		return nil, err
	}
	_, portsAreExplicit := config.ResolvePorts(s.Overrides, s.Config)
	if ports == nil && !portsAreExplicit {
		ui.Warn("%s", PortsExhaustedMessage(
			s.Overrides.PortBase, s.Overrides.PortLimit))
	}
	// The record saves the block now so that another project cannot take it.
	if len(ports) > 0 && !portsAreExplicit &&
		record.PortBlock != ports[0].Host {
		record.PortBlock = ports[0].Host
		if err := s.Project.SaveRecord(record); err != nil {
			return nil, err
		}
	}
	result.Ports = ports

	shadowPaths := s.ShadowPaths()
	result.Shadow = shadowPaths
	if err := s.prepareDirectories(shadowPaths); err != nil {
		return nil, err
	}

	imageTag, err := s.ensureImage(ctx)
	if err != nil {
		return nil, err
	}
	result.Image = imageTag

	// The token must exist before the profile push. Otherwise the broker
	// does not accept the box until its next poll.
	if _, err := s.Project.Token(); err != nil {
		return nil, err
	}
	if err := s.pushProfile(ctx, client); err != nil {
		return nil, err
	}
	s.reportRequiredSecrets()

	created, err := s.createOrStartBox(ctx, record, imageTag, ports,
		shadowPaths, sudo)
	if err != nil {
		return nil, err
	}
	result.Created = created

	if network.Gateway != "" {
		listener, err := client.Listen(ctx, network.Gateway)
		switch {
		case err != nil:
			ui.Warn("cannot ask the broker to listen on %s: %v",
				network.Gateway, err)
		case listener != nil && !listener.Bound:
			// The box has no outbound path without this listener.
			ui.Warn("the broker is not listening on %s: %s", network.Gateway,
				listenerFailureText(listener))
		}
	}

	if err := s.WaitUntilReady(ctx); err != nil {
		return nil, err
	}
	s.publishPorts(ctx, client, ports)

	s.ensureHostRoute(ctx, network)
	s.runHostOperations()
	s.runBoxCommands(ctx)

	if err := s.runSetup(ctx, false); err != nil {
		return nil, err
	}

	probe, err := s.probeInmateCommands(ctx)
	if err != nil {
		return nil, err
	}
	result.Probe = probe
	return result, nil
}

func (s *Session) ensureRecord() (*state.ProjectRecord, error) {
	record := s.Record
	if record == nil {
		record = &state.ProjectRecord{
			Path:    s.Directory,
			Created: time.Now(),
		}
	}
	record.Path = s.Directory
	if err := s.Project.SaveRecord(record); err != nil {
		return nil, err
	}
	s.Record = record
	return record, nil
}

func (s *Session) ensureBroker(ctx context.Context) (*control.Client, error) {
	spawn := func() error {
		return control.SpawnDetached(s.Executable,
			[]string{"broker", "serve"}, s.Root.BrokerLog()+".err")
	}
	client, err := control.EnsureRunning(ctx, s.Root, s.Version, spawn)
	if err != nil {
		return nil, fmt.Errorf("cannot start the broker: %w", err)
	}
	return client, nil
}

func (s *Session) unlockVaultIfNeeded(ctx context.Context,
	client *control.Client) (bool, error) {
	if !vault.Exists(s.Root.VaultFile()) {
		return false, nil
	}
	grants, err := s.Project.Grants()
	if err != nil {
		return false, err
	}
	if len(grants) == 0 {
		return false, nil
	}
	status, err := client.Status(ctx)
	if err != nil {
		return false, err
	}
	if status.Vault == control.VaultUnlocked {
		return true, nil
	}
	if !ui.StdinIsTerminal() {
		ui.Warn("the vault is locked. Run `prison secret unlock`")
		return false, nil
	}
	passphrase, err := ui.ReadSecret("vault passphrase: ")
	if err != nil {
		return false, err
	}
	if passphrase == "" {
		ui.Warn("the vault stays locked")
		return false, nil
	}
	if err := client.Unlock(ctx, passphrase); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Session) ensureNetwork(ctx context.Context) (cage.NetworkInfo, error) {
	if !s.Cage.Capabilities().HostOnlyNetwork {
		ui.Warn("the %s cage has no host-only network", s.Cage.Name())
		s.Overrides.Network = "default"
		return s.Cage.Network(ctx, s.Overrides.Network)
	}
	if err := s.Cage.EnsureNetwork(ctx, s.Overrides.Network); err != nil {
		return cage.NetworkInfo{}, err
	}
	network, err := s.Cage.Network(ctx, s.Overrides.Network)
	if err != nil {
		return cage.NetworkInfo{}, err
	}
	if !network.HostOnly {
		return network, fmt.Errorf(
			"the %s network is not host-only. Delete it and run `prison up`",
			s.Overrides.Network)
	}
	return network, nil
}

func (s *Session) refuseSudoOnOpenNetwork(sudo bool,
	network cage.NetworkInfo) error {
	if !sudo || network.HostOnly {
		return nil
	}
	return fmt.Errorf(
		"sudo needs a host-only network. Remove [box] sudo or use " +
			"a cage with a host-only network")
}

func (s *Session) warnAboutSudoInBox(sudo, firewallIsInPlace bool) {
	if !sudo {
		return
	}
	if !firewallIsInPlace {
		ui.Warn("this box has sudo but the host packet filter rules are " +
			"missing. Run `prison host firewall`")
		return
	}
	ui.Warn("this box has sudo")
}

func (s *Session) hostFirewallIsMissing(network cage.NetworkInfo) bool {
	if !s.Cage.Capabilities().HostFirewall || network.SubnetV4 == "" {
		return false
	}
	state, err := hostfw.Status(context.Background(),
		s.hostFirewallSpec(network), s.Root.Path, hostfw.SystemRunner)
	if err != nil {
		return false
	}
	return state != hostfw.StateInstalled && state != hostfw.StateUnsupported
}

func (s *Session) hostFirewallSpec(network cage.NetworkInfo) hostfw.Spec {
	return hostfw.Spec{
		SubnetV4:   network.SubnetV4,
		SubnetV6:   network.SubnetV6,
		BrokerPort: s.Overrides.BrokerPort,
	}
}

func (s *Session) prepareDirectories(shadowPaths []string) error {
	for _, relative := range shadowPaths {
		if err := os.MkdirAll(s.Project.ShadowDir(relative), 0o755); err != nil {
			return err
		}
	}
	// Mode 0555 makes the `.git/hooks` overlay read-only.
	if err := os.MkdirAll(s.Project.EmptyDir(), 0o555); err != nil {
		return err
	}
	for _, inmate := range s.Inmates {
		for _, guestPath := range inmate.Persist.Paths {
			host := s.hostPathForGuestPath(inmate, guestPath)
			if err := os.MkdirAll(host, 0o700); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Session) ensureImage(ctx context.Context) (string, error) {
	if s.Overrides.Image != "" {
		return s.Overrides.Image, nil
	}
	foundation, err := image.ResolveFoundation(s.Inmates, s.Overrides.Foundation)
	if err != nil {
		return "", err
	}
	plan, err := image.NewPlan(s.Assets, prison.BaseImageDir, s.Inmates,
		foundation, s.HostUID)
	if err != nil {
		return "", err
	}
	if err := image.Ensure(ctx, s.Cage, plan, os.Stderr); err != nil {
		return "", err
	}
	return plan.Final, nil
}

func (s *Session) BuildImage(ctx context.Context) (string, error) {
	return s.ensureImage(ctx)
}

func (s *Session) pushProfile(
	ctx context.Context, client *control.Client,
) error {
	profile := s.Profile()
	credentials := map[string]string{}
	for _, inmate := range s.Inmates {
		if inmate.Auth == nil {
			continue
		}
		held := false
		var wanted []string
		for _, credential := range inmate.Auth.Credentials {
			wanted = append(wanted, credential.Variable)
			value, present := os.LookupEnv(credential.Variable)
			if present && value != "" {
				credentials[credential.Variable] = value
				held = true
			}
		}
		if !held {
			ui.Warn("no credential for the %s inmate. Export %s "+
				"and run `prison up` again", inmate.Name,
				strings.Join(wanted, " or "))
		}
	}
	return client.UpdateProject(ctx, s.Project.ID, control.ProjectUpdate{
		Profile:     profile,
		Credentials: credentials,
	})
}

func (s *Session) reportRequiredSecrets() {
	if s.Config == nil || len(s.Config.Secrets.Require) == 0 {
		return
	}
	granted, err := s.Project.Grants()
	if err != nil {
		return
	}
	held := map[string]bool{}
	for _, name := range granted {
		held[name] = true
	}
	for _, name := range s.Config.Secrets.Require {
		if !held[name] {
			ui.Warn("prison.toml requires %s but it is not granted. "+
				"Run `prison secret grant %s`", name, name)
		}
	}
}

// createOrStartBox does not change an existing box, so it only warns about
// shape drift. `NET_ADMIN` lets the guest agent set its firewall rules.
func (s *Session) createOrStartBox(
	ctx context.Context, record *state.ProjectRecord, imageTag string,
	ports []cage.PortMapping, shadowPaths []string, sudo bool,
) (bool, error) {
	box, err := s.Cage.Box(ctx, s.BoxName)
	if err != nil {
		return false, err
	}
	shape := s.Shape(imageTag, ports, shadowPaths)
	if box.Exists {
		if !box.Running {
			ui.Progress("starting %s", s.BoxName)
			if err := s.Cage.Start(ctx, s.BoxName); err != nil {
				return false, err
			}
		}
		for _, drift := range DescribeShapeDrift(record.Box, shape) {
			ui.Warn("%s. Run `prison rm`, then `prison up`", drift)
		}
		return false, nil
	}
	token, err := s.Project.Token()
	if err != nil {
		return false, err
	}
	ui.Progress("creating %s", s.BoxName)
	spec := cage.CreateSpec{
		Name:         s.BoxName,
		Image:        imageTag,
		Network:      s.Overrides.Network,
		CPUs:         shape.CPUs,
		Memory:       shape.Memory,
		Mounts:       s.Mounts(shadowPaths),
		Ports:        ports,
		Environment:  s.CreationEnvironment(token, sudo, s.PersistPaths()),
		Capabilities: []string{"NET_ADMIN"},
		Command:      []string{"sleep", "infinity"},
	}
	if err := s.Cage.Create(ctx, spec); err != nil {
		return false, err
	}
	record.Box = shape
	if err := s.Project.SaveRecord(record); err != nil {
		return true, err
	}
	return true, nil
}

// runHostOperations only warns on failure because the box works without the
// host configuration.
func (s *Session) runHostOperations() {
	placeholderTail := s.PlaceholderTail()
	for _, inmate := range s.Inmates {
		if len(inmate.Host.Copy) == 0 && len(inmate.Host.JSON) == 0 {
			continue
		}
		carried, notes, err := plugin.RunHostOperations(inmate, s.HomeDir,
			s.PersistedHome(inmate), plugin.Placeholders{
				PlaceholderTail: placeholderTail,
				Inmate:          inmate.Name,
				ProjectID:       s.Project.ID,
			})
		if err != nil {
			ui.Warn("%s: %v", inmate.Name, err)
			continue
		}
		if len(carried) > 0 {
			ui.Summary("inherit", "%s: %s", inmate.Name,
				strings.Join(carried, ", "))
		}
		for _, note := range notes {
			ui.Warn("%s: %s", inmate.Name, note)
		}
	}
}

// runBoxCommands ignores failures because no later step uses a hook result.
func (s *Session) runBoxCommands(ctx context.Context) {
	for _, inmate := range s.Inmates {
		line := strings.TrimSpace(inmate.Hooks.BoxCommand)
		if line == "" {
			continue
		}
		_, _, _ = s.RunQuiet(ctx, "bash", "-lc", line)
	}
}

// runSetup saves the signature only when every command succeeds, so a failed
// setup runs again on the next `prison up`.
func (s *Session) runSetup(ctx context.Context, force bool) error {
	commands, missing, err := s.SetupCommands(ctx)
	if err != nil {
		return err
	}
	for _, note := range missing {
		ui.Warn("%s", note)
	}
	if len(commands) == 0 {
		return nil
	}
	signature := SetupSignature(commands)
	if !force && s.Record != nil && s.Record.SetupSignature == signature {
		return nil
	}
	// The nil client leaves granted secrets out of setup commands. Secrets are
	// for interactive sessions.
	environment, err := s.ExecEnvironment(ctx, nil, "")
	if err != nil {
		return err
	}
	for _, line := range commands {
		ui.Progress("setup  %s", line)
		status, err := s.RunShell(ctx, line, environment)
		if err != nil {
			ui.Warn("cannot run `%s`: %v", line, err)
			return nil
		}
		if status != 0 {
			ui.Warn("`%s` exited %d. The next `prison up` will run it again",
				line, status)
			return nil
		}
	}
	if s.Record != nil {
		s.Record.SetupSignature = signature
		if err := s.Project.SaveRecord(s.Record); err != nil {
			return err
		}
	}
	return nil
}

func (s *Session) RunSetup(ctx context.Context, force bool) error {
	return s.runSetup(ctx, force)
}

func (s *Session) probeInmateCommands(
	ctx context.Context,
) (ProbeResult, error) {
	var commands []string
	for _, inmate := range s.Inmates {
		if name := firstWord(inmate.Command.Run); name != "" {
			commands = append(commands, name)
		}
	}
	if len(commands) == 0 {
		return ProbeResult{Commands: map[string]bool{}}, nil
	}
	return s.Probe(ctx, commands, "")
}

func firstWord(line string) string {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

func (s *Session) WarnAboutUntrustedConfiguration() {
	if !s.ConfigPresent || s.ConfigTrusted {
		return
	}
	if s.wasTrustedBefore() {
		ui.Warn("prison.toml changed after approval. Run `prison trust`")
		return
	}
	ui.Warn("prison.toml is not trusted. Run `prison trust`")
}

func (s *Session) wasTrustedBefore() bool {
	if s.Record != nil && s.Record.TrustedConfigSHA256 != "" {
		return true
	}
	_, err := os.Stat(s.Project.TrustedConfigCopy())
	return err == nil
}

func (s *Session) InmateByName(name string) (*plugin.Inmate, bool) {
	for _, inmate := range s.Inmates {
		if inmate.Name == name {
			return inmate, true
		}
	}
	for _, inmate := range s.Inmates {
		if inmate.HasAlias(name) {
			return inmate, true
		}
	}
	return nil, false
}

func listenerFailureText(listener *control.Listener) string {
	if listener.Error == "" {
		return "still trying"
	}
	if strings.Contains(listener.Error, "address already in use") {
		return listener.Error + ". Another prison state root uses " +
			"this port. Run `prison broker stop` in that root"
	}
	return listener.Error
}

// publishPorts has the broker forward the ports for a cage that cannot
// publish them.
func (s *Session) publishPorts(ctx context.Context, client *control.Client,
	ports []cage.PortMapping) {
	if !s.Cage.Capabilities().ForwardsPorts || len(ports) == 0 {
		return
	}
	box, err := s.Cage.Box(ctx, s.BoxName)
	if err != nil || box.Address == "" {
		ui.Warn("cannot publish the ports because the box has no address")
		return
	}
	request := control.PublishRequest{Address: box.Address}
	for _, port := range ports {
		request.Ports = append(request.Ports,
			control.PortForward{Host: port.Host, Guest: port.Guest})
	}
	if err := client.Publish(ctx, s.BoxName, request); err != nil {
		ui.Warn("cannot publish the ports: %v", err)
	}
}

// UnpublishPorts does not start the broker because a stopped broker has no
// forwards.
func (e *Environment) UnpublishPorts(ctx context.Context, boxName string) {
	if !e.Cage.Capabilities().ForwardsPorts {
		return
	}
	client := control.NewClient(e.Root.BrokerSocket())
	if client.Alive(ctx) {
		client.Unpublish(ctx, boxName)
	}
}
