package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"prison/internal/broker/control"
	"prison/internal/plugin"
	"prison/internal/session"
	"prison/internal/state"
	"prison/internal/ui"
)

func init() {
	registerGroup(addInmateCommands)
}

func addInmateCommands(root *cobra.Command) {
	group := &cobra.Command{
		Use:   "inmate",
		Short: "manage the tools that can go in a box",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, arguments []string) error {
			return runInmateList(command)
		},
	}
	group.AddCommand(
		newInmateListCommand(),
		newInmateShowCommand(),
		newInmateInstallCommand(),
		newInmateUpdateCommand(),
		newInmateSyncCommand(),
		newInmateRemoveCommand(),
		newInmateTrustCommand(),
		newInmateAuthCommand(),
	)
	root.AddCommand(group)
}

func newInmateListCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "list inmates and their state",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, arguments []string) error {
			return runInmateList(command)
		},
	}
}

func newInmateShowCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "show <name>",
		Short: "show an inmate's manifest",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, arguments []string) error {
			return runInmateShow(command, arguments[0])
		},
	}
}

func newInmateInstallCommand() *cobra.Command {
	var requestedRef string
	var assumeYes bool
	command := &cobra.Command{
		Use:   "install <git-url>",
		Short: "install an inmate from a git URL",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, arguments []string) error {
			return runInmateInstall(
				command, arguments[0], requestedRef, assumeYes)
		},
	}
	addInmateRefFlag(command, &requestedRef)
	addInmateYesFlag(command, &assumeYes)
	return command
}

func newInmateUpdateCommand() *cobra.Command {
	var requestedRef string
	var assumeYes bool
	var updateAll bool
	command := &cobra.Command{
		Use:   "update [<name>]",
		Short: "update an inmate and rebuild this project's image",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(command *cobra.Command, arguments []string) error {
			if updateAll && len(arguments) > 0 {
				return errors.New("cannot pass an inmate name with `--all`")
			}
			if updateAll && requestedRef != "" {
				return errors.New("cannot use `--ref` with `--all`")
			}
			if !updateAll && len(arguments) != 1 {
				return errors.New("pass an inmate name or `--all`")
			}
			if updateAll {
				return runInmateUpdateAll(command, assumeYes)
			}
			return runInmateUpdate(
				command, arguments[0], requestedRef, assumeYes)
		},
	}
	addInmateRefFlag(command, &requestedRef)
	addInmateYesFlag(command, &assumeYes)
	command.Flags().BoolVar(&updateAll, "all", false,
		"update every inmate enabled in this project")
	return command
}

func newInmateSyncCommand() *cobra.Command {
	var syncAll bool
	command := &cobra.Command{
		Use:   "sync [<name>]",
		Short: "sync host configuration for inmates into this project's box",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(command *cobra.Command, arguments []string) error {
			if syncAll && len(arguments) > 0 {
				return errors.New("cannot pass an inmate name with `--all`")
			}
			if !syncAll && len(arguments) != 1 {
				return errors.New("pass an inmate name or `--all`")
			}
			if syncAll {
				return runInmateSync(command, "")
			}
			return runInmateSync(command, arguments[0])
		},
	}
	command.Flags().BoolVar(&syncAll, "all", false,
		"sync every inmate enabled in this project")
	return command
}

func newInmateRemoveCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "remove <name>",
		Short: "uninstall an inmate",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, arguments []string) error {
			return runInmateRemove(command, arguments[0])
		},
	}
}

func newInmateTrustCommand() *cobra.Command {
	var assumeYes bool
	command := &cobra.Command{
		Use:   "trust <name>",
		Short: "approve a changed inmate again",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, arguments []string) error {
			return runInmateTrust(command, arguments[0], assumeYes)
		},
	}
	addInmateYesFlag(command, &assumeYes)
	return command
}

func addInmateRefFlag(command *cobra.Command, target *string) {
	command.Flags().StringVar(target, "ref", "",
		"branch or tag to fetch")
}

func addInmateYesFlag(command *cobra.Command, target *bool) {
	command.Flags().BoolVar(target, "yes", false,
		"approve without the prompt")
}

func runInmateList(command *cobra.Command) error {
	environment, err := loadEnvironment()
	if err != nil {
		return err
	}
	inmates, err := environment.Registry.List()
	if err != nil {
		return err
	}
	rows := [][]string{{"NAME", "STATE", "DESCRIPTION"}}
	for _, inmate := range inmates {
		rows = append(rows, []string{
			inmate.Name,
			inmateStateText(inmate),
			inmateDescriptionText(inmate),
		})
	}
	return ui.Table(command.OutOrStdout(), rows)
}

func runInmateShow(command *cobra.Command, name string) error {
	environment, err := loadEnvironment()
	if err != nil {
		return err
	}
	inmate, err := environment.Registry.Get(name)
	if err != nil {
		return fmt.Errorf("%w. Run `prison inmate list`", err)
	}
	plugin.Describe(inmate, command.OutOrStdout())
	return nil
}

func runInmateInstall(
	command *cobra.Command, url, requestedRef string, assumeYes bool,
) error {
	environment, err := loadEnvironment()
	if err != nil {
		return err
	}
	ctx, cancel := commandContext()
	defer cancel()
	out := command.OutOrStdout()
	approve := inmateApprovalFunction(
		out, "install this inmate?", assumeYes)
	_, err = environment.Registry.Install(
		ctx, url, requestedRef, approve, out)
	return refuseUnapprovedInmate(err, "not installed")
}

func runInmateUpdate(
	command *cobra.Command, name, requestedRef string, assumeYes bool,
) error {
	environment, err := loadEnvironment()
	if err != nil {
		return err
	}
	inmate, err := environment.Registry.Get(name)
	if err != nil {
		return fmt.Errorf("%w. Run `prison inmate list`", err)
	}
	if inmate.Bundled && requestedRef != "" {
		return fmt.Errorf(
			"the %s inmate is bundled and does not accept `--ref`", inmate.Name)
	}
	ctx, cancel := commandContext()
	defer cancel()
	out := command.OutOrStdout()
	if !inmate.Bundled {
		approve := inmateApprovalFunction(
			out, "install this inmate?", assumeYes)
		if _, err := environment.Registry.Update(
			ctx, inmate.Name, requestedRef, approve, out); err != nil {
			return refuseUnapprovedInmate(err, "not changed")
		}
	}

	if current, err := environment.OpenCurrent(); err == nil {
		if projectInmate, enabled := current.InmateByName(inmate.Name); enabled {
			current.WarnAboutUntrustedConfiguration()
			var toBump []string
			if projectInmate.IsPinnedVersion() {
				fmt.Fprintf(out,
					"the %s inmate is pinned to %s in this project; edit prison.toml to change its version\n",
					projectInmate.Name, projectInmate.Version)
			} else {
				toBump = []string{inmate.Name}
			}
			return rebuildProjectAfterUpdate(ctx, current, toBump, out)
		}
	}

	revisions, err := environment.Root.InmateRevisions()
	if err != nil {
		return err
	}
	nextRevisions := state.NextInmateRevisions(revisions, inmate.Name)
	if err := environment.Root.SaveInmateRevisions(nextRevisions); err != nil {
		return err
	}
	fmt.Fprintf(out,
		"updated %s. Projects using it will rebuild on the next `prison build` or `prison up`\n",
		inmate.Name)
	return nil
}

func runInmateUpdateAll(command *cobra.Command, assumeYes bool) error {
	environment, err := loadEnvironment()
	if err != nil {
		return err
	}
	current, err := environment.OpenCurrent()
	if err != nil {
		return err
	}
	current.WarnAboutUntrustedConfiguration()
	out := command.OutOrStdout()
	if len(current.Inmates) == 0 {
		fmt.Fprintln(out, "no inmates enabled in this project")
		return nil
	}
	ctx, cancel := commandContext()
	defer cancel()
	approve := inmateApprovalFunction(out, "install this inmate?", assumeYes)
	var toBump []string
	for _, inmate := range current.Inmates {
		if !inmate.Bundled {
			if _, err := environment.Registry.Update(
				ctx, inmate.Name, "", approve, out); err != nil {
				return refuseUnapprovedInmate(err, "not changed")
			}
		}
		if inmate.IsPinnedVersion() {
			fmt.Fprintf(out, "the %s inmate is pinned to %s in this project\n",
				inmate.Name, inmate.Version)
			continue
		}
		toBump = append(toBump, inmate.Name)
	}
	current, err = environment.OpenCurrent()
	if err != nil {
		return err
	}
	return rebuildProjectAfterUpdate(ctx, current, toBump, out)
}

func rebuildProjectAfterUpdate(
	ctx context.Context, current *session.Session, toBump []string, out io.Writer,
) error {
	revisions, err := current.Root.InmateRevisions()
	if err != nil {
		return err
	}
	nextRevisions := state.NextInmateRevisions(revisions, toBump...)
	if current.Overrides.Image != "" {
		if len(toBump) > 0 {
			if err := current.Root.SaveInmateRevisions(nextRevisions); err != nil {
				return err
			}
		}
		ui.Warn("PRISON_IMAGE is set to %s; skipping image rebuild",
			current.Overrides.Image)
		return nil
	}
	if err := current.Isolator.Require(ctx); err != nil {
		return err
	}
	tag, err := current.BuildImageWithRevisions(ctx, nextRevisions)
	if err != nil {
		return err
	}
	if len(toBump) > 0 {
		if err := current.Root.SaveInmateRevisions(nextRevisions); err != nil {
			return err
		}
	}
	fmt.Fprintf(out, "%s\n", tag)
	suggestRecreateIfNeeded(ctx, current, tag)
	return nil
}

func suggestRecreateIfNeeded(
	ctx context.Context, current *session.Session, tag string,
) {
	if current.Record == nil || current.Record.Box == nil ||
		current.Record.Box.Image == tag {
		return
	}
	if box, err := current.Isolator.Box(ctx, current.BoxName); err == nil && box.Exists {
		ui.Progress("run `prison up --recreate` to apply the updated image")
	}
}

func runInmateSync(command *cobra.Command, name string) error {
	environment, err := loadEnvironment()
	if err != nil {
		return err
	}
	current, err := environment.OpenCurrent()
	if err != nil {
		return err
	}
	current.WarnAboutUntrustedConfiguration()
	out := command.OutOrStdout()

	var targets []*plugin.Inmate
	if name == "" {
		if len(current.Inmates) == 0 {
			fmt.Fprintln(out, "no inmates enabled in this project")
			return nil
		}
		targets = current.Inmates
	} else {
		inmate, enabled := current.InmateByName(name)
		if !enabled {
			if known, lookupErr := environment.Registry.Get(name); lookupErr == nil {
				return fmt.Errorf("%s is not enabled in this project. Add it to "+
					"`inmates` under [prison] in %s and run `prison trust`, or add "+
					"it to %s",
					known.Name, session.ProjectConfigFileName,
					current.Root.ConfigFile())
			} else {
				return fmt.Errorf("%w. Run `prison inmate list`", lookupErr)
			}
		}
		targets = []*plugin.Inmate{inmate}
	}

	ctx, cancel := commandContext()
	defer cancel()
	var client *control.Client
	if candidate := control.NewClient(current.Root.BrokerSocket()); candidate.Alive(ctx) {
		client = candidate
	}

	reports := current.SyncInmatesFromHost(ctx, client, targets)
	var errs []error
	for _, report := range reports {
		if report.Error != nil {
			errs = append(errs, fmt.Errorf("%s: %w", report.Inmate.Name, report.Error))
			continue
		}
		for _, note := range report.Result.Notes {
			ui.Warn("%s: %s", report.Inmate.Name, note)
		}
		switch {
		case len(report.Result.Carried) > 0 && len(report.Result.Removed) > 0:
			fmt.Fprintf(out, "%s: synced %s; removed %s\n",
				report.Inmate.Name,
				strings.Join(report.Result.Carried, ", "),
				strings.Join(report.Result.Removed, ", "))
		case len(report.Result.Carried) > 0:
			fmt.Fprintf(out, "%s: synced %s\n",
				report.Inmate.Name,
				strings.Join(report.Result.Carried, ", "))
		case len(report.Result.Removed) > 0:
			fmt.Fprintf(out, "%s: removed %s\n",
				report.Inmate.Name,
				strings.Join(report.Result.Removed, ", "))
		default:
			fmt.Fprintf(out, "%s: nothing to sync\n", report.Inmate.Name)
		}
	}
	return errors.Join(errs...)
}

func runInmateRemove(command *cobra.Command, name string) error {
	environment, err := loadEnvironment()
	if err != nil {
		return err
	}
	if err := environment.Registry.Remove(name); err != nil {
		return err
	}
	fmt.Fprintf(command.OutOrStdout(), "removed the %s inmate\n", name)
	return nil
}

func runInmateTrust(
	command *cobra.Command, name string, assumeYes bool,
) error {
	environment, err := loadEnvironment()
	if err != nil {
		return err
	}
	out := command.OutOrStdout()
	approve := inmateApprovalFunction(
		out, "approve this inmate?", assumeYes)
	_, err = environment.Registry.Trust(name, approve, out)
	return refuseUnapprovedInmate(err, "not approved")
}

func inmateApprovalFunction(
	out io.Writer, question string, assumeYes bool,
) func(string) bool {
	return func(description string) bool {
		fmt.Fprint(out, description)
		if assumeYes {
			return true
		}
		return ui.Confirm(question)
	}
}

func refuseUnapprovedInmate(err error, message string) error {
	if errors.Is(err, plugin.ErrNotApproved) {
		return ui.Exit(1, "%s", message)
	}
	return err
}

func inmateStateText(inmate *plugin.Inmate) string {
	switch {
	case inmate.Bundled:
		return "bundled"
	case inmate.Trusted:
		return "installed"
	default:
		return "installed, not approved"
	}
}

func inmateDescriptionText(inmate *plugin.Inmate) string {
	if inmate.Problem != nil {
		return "unusable: " + inmate.Problem.Error()
	}
	return inmate.Inmate.Description
}
