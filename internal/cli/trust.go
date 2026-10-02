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

const trustedConfigCopyMode = 0o600

const (
	trustedConfigLabel = "prison.toml (trusted)"
	currentConfigLabel = "prison.toml (now)"
)

var errNoDifference = errors.New("the files are the same")

func init() {
	registerGroup(addTrustCommand)
}

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

func runTrustCommand(approveWithoutAsking bool) error {
	currentSession, err := openSession()
	if err != nil {
		return err
	}
	if !currentSession.ConfigPresent {
		return fmt.Errorf("there is no %s in %s",
			session.ProjectConfigFileName, currentSession.Directory)
	}
	if currentSession.ConfigTrusted {
		ui.Progress("prison.toml is trusted")
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
		return ui.Exit(1, "not trusted")
	}
	return recordTrustedConfiguration(currentSession, configurationContent)
}

func showConfigurationBeingTrusted(
	currentSession *session.Session, configurationContent []byte,
	diffTool string,
) {
	trustedCopyPath := currentSession.Project.TrustedConfigCopy()
	if _, err := os.Stat(trustedCopyPath); err != nil {
		ui.Progress("prison.toml was never trusted:")
		fmt.Println(ui.Indent(string(configurationContent), "  "))
		return
	}
	difference, err := unifiedDifference(
		trustedCopyPath, currentSession.ConfigPath)
	if errors.Is(err, errNoDifference) {
		ui.Progress("prison.toml did not change:")
		fmt.Println(ui.Indent(string(configurationContent), "  "))
		return
	}
	ui.Progress("prison.toml changed after it was trusted:")
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

func resolveTrustDiffTool(currentSession *session.Session) string {
	diffTool := config.ResolveDiffTool(
		currentSession.Overrides, currentSession.Global)
	program := strings.Fields(diffTool)
	if len(program) == 0 {
		return ""
	}
	if _, err := exec.LookPath(program[0]); err != nil {
		ui.Warn("cannot use the diff tool `%s` because %s is not on PATH",
			diffTool, program[0])
		return ""
	}
	return diffTool
}

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

func withoutFileHeader(difference string) string {
	parts := strings.SplitAfterN(difference, "\n", 3)
	if len(parts) < 3 {
		return ""
	}
	return parts[2]
}

func printBothConfigurations(trustedCopyPath string, configurationContent []byte) {
	trustedContent, err := os.ReadFile(trustedCopyPath)
	if err == nil {
		fmt.Println("trusted file:")
		fmt.Println(ui.Indent(string(trustedContent), "  "))
	}
	fmt.Println("current file:")
	fmt.Println(ui.Indent(string(configurationContent), "  "))
}

func warnAboutWhatTheConfigurationGrants(projectConfiguration *config.Project) {
	if len(projectConfiguration.Setup.Commands) > 0 {
		ui.Warn("`[setup] commands` run in the box on the next `prison up`")
	}
	if projectConfiguration.Prison.Inmates != nil {
		ui.Warn("`[prison] inmates` sets the tools in the box")
	}
	if len(projectConfiguration.Egress.Hosts) > 0 {
		ui.Warn("`[egress] hosts` lets the box reach more hosts")
	}
	if projectConfiguration.Box.Sudo != nil && *projectConfiguration.Box.Sudo {
		ui.Warn("`[box] sudo = true` gives root access in the box")
	}
}

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
	ui.Progress("trusted prison.toml. Run `prison up` to apply it")
	return nil
}

func warnAboutUntrustedConfiguration(currentSession *session.Session) {
	if !currentSession.ConfigPresent || currentSession.ConfigTrusted {
		return
	}
	if wasTrustedBefore(currentSession) {
		ui.Warn("prison.toml changed after it was trusted. Run `prison trust`")
		return
	}
	ui.Warn("prison.toml is not trusted. Run `prison trust`")
}

func wasTrustedBefore(currentSession *session.Session) bool {
	if currentSession.Record != nil &&
		currentSession.Record.TrustedConfigSHA256 != "" {
		return true
	}
	_, err := os.Stat(currentSession.Project.TrustedConfigCopy())
	return err == nil
}
