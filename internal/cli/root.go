// Package cli defines the prison command tree. Each file adds one
// command group to the root.
package cli

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"prison/internal/ui"
)

// Version is stamped by the build through -ldflags; "dev" otherwise.
var Version = "dev"

// newRootCommand builds the root command and attaches every command
// group. Returns the root `*cobra.Command`.
func newRootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:           "prison",
		Short:         "run tools in a per-project isolated box",
		Version:       Version,
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.ArbitraryArgs,
		RunE:          runRootFallback,
		// Prison has never had a completion command, and its own
		// commands are the whole surface it documents.
		CompletionOptions: cobra.CompletionOptions{DisableDefaultCmd: true},
	}
	root.Flags().SetInterspersed(false)
	for _, add := range commandGroups {
		add(root)
	}
	return root
}

// commandGroups holds the functions that attach command groups to the
// root. Each command file appends to it from an init function.
var commandGroups []func(*cobra.Command)

// registerGroup adds a function that attaches commands to the root.
// Takes the function to call during root construction.
func registerGroup(add func(*cobra.Command)) {
	commandGroups = append(commandGroups, add)
}

// Execute runs the command tree with the given arguments. Returns 0
// on success, the status from a `ui.ExitError`, or 1 for any other
// error after printing it.
func Execute(arguments []string) int {
	root := newRootCommand()
	root.SetArgs(arguments)
	err := root.Execute()
	if err == nil {
		return 0
	}
	var exit *ui.ExitError
	if errors.As(err, &exit) {
		if exit.Message != "" {
			fmt.Fprintln(os.Stderr, "error:", exit.Message)
		}
		return exit.Status
	}
	fmt.Fprintln(os.Stderr, "error:", err)
	return 1
}
