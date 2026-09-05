package cli

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"prison/internal/config"
	"prison/internal/session"
	"prison/internal/state"
	"prison/internal/ui"
)

// trustedConfigCopyMode is the file permission for the approved copy
// of prison.toml.
const trustedConfigCopyMode = 0o600

// Labels for the two sides of a prison.toml diff.
const (
	trustedConfigLabel = "prison.toml (trusted)"
	currentConfigLabel = "prison.toml (now)"
)

// errNoDifference signals that two files are identical.
var errNoDifference = errors.New("the two files hold the same bytes")

func init() {
	registerGroup(addTrustCommand)
}

// addTrustCommand attaches `prison trust` to the root command.
func addTrustCommand(root *cobra.Command) {
	var approveWithoutAsking bool
	command := &cobra.Command{
		Use:   "trust",
		Short: "approve this project's prison.toml",
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, arguments []string) error {
			return runTrustCommand(approveWithoutAsking)
		},
	}
	command.Flags().BoolVar(&approveWithoutAsking, "yes", false,
		"approve without the confirmation prompt")
	root.AddCommand(command)
}

// runTrustCommand approves the working directory's prison.toml. Takes
// whether to skip the confirmation prompt. Returns an error if the
// file is absent.
func runTrustCommand(approveWithoutAsking bool) error {
	currentSession, err := openSession()
	if err != nil {
		return err
	}
	if !currentSession.ConfigPresent {
		return fmt.Errorf("there is no %s in %s; nothing to trust",
			session.ProjectConfigFileName, currentSession.Directory)
	}
	if currentSession.ConfigTrusted {
		ui.Progress("prison.toml is already trusted")
		return nil
	}
	projectConfiguration, err := config.LoadProject(currentSession.ConfigPath)
	if err != nil {
		return err
	}
	configurationContent, err := os.ReadFile(currentSession.ConfigPath)
	if err != nil {
		return fmt.Errorf("cannot read %s: %w", currentSession.ConfigPath, err)
	}
	diffTool := resolveTrustDiffTool(currentSession)
	showConfigurationBeingTrusted(currentSession, configurationContent, diffTool)
	warnAboutWhatTheConfigurationGrants(projectConfiguration)
	if !approveWithoutAsking && !ui.Confirm("trust this file?") {
		return ui.Exit(1, "left untrusted")
	}
	return recordTrustedConfiguration(currentSession, configurationContent)
}

// showConfigurationBeingTrusted prints the file being approved. Shows
// the full file for new configs and a diff for changed ones. Takes
// the session, file contents, and diff tool command line.
func showConfigurationBeingTrusted(
	currentSession *session.Session, configurationContent []byte,
	diffTool string,
) {
	trustedCopyPath := currentSession.Project.TrustedConfigCopy()
	if _, err := os.Stat(trustedCopyPath); err != nil {
		ui.Progress("prison.toml has not been trusted before:")
		fmt.Println(ui.Indent(string(configurationContent), "  "))
		return
	}
	difference, err := unifiedDifference(
		trustedCopyPath, currentSession.ConfigPath)
	if errors.Is(err, errNoDifference) {
		ui.Progress("prison.toml matches the copy you approved before:")
		fmt.Println(ui.Indent(string(configurationContent), "  "))
		return
	}
	ui.Progress("prison.toml has changed since it was trusted:")
	if err != nil {
		printBothConfigurations(trustedCopyPath, configurationContent)
		return
	}
	if diffTool != "" {
		err = pipeThroughCommand(diffTool, strings.NewReader(difference))
		if err == nil {
			fmt.Println()
			return
		}
		ui.Warn("%v", err)
	}
	fmt.Print(withoutFileHeader(difference))
	fmt.Println()
}

// resolveTrustDiffTool returns the diff tool command line from the
// session settings. Returns an empty string if none is set or the
// tool is not on the PATH.
func resolveTrustDiffTool(currentSession *session.Session) string {
	diffTool := config.ResolveDiffTool(
		currentSession.Overrides, currentSession.Global)
	program := strings.Fields(diffTool)
	if len(program) == 0 {
		return ""
	}
	if _, err := exec.LookPath(program[0]); err != nil {
		ui.Warn("the diff tool is set to `%s`, but %s is not on your PATH, "+
			"so this diff is Prison's own", diffTool, program[0])
		return ""
	}
	return diffTool
}

// unifiedDifference runs `diff -u` on the trusted copy and the
// current file. Returns the labelled diff output. Returns
// `errNoDifference` when the files are identical.
func unifiedDifference(
	trustedCopyPath, currentPath string,
) (string, error) {
	program, err := exec.LookPath("diff")
	if err != nil {
		return "", err
	}
	output, err := exec.Command(program, "-u",
		"-L", trustedConfigLabel, "-L", currentConfigLabel,
		trustedCopyPath, currentPath).Output()
	if err != nil {
		var exitError *exec.ExitError
		if !errors.As(err, &exitError) || exitError.ExitCode() != 1 {
			return "", err
		}
	}
	if withoutFileHeader(string(output)) == "" {
		return "", errNoDifference
	}
	return string(output), nil
}

// withoutFileHeader strips the two file-name header lines from a
// unified diff. Returns the diff from the first hunk on.
func withoutFileHeader(difference string) string {
	parts := strings.SplitAfterN(difference, "\n", 3)
	if len(parts) < 3 {
		return ""
	}
	return parts[2]
}

// printBothConfigurations prints the trusted copy and the current
// file in full. Skips the trusted copy if it cannot be read.
func printBothConfigurations(trustedCopyPath string, configurationContent []byte) {
	trustedContent, err := os.ReadFile(trustedCopyPath)
	if err == nil {
		fmt.Println("the file you trusted:")
		fmt.Println(ui.Indent(string(trustedContent), "  "))
	}
	fmt.Println("the file on disk now:")
	fmt.Println(ui.Indent(string(configurationContent), "  "))
}

// warnAboutWhatTheConfigurationGrants warns about each part of the
// config that widens what the box can do.
func warnAboutWhatTheConfigurationGrants(projectConfiguration *config.Project) {
	if len(projectConfiguration.Setup.Commands) > 0 {
		ui.Warn("the `[setup] commands` above run inside the box on the " +
			"next `prison up`")
	}
	if projectConfiguration.Prison.Inmates != nil {
		ui.Warn("the `[prison] inmates` above decide which tools that box " +
			"holds")
	}
	if len(projectConfiguration.Egress.Hosts) > 0 {
		ui.Warn("the `[egress] hosts` above widen what the box may reach on " +
			"the network")
	}
	if projectConfiguration.Box.Sudo != nil && *projectConfiguration.Box.Sudo {
		ui.Warn("`[box] sudo = true` grants root inside the box")
	}
}

// recordTrustedConfiguration saves the config hash into the project
// record and writes a copy for future diffs.
func recordTrustedConfiguration(
	currentSession *session.Session, configurationContent []byte,
) error {
	record := currentSession.Record
	if record == nil {
		record = &state.ProjectRecord{
			Path:    currentSession.Directory,
			Created: time.Now(),
		}
	}
	record.TrustedConfigSHA256 = currentSession.ConfigHash
	if err := currentSession.Project.SaveRecord(record); err != nil {
		return err
	}
	trustedCopyPath := currentSession.Project.TrustedConfigCopy()
	err := state.WriteFileAtomic(
		trustedCopyPath, configurationContent, trustedConfigCopyMode)
	if err != nil {
		return err
	}
	ui.Progress("trusted prison.toml; `prison up` applies it")
	return nil
}

// warnAboutUntrustedConfiguration warns that prison.toml is being
// skipped. Says nothing if the file is absent or trusted.
func warnAboutUntrustedConfiguration(currentSession *session.Session) {
	if !currentSession.ConfigPresent || currentSession.ConfigTrusted {
		return
	}
	if wasTrustedBefore(currentSession) {
		ui.Warn("prison.toml changed since it was trusted; " +
			"`prison trust` approves it")
		return
	}
	ui.Warn("prison.toml is present but not trusted; `prison trust` " +
		"approves it")
}

// wasTrustedBefore returns true if any version of prison.toml was
// approved before.
func wasTrustedBefore(currentSession *session.Session) bool {
	if currentSession.Record != nil &&
		currentSession.Record.TrustedConfigSHA256 != "" {
		return true
	}
	_, err := os.Stat(currentSession.Project.TrustedConfigCopy())
	return err == nil
}
