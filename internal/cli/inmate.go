package cli

import (
	"errors"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"prison/internal/plugin"
	"prison/internal/ui"
)

// init registers the inmate command group.
func init() {
	registerGroup(addInmateCommands)
}

// addInmateCommands adds the inmate command group to root. Lists all
// inmates when no subcommand is given.
func addInmateCommands(root *cobra.Command) {
	group := &cobra.Command{
		Use:   "inmate",
		Short: "manage the tools Prison can put in a box",
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

// newInmateListCommand builds the list subcommand. Returns a
// *cobra.Command that prints every known inmate.
func newInmateListCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "every inmate Prison knows, and its state",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, arguments []string) error {
			return runInmateList(command)
		},
	}
}

// newInmateShowCommand builds the show subcommand. Takes an inmate
// name. Returns a *cobra.Command that prints its manifest.
func newInmateShowCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "show <name>",
		Short: "an inmate's manifest",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, arguments []string) error {
			return runInmateShow(command, arguments[0])
		},
	}
}

// newInmateInstallCommand builds the install subcommand. Takes a git
// URL. Returns a *cobra.Command that fetches an inmate and asks
// before writing it.
func newInmateInstallCommand() *cobra.Command {
	var requestedRef string
	var assumeYes bool
	command := &cobra.Command{
		Use:   "install <git-url>",
		Short: "fetch and install one, showing what it does first and asking",
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

// newInmateUpdateCommand builds the update subcommand. Takes an inmate
// name. Returns a *cobra.Command that fetches it again from its source.
func newInmateUpdateCommand() *cobra.Command {
	var requestedRef string
	var assumeYes bool
	command := &cobra.Command{
		Use:   "update <name>",
		Short: "fetch the current version of an installed one",
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

// newInmateRemoveCommand builds the remove subcommand. Takes an inmate
// name. Returns a *cobra.Command that uninstalls it.
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

// newInmateTrustCommand builds the trust subcommand. Takes an inmate
// name. Returns a *cobra.Command that approves it after its files
// changed.
func newInmateTrustCommand() *cobra.Command {
	var assumeYes bool
	command := &cobra.Command{
		Use:   "trust <name>",
		Short: "approve an installed inmate again after it changed",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, arguments []string) error {
			return runInmateTrust(command, arguments[0], assumeYes)
		},
	}
	addInmateYesFlag(command, &assumeYes)
	return command
}

// addInmateRefFlag adds the --ref flag to a command. Takes the
// command and a pointer for the flag value.
func addInmateRefFlag(command *cobra.Command, target *string) {
	command.Flags().StringVar(target, "ref", "",
		"branch, tag, or commit to fetch")
}

// addInmateYesFlag adds the --yes flag to a command. Takes the
// command and a pointer for the flag value.
func addInmateYesFlag(command *cobra.Command, target *bool) {
	command.Flags().BoolVar(target, "yes", false,
		"approve without the prompt")
}

// runInmateList prints a table of all inmates with name, state, and
// description. Returns an error if the registry is unreadable.
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

// runInmateShow prints one inmate's manifest. Takes the command and
// name. Returns an error if the name is not found.
func runInmateShow(command *cobra.Command, name string) error {
	environment, err := loadEnvironment()
	if err != nil {
		return err
	}
	inmate, err := environment.Registry.Get(name)
	if err != nil {
		return fmt.Errorf("%w; `prison inmate list` lists them", err)
	}
	plugin.Describe(inmate, command.OutOrStdout())
	return nil
}

// runInmateInstall fetches an inmate from a repository and installs
// it after approval. Declining exits 1.
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
	return refuseUnapprovedInmate(err, "left uninstalled")
}

// runInmateUpdate fetches an installed inmate again and replaces it
// after approval. Does nothing if unchanged.
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
	return refuseUnapprovedInmate(err, "left unchanged")
}

// runInmateRemove uninstalls an inmate. Takes the command and name.
// Bundled inmates cannot be removed.
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

// runInmateTrust shows a changed inmate and records the new hash
// after approval.
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
	return refuseUnapprovedInmate(err, "left unapproved")
}

// inmateApprovalFunction returns a callback that prints the inmate
// and asks for approval. Takes a writer, a question, and whether to
// skip the prompt. Returns a func(string) bool.
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

// refuseUnapprovedInmate turns a declined approval into exit status 1
// with the given message. Other errors pass through.
func refuseUnapprovedInmate(err error, message string) error {
	if errors.Is(err, plugin.ErrNotApproved) {
		return ui.Exit(1, "%s", message)
	}
	return err
}

// inmateStateText returns the state label for an inmate: "bundled",
// "installed", or "installed, not approved".
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

// inmateDescriptionText returns the inmate's summary, or the error
// that makes it unusable.
func inmateDescriptionText(inmate *plugin.Inmate) string {
	if inmate.Problem != nil {
		return "unusable: " + inmate.Problem.Error()
	}
	return inmate.Inmate.Description
}
