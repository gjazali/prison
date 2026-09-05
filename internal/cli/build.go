package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

func init() {
	registerGroup(addBuildCommand)
}

// addBuildCommand adds the `prison build` command to root.
func addBuildCommand(root *cobra.Command) {
	root.AddCommand(&cobra.Command{
		Use:   "build",
		Short: "build this project's image chain, without a box",
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, arguments []string) error {
			return runBuild(command)
		},
	})
}

// runBuild builds this project's image chain and prints the final
// tag to stdout. Takes command for its output writer. Returns an
// error if `PRISON_IMAGE` is set or the build fails.
func runBuild(command *cobra.Command) error {
	ctx, cancel := commandContext()
	defer cancel()
	current, err := openSession()
	if err != nil {
		return err
	}
	if current.Overrides.Image != "" {
		return fmt.Errorf(
			"PRISON_IMAGE names %s, which Prison did not build and will "+
				"not rebuild; unset PRISON_IMAGE to build the image chain",
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
