package cli

import (
	"errors"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"prison/internal/plugin"
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
		newInmateRemoveCommand(),
		newInmateTrustCommand(),
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
	command := &cobra.Command{
		Use:   "update <name>",
		Short: "update an installed inmate",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, arguments []string) error {
			return runInmateUpdate(
				command, arguments[0], requestedRef, assumeYes)
		},
	}
	addInmateRefFlag(command, &requestedRef)
	addInmateYesFlag(command, &assumeYes)
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
		"branch, tag, or commit to fetch")
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
	ctx, cancel := commandContext()
	defer cancel()
	out := command.OutOrStdout()
	approve := inmateApprovalFunction(
		out, "install this inmate?", assumeYes)
	_, err = environment.Registry.Update(
		ctx, name, requestedRef, approve, out)
	return refuseUnapprovedInmate(err, "not changed")
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
