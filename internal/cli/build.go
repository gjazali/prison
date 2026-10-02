package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

func init() {
	registerGroup(addBuildCommand)
}

func addBuildCommand(root *cobra.Command) {
	root.AddCommand(&cobra.Command{
		Use:   "build",
		Short: "build this project's image chain",
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, arguments []string) error {
			return runBuild(command)
		},
	})
}

func runBuild(command *cobra.Command) error {
	ctx, cancel := commandContext()
	defer cancel()
	current, err := openSession()
	if err != nil {
		return err
	}
	if current.Overrides.Image != "" {
		return fmt.Errorf("PRISON_IMAGE is set to %s. Unset it to build",
			current.Overrides.Image)
	}
	if err := current.Cage.Require(ctx); err != nil {
		return err
	}
	current.WarnAboutUntrustedConfiguration()
	tag, err := current.BuildImage(ctx)
	if err != nil {
		return err
	}
	fmt.Fprintf(command.OutOrStdout(), "%s\n", tag)
	return nil
}
