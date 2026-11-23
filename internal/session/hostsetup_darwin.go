package session

import (
	"context"
	"fmt"
	"strings"

	"prison/internal/broker/control"
	"prison/internal/isolator"
	"prison/internal/ui"
)

func (s *Session) ensureHostDNS(ctx context.Context) {
	domains := s.Isolator.DNS()
	domain := s.Overrides.Domain
	registered, err := domains.Exists(ctx, domain)
	if err != nil {
		ui.Warn("cannot read the `.%s` domain: %v", domain, err)
		return
	}
	if registered {
		return
	}
	ui.Progress("registering the `.%s` domain", domain)
	printHostSetupBlock(domains.Describe(domain))
	if err := domains.Register(ctx, domain); err != nil {
		ui.Warn("cannot register the `.%s` domain: %v", domain, err)
	}
}

func (e *Environment) ensureHostRoute(
	ctx context.Context, network isolator.NetworkInfo) {
	if network.Gateway == "" {
		return
	}
	routes := e.Isolator.Route()
	installed, err := routes.Installed(ctx, network.Gateway)
	if err != nil {
		ui.Warn("cannot read the route to the box network: %v", err)
		return
	}
	if installed {
		return
	}
	ui.Progress("routing the box network over %s",
		network.Gateway)
	line, err := routes.Command(ctx, network.Gateway)
	if err == nil && line != "" {
		printHostSetupBlock([]string{line})
	}
	if err := routes.Install(ctx, network.Gateway); err != nil {
		ui.Warn("cannot add the route to the box network: %v", err)
	}
}

func (e *Environment) BlockingBoxesForAnchor(
	ctx context.Context,
) ([]isolator.BoxInfo, error) {
	anchor := e.Isolator.Anchor()
	boxes, err := e.Isolator.ListBoxes(ctx)
	if err != nil {
		return nil, err
	}
	domainSuffix := ""
	if e.Overrides != nil && e.Overrides.Domain != "" {
		domainSuffix = "." + e.Overrides.Domain
	}
	boxNetwork := ""
	if e.Overrides != nil {
		boxNetwork = e.Overrides.Network
	}
	var blocking []isolator.BoxInfo
	for _, box := range boxes {
		if !box.Running || box.Name == anchor.Name() {
			continue
		}
		if box.Network == anchor.Network() {
			continue
		}
		if (boxNetwork != "" && box.Network == boxNetwork) ||
			(domainSuffix != "" && strings.HasSuffix(box.Name, domainSuffix)) {
			blocking = append(blocking, box)
		}
	}
	return blocking, nil
}

func (e *Environment) StartAnchorWithQuiesce(
	ctx context.Context, blocking []isolator.BoxInfo, skipRestart string,
) (func(), error) {
	anchor := e.Isolator.Anchor()
	stopped := make([]string, 0, len(blocking))
	for _, box := range blocking {
		ui.Progress("stopping %s to start %s", box.Name, anchor.Name())
		e.UnpublishPorts(ctx, box.Name)
		if err := e.Isolator.Stop(ctx, box.Name); err != nil {
			e.restartBoxes(ctx, stopped)
			return nil, fmt.Errorf("cannot stop %s before starting %s: %w",
				box.Name, anchor.Name(), err)
		}
		stopped = append(stopped, box.Name)
	}

	ui.Progress("starting %s on %s network", anchor.Name(), anchor.Network())
	anchorErr := anchor.Ensure(ctx)

	var toRestart []string
	skippedStopped := false
	for _, name := range stopped {
		if skipRestart != "" && name == skipRestart {
			skippedStopped = true
			continue
		}
		toRestart = append(toRestart, name)
	}
	e.restartBoxes(ctx, toRestart)

	var restore func()
	if skippedStopped {
		restore = func() {
			e.restartBoxes(context.WithoutCancel(ctx), []string{skipRestart})
		}
	}
	return restore, anchorErr
}

func (e *Environment) restartBoxes(ctx context.Context, names []string) {
	if len(names) == 0 {
		return
	}
	restartCtx := context.WithoutCancel(ctx)
	restarted := false
	for _, name := range names {
		ui.Progress("restarting %s", name)
		if err := e.Isolator.Start(restartCtx, name); err != nil {
			ui.Warn("cannot restart %s: %v", name, err)
			continue
		}
		restarted = true
	}
	if restarted {
		e.repairBoxNetworkAfterRestart(restartCtx)
	}
}

func (e *Environment) repairBoxNetworkAfterRestart(ctx context.Context) {
	if e.Overrides == nil || e.Overrides.Network == "" {
		return
	}
	network, err := e.Isolator.Network(ctx, e.Overrides.Network)
	if err != nil || network.Gateway == "" {
		return
	}
	e.ensureHostRoute(ctx, network)
	if e.Root == nil {
		return
	}
	client := control.NewClient(e.Root.BrokerSocket())
	if !client.Alive(ctx) {
		return
	}
	_, _ = client.Listen(ctx, network.Gateway)
}

func (s *Session) ensureHostAnchor(
	ctx context.Context, recreateCurrent bool,
) func() {
	anchor := s.Isolator.Anchor()
	info, err := anchor.Status(ctx)
	if err != nil {
		ui.Warn("cannot check %s: %v", anchor.Name(), err)
		return nil
	}
	if info.Exists && info.Running && info.Network == anchor.Network() {
		return nil
	}

	blocking, err := s.BlockingBoxesForAnchor(ctx)
	if err != nil {
		ui.Warn("cannot list boxes before starting %s: %v", anchor.Name(), err)
		return nil
	}

	recreateBox := ""
	if recreateCurrent {
		recreateBox = s.BoxName
	}

	var others []isolator.BoxInfo
	names := make([]string, 0, len(blocking))
	for _, box := range blocking {
		names = append(names, box.Name)
		if recreateBox == "" || box.Name != recreateBox {
			others = append(others, box)
		}
	}

	if len(others) > 0 {
		listed := strings.Join(names, ", ")
		if !stdinIsTerminal() {
			ui.Warn("%s is not running while %s is active; "+
				"stop running boxes and run `prison host anchor` to avoid vmnet bridge collisions",
				anchor.Name(), listed)
			return nil
		}
		ui.Warn("%s is not running while %s is active on %s",
			anchor.Name(), listed, s.Overrides.Network)
		if !confirmPrompt(fmt.Sprintf(
			"stop %s briefly to start %s?", listed, anchor.Name())) {
			ui.Warn("continuing without %s; stop running boxes and run `prison host anchor` if builds lose networking",
				anchor.Name())
			return nil
		}
	}

	restore, err := s.StartAnchorWithQuiesce(ctx, blocking, recreateBox)
	if err != nil {
		ui.Warn("cannot start %s: %v", anchor.Name(), err)
	}
	return restore
}
