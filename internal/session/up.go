package session

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"prison"
	"prison/internal/broker/control"
	"prison/internal/config"
	"prison/internal/hostfw"
	"prison/internal/image"
	"prison/internal/isolator"
	"prison/internal/plugin"
	"prison/internal/state"
	"prison/internal/ui"
	"prison/internal/vault"
)

const (
	listenerReadyTimeout  = 10 * time.Second
	listenerReadyInterval = 500 * time.Millisecond
)

var (
	stdinIsTerminal = ui.StdinIsTerminal
	confirmPrompt   = ui.Confirm
)

type UpOptions struct {
	Recreate bool
}

type UpResult struct {
	Client   *control.Client
	Image    string
	Ports    []isolator.PortMapping
	Network  isolator.NetworkInfo
	Created  bool
	Shadow   []string
	Probe    ProbeResult
	Gateway  string
	Unlocked bool
}

func (s *Session) Up(ctx context.Context, options UpOptions) (*UpResult, error) {
	if err := s.Isolator.Require(ctx); err != nil {
		return nil, err
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

	var network isolator.NetworkInfo
	sudo := config.ResolveSudo(s.Overrides, s.Config)
	if s.UsesSocketUplink() {
		network = isolator.NetworkInfo{
			Name:     "none",
			Exists:   true,
			HostOnly: true,
		}
	} else {
		network, err = s.ensureNetwork(ctx)
		if err != nil {
			return nil, err
		}
		s.ensureHostDNS(ctx)
		if err := s.ensureHostFirewall(ctx, network); err != nil {
			return nil, err
		}
	}
	result.Network = network
	result.Gateway = network.Gateway
	if sudo {
		ui.Warn("this box has sudo")
	}
	s.warnAboutInBoxLogin()

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

	imageTag, err := s.ensureImage(ctx, nil)
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
	s.reportMissingInmateCredentials(ctx, client)
	s.reportRequiredSecrets()

	created, err := s.createOrStartBox(ctx, record, imageTag, ports,
		shadowPaths, sudo, options.Recreate)
	if err != nil {
		return nil, err
	}
	result.Created = created

	if s.UsesSocketUplink() {
		if err := s.ensureBrokerUplink(ctx, client); err != nil {
			return nil, err
		}
	} else {
		s.ensureHostRoute(ctx, network)
		if err := s.ensureBrokerListener(ctx, client, network.Gateway); err != nil {
			return nil, err
		}
	}

	if err := s.WaitUntilReady(ctx); err != nil {
		return nil, err
	}
	s.publishPorts(ctx, client, ports)

	s.runHostOperations(ctx, client)
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

func (s *Session) EnsureBrokerReady(ctx context.Context) (*control.Client, error) {
	client, err := s.ensureBroker(ctx)
	if err != nil {
		return nil, err
	}
	if s.UsesSocketUplink() {
		if _, err := os.Stat(s.Project.UplinkSocket()); err == nil {
			if err := s.ensureBrokerUplink(ctx, client); err != nil {
				return nil, err
			}
		}
	} else if network, err := s.Isolator.Network(ctx, s.Overrides.Network); err == nil &&
		network.Gateway != "" {
		s.ensureHostRoute(ctx, network)
		if err := s.ensureBrokerListener(ctx, client, network.Gateway); err != nil {
			return nil, err
		}
	}
	if _, err := s.Project.Token(); err != nil {
		return nil, err
	}
	if err := s.pushProfile(ctx, client); err != nil {
		return nil, err
	}
	return client, nil
}

func (s *Session) ensureBrokerUplink(
	ctx context.Context, client *control.Client,
) error {
	socketPath := s.Project.UplinkSocket()
	deadline := time.Now().Add(listenerReadyTimeout)
	var lastUplink *control.Uplink
	for {
		uplink, err := client.AttachUplink(ctx, s.BoxName, socketPath)
		if err != nil {
			return fmt.Errorf("cannot attach the broker uplink for %s: %w",
				s.BoxName, err)
		}
		if uplink != nil && uplink.Connected {
			return nil
		}
		lastUplink = uplink
		if !time.Now().Before(deadline) {
			detail := "the box uplink socket did not accept a connection"
			if lastUplink != nil && lastUplink.Error != "" {
				detail = lastUplink.Error
			}
			return fmt.Errorf("the broker could not connect to %s at %s: %s",
				s.BoxName, socketPath, detail)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

func (s *Session) ensureBrokerListener(
	ctx context.Context, client *control.Client, gateway string,
) error {
	if gateway == "" {
		return nil
	}
	deadline := time.Now().Add(listenerReadyTimeout)
	var lastListener *control.Listener
	for {
		listener, err := client.Listen(ctx, gateway)
		if err != nil {
			return fmt.Errorf("cannot ask the broker to listen on %s: %w",
				gateway, err)
		}
		if listener != nil && listener.Bound {
			return nil
		}
		lastListener = listener
		if !time.Now().Before(deadline) {
			return fmt.Errorf("the broker is not listening on %s: %s",
				gateway, listenerFailureText(lastListener))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(listenerReadyInterval):
		}
	}
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
	if len(grants) == 0 && !s.hasAuthInmates() {
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

func (s *Session) ensureNetwork(
	ctx context.Context) (isolator.NetworkInfo, error) {
	if err := s.Isolator.EnsureNetwork(ctx, s.Overrides.Network); err != nil {
		return isolator.NetworkInfo{}, err
	}
	network, err := s.Isolator.Network(ctx, s.Overrides.Network)
	if err != nil {
		return isolator.NetworkInfo{}, err
	}
	if !network.HostOnly {
		return network, fmt.Errorf(
			"the %s network is not host-only. Delete it and run `prison up`",
			s.Overrides.Network)
	}
	return network, nil
}

func (s *Session) hostFirewallIsMissing(network isolator.NetworkInfo) bool {
	if network.SubnetV4 == "" {
		return false
	}
	state, err := hostfw.Status(context.Background(),
		s.HostFirewallSpec(network), s.Root.Path, hostfw.SystemRunner)
	if err != nil {
		return true
	}
	return state != hostfw.StateInstalled && state != hostfw.StateUnsupported
}

func (environment *Environment) HostFirewallSpec(
	network isolator.NetworkInfo) hostfw.Spec {
	return hostfw.Spec{
		SubnetV4:   network.SubnetV4,
		SubnetV6:   network.SubnetV6,
		BrokerPort: environment.Overrides.BrokerPort,
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
	if err := os.MkdirAll(s.Project.MiseDir(), 0o700); err != nil {
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

func (s *Session) ImagePlan(revisions map[string]int) (*image.Plan, error) {
	foundation, err := image.ResolveFoundation(s.Inmates, s.Overrides.Foundation)
	if err != nil {
		return nil, err
	}
	return image.NewPlan(s.Assets, prison.BaseImageDir, s.Inmates,
		foundation, s.HostUID, revisions)
}

func (s *Session) ensureImage(
	ctx context.Context, revisions map[string]int,
) (string, error) {
	if s.Overrides.Image != "" {
		return s.Overrides.Image, nil
	}
	if revisions == nil {
		var err error
		revisions, err = s.Root.InmateRevisions()
		if err != nil {
			return "", err
		}
	}
	plan, err := s.ImagePlan(revisions)
	if err != nil {
		return "", err
	}
	if err := image.Ensure(ctx, s.Isolator, plan, os.Stderr); err != nil {
		return "", err
	}
	return plan.Final, nil
}

func (s *Session) BuildImage(ctx context.Context) (string, error) {
	return s.ensureImage(ctx, nil)
}

func (s *Session) BuildImageWithRevisions(
	ctx context.Context, revisions map[string]int,
) (string, error) {
	return s.ensureImage(ctx, revisions)
}

func (s *Session) hasAuthInmates() bool {
	profiles := s.inmateAuthProfiles()
	for _, inmate := range s.Inmates {
		if inmate.Auth != nil && profiles[inmate.Name] != vault.InBoxInmateProfile {
			return true
		}
	}
	return false
}

func (s *Session) warnAboutInBoxLogin() {
	profiles := s.inmateAuthProfiles()
	for _, inmate := range s.Inmates {
		if inmate.Auth != nil && profiles[inmate.Name] == vault.InBoxInmateProfile {
			ui.Warn("the %s inmate uses in-box login; credentials stored in this box can be read by code inside it",
				inmate.Name)
		}
	}
}

func (s *Session) pushProfile(
	ctx context.Context, client *control.Client,
) error {
	return client.UpdateProject(ctx, s.Project.ID, control.ProjectUpdate{
		Profile: s.Profile(),
	})
}

func (s *Session) reportMissingInmateCredentials(
	ctx context.Context, client *control.Client,
) {
	if !s.hasAuthInmates() {
		return
	}
	profiles := s.inmateAuthProfiles()
	if !vault.Exists(s.Root.VaultFile()) {
		for _, inmate := range s.Inmates {
			if inmate.Auth == nil || profiles[inmate.Name] == vault.InBoxInmateProfile {
				continue
			}
			ui.Warn("no credential for the %s inmate. Run `prison secret init`, "+
				"then `prison inmate auth set %s`", inmate.Name, inmate.Name)
		}
		return
	}
	status, err := client.Status(ctx)
	if err != nil || status.Vault != control.VaultUnlocked {
		return
	}
	held := map[string]bool{}
	for _, credential := range status.InmateCredentials {
		held[vault.InmateCredentialKey(
			credential.Inmate, credential.Profile, credential.Variable)] = true
	}
	for _, inmate := range s.Inmates {
		if inmate.Auth == nil || profiles[inmate.Name] == vault.InBoxInmateProfile {
			continue
		}
		profile := vault.DefaultInmateProfile
		if selected := profiles[inmate.Name]; selected != "" {
			profile = selected
		}
		matched := false
		for _, spec := range inmate.Auth.Credentials {
			if held[vault.InmateCredentialKey(inmate.Name, profile, spec.Variable)] {
				matched = true
				break
			}
		}
		if matched {
			continue
		}
		if profile != vault.DefaultInmateProfile {
			ui.Warn("no credential for the %s inmate in profile %q. "+
				"Run `prison inmate auth set %s --profile %s`",
				inmate.Name, profile, inmate.Name, profile)
			continue
		}
		ui.Warn("no credential for the %s inmate. "+
			"Run `prison inmate auth set %s`", inmate.Name, inmate.Name)
	}
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

// createOrStartBox leaves an existing box alone unless `recreate` is set or
// the user confirms recreation after shape drift. `NET_ADMIN` lets the guest
// agent set its firewall rules.
func (s *Session) createOrStartBox(
	ctx context.Context, record *state.ProjectRecord, imageTag string,
	ports []isolator.PortMapping, shadowPaths []string, sudo bool,
	recreate bool,
) (bool, error) {
	box, err := s.Isolator.Box(ctx, s.BoxName)
	if err != nil {
		return false, err
	}
	shape := s.Shape(imageTag, ports, shadowPaths)
	if box.Exists && !recreate {
		drifts := DescribeShapeDrift(record.Box, shape)
		if len(drifts) > 0 && stdinIsTerminal() {
			for _, drift := range drifts {
				ui.Warn("%s", drift)
			}
			if confirmPrompt(fmt.Sprintf("recreate %s now?", s.BoxName)) {
				recreate = true
			} else {
				ui.Progress("keeping the existing box. Run `prison up --recreate` later to apply")
			}
		} else {
			for _, drift := range drifts {
				ui.Warn("%s. Run `prison up --recreate`", drift)
			}
		}
	}
	if box.Exists && !recreate {
		if !box.Running {
			ui.Progress("starting %s", s.BoxName)
			if err := s.Isolator.Start(ctx, s.BoxName); err != nil {
				return false, err
			}
		}
		return false, nil
	}
	if box.Exists && recreate {
		ui.Progress("recreating %s", s.BoxName)
		s.UnpublishPorts(ctx, s.BoxName)
		s.DetachUplink(ctx, s.BoxName)
		_ = s.Isolator.Stop(ctx, s.BoxName)
		if err := s.Isolator.Delete(ctx, s.BoxName); err != nil {
			current, lookupErr := s.Isolator.Box(ctx, s.BoxName)
			if lookupErr != nil || current.Exists {
				return false, err
			}
		}
		record.Box = nil
		record.SetupSignature = ""
		if err := s.Project.SaveRecord(record); err != nil {
			return false, err
		}
	}
	token, err := s.Project.Token()
	if err != nil {
		return false, err
	}
	ui.Progress("creating %s", s.BoxName)
	spec := isolator.CreateSpec{
		Name:         s.BoxName,
		Image:        imageTag,
		Network:      shape.Network,
		CPUs:         shape.CPUs,
		Memory:       shape.Memory,
		Mounts:       s.Mounts(shadowPaths),
		Ports:        ports,
		Environment:  s.CreationEnvironment(token, sudo, s.PersistPaths()),
		Capabilities: []string{"NET_ADMIN", "SYS_ADMIN"},
		Command:      []string{"sleep", "infinity"},
	}
	if s.UsesSocketUplink() {
		spec.Ports = nil
		spec.Sockets = []isolator.SocketForward{{
			Host:      s.Project.UplinkSocket(),
			Container: GuestUplinkSocket,
		}}
	}
	if err := s.Isolator.Create(ctx, spec); err != nil {
		return false, err
	}
	record.Box = shape
	if err := s.Project.SaveRecord(record); err != nil {
		return true, err
	}
	return true, nil
}

func (s *Session) EnsureVaultUnlockedForInmate(ctx context.Context,
	client *control.Client, inmate *plugin.Inmate) error {
	if inmate == nil || inmate.Auth == nil || s.InmateUsesInBoxLogin(inmate.Name) ||
		!vault.Exists(s.Root.VaultFile()) {
		return nil
	}
	status, err := client.Status(ctx)
	if err != nil || status.Vault == control.VaultUnlocked {
		return err
	}
	if !ui.StdinIsTerminal() {
		ui.Warn("the vault is locked. Run `prison secret unlock`")
		return nil
	}
	passphrase, err := ui.ReadSecret("vault passphrase: ")
	if err != nil {
		return err
	}
	if passphrase == "" {
		ui.Warn("the vault stays locked")
		return nil
	}
	return client.Unlock(ctx, passphrase)
}

type InmateSyncReport struct {
	Inmate *plugin.Inmate
	Result plugin.HostSyncResult
	Error  error
}

// SyncInmatesFromHost mirrors each inmate's host configuration into its
// persisted home directory for this project.
func (s *Session) SyncInmatesFromHost(ctx context.Context,
	client *control.Client, inmates []*plugin.Inmate) []InmateSyncReport {
	placeholderTail := s.PlaceholderTail()
	authProfiles := s.inmateAuthProfiles()
	active := s.ActiveInmateCredentials(ctx, client)
	reports := make([]InmateSyncReport, 0, len(inmates))
	for _, inmate := range inmates {
		report := InmateSyncReport{Inmate: inmate}
		if inmate == nil || (len(inmate.Host.Copy) == 0 && len(inmate.Host.JSON) == 0) {
			reports = append(reports, report)
			continue
		}
		inBox := authProfiles[inmate.Name] == vault.InBoxInmateProfile
		activeVariable := ""
		if !inBox {
			if cred := active[inmate.Name]; cred != nil {
				activeVariable = cred.Variable
			}
		}
		result, err := plugin.MirrorHostOperations(inmate, s.HomeDir,
			s.PersistedHome(inmate), plugin.Placeholders{
				PlaceholderTail:  placeholderTail,
				Inmate:           inmate.Name,
				ProjectID:        s.Project.ID,
				ActiveCredential: activeVariable,
				InBoxLogin:       inBox,
			})
		report.Result = result
		report.Error = err
		reports = append(reports, report)
	}
	return reports
}

func (s *Session) SyncInmateHost(ctx context.Context,
	client *control.Client, inmate *plugin.Inmate) {
	if inmate == nil || (len(inmate.Host.Copy) == 0 && len(inmate.Host.JSON) == 0) {
		return
	}
	inBox := s.InmateUsesInBoxLogin(inmate.Name)
	activeVariable := ""
	if !inBox {
		active := s.ActiveInmateCredentials(ctx, client)
		if cred := active[inmate.Name]; cred != nil {
			activeVariable = cred.Variable
		}
	}
	_, _, err := plugin.RunHostOperations(inmate, s.HomeDir,
		s.PersistedHome(inmate), plugin.Placeholders{
			PlaceholderTail:  s.PlaceholderTail(),
			Inmate:           inmate.Name,
			ProjectID:        s.Project.ID,
			ActiveCredential: activeVariable,
			InBoxLogin:       inBox,
		})
	if err != nil {
		ui.Warn("%s: %v", inmate.Name, err)
	}
}

// runHostOperations only warns on failure because the box works without the
// host configuration.
func (s *Session) runHostOperations(ctx context.Context, client *control.Client) {
	placeholderTail := s.PlaceholderTail()
	authProfiles := s.inmateAuthProfiles()
	active := s.ActiveInmateCredentials(ctx, client)
	for _, inmate := range s.Inmates {
		if len(inmate.Host.Copy) == 0 && len(inmate.Host.JSON) == 0 {
			continue
		}
		inBox := authProfiles[inmate.Name] == vault.InBoxInmateProfile
		activeVariable := ""
		if !inBox {
			if cred := active[inmate.Name]; cred != nil {
				activeVariable = cred.Variable
			}
		}
		carried, notes, err := plugin.RunHostOperations(inmate, s.HomeDir,
			s.PersistedHome(inmate), plugin.Placeholders{
				PlaceholderTail:  placeholderTail,
				Inmate:           inmate.Name,
				ProjectID:        s.Project.ID,
				ActiveCredential: activeVariable,
				InBoxLogin:       inBox,
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
	commands := s.SetupCommands()
	if len(commands) == 0 {
		return nil
	}
	signature := SetupSignature(commands)
	if !force && s.Record != nil && s.Record.SetupSignature == signature {
		return nil
	}
	tty := stdoutIsTerminal()
	term := ""
	if tty {
		term = s.ResolveTerm(ctx)
	}
	// The nil client leaves granted secrets out of setup commands. Secrets are
	// for interactive sessions.
	environment, err := s.ExecEnvironment(ctx, nil, term)
	if err != nil {
		return err
	}
	for _, line := range commands {
		ui.Progress("setup  %s", line)
		status, err := s.runShell(ctx, line, environment, tty)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
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
		if name := FirstWord(inmate.Command.Run); name != "" {
			commands = append(commands, name)
		}
	}
	if len(commands) == 0 {
		return ProbeResult{Commands: map[string]bool{}}, nil
	}
	return s.Probe(ctx, commands, "")
}

func FirstWord(line string) string {
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
