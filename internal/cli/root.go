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

// Version is set by the build through `-ldflags`.
var Version = "dev"

func newRootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:           "prison",
		Short:         "run tools in a per-project isolated box",
		Version:       Version,
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.ArbitraryArgs,
		RunE:          runRootFallback,
		// Prison documents only its own commands, so it has no completion command.
		CompletionOptions: cobra.CompletionOptions{DisableDefaultCmd: true},
	}
	root.Flags().SetInterspersed(false)
	for _, add := range commandGroups {
		add(root)
	}
	return root
}

var commandGroups []func(*cobra.Command)

func registerGroup(add func(*cobra.Command)) {
	commandGroups = append(commandGroups, add)
}

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
