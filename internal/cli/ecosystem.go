package cli

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"prison/internal/checkpoint/textdiff"
	"prison/internal/config"
	"prison/internal/ecosystem"
	"prison/internal/session"
	"prison/internal/state"
	"prison/internal/ui"
)

func init() {
	registerGroup(addEcosystemCommands)
}

func addEcosystemCommands(root *cobra.Command) {
	group := &cobra.Command{
		Use:   "ecosystem",
		Short: "manage ecosystem definitions and detect project settings",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, arguments []string) error {
			return runEcosystemList(command, "")
		},
	}
	group.AddCommand(
		newEcosystemInitCommand(),
		newEcosystemUpdateCommand(),
		newEcosystemListCommand(),
		newEcosystemShowCommand(),
	)
	root.AddCommand(group)
}

func newEcosystemInitCommand() *cobra.Command {
	var source string
	var dryRun bool
	var assumeYes bool
	var noDocs bool
	command := &cobra.Command{
		Use:   "init",
		Short: "detect this project's ecosystems and propose edits to prison.toml",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, arguments []string) error {
			return runEcosystemInit(command, source, dryRun, assumeYes, noDocs)
		},
	}
	command.Flags().StringVar(&source, "source", "",
		"local ecosystem catalog directory to use instead of the installed catalog")
	command.Flags().BoolVar(&dryRun, "dry-run", false,
		"show the proposed prison.toml changes without writing")
	command.Flags().BoolVar(&assumeYes, "yes", false,
		"write prison.toml without prompting")
	command.Flags().BoolVar(&noDocs, "no-docs", false,
		"do not add official documentation hosts to [egress] hosts")
	return command
}

func newEcosystemUpdateCommand() *cobra.Command {
	var ref string
	command := &cobra.Command{
		Use:   "update [source]",
		Short: "fetch or refresh the ecosystem catalog from a git URL or local directory",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(command *cobra.Command, arguments []string) error {
			source := ""
			if len(arguments) == 1 {
				source = arguments[0]
			}
			return runEcosystemUpdate(command, source, ref)
		},
	}
	command.Flags().StringVar(&ref, "ref", "",
		"branch or tag to fetch")
	return command
}

func newEcosystemListCommand() *cobra.Command {
	var source string
	command := &cobra.Command{
		Use:   "list",
		Short: "list ecosystems in the catalog",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, arguments []string) error {
			return runEcosystemList(command, source)
		},
	}
	command.Flags().StringVar(&source, "source", "",
		"local ecosystem catalog directory to read")
	return command
}

func newEcosystemShowCommand() *cobra.Command {
	var source string
	command := &cobra.Command{
		Use:   "show <name>",
		Short: "show one ecosystem's definition",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, arguments []string) error {
			return runEcosystemShow(command, arguments[0], source)
		},
	}
	command.Flags().StringVar(&source, "source", "",
		"local ecosystem catalog directory to read")
	return command
}

func resolveCatalogDirectory(environment *session.Environment, explicit string) string {
	if explicit != "" {
		return explicit
	}
	if environment.Overrides.Ecosystems != "" {
		return environment.Overrides.Ecosystems
	}
	return environment.Root.EcosystemsDir()
}

func loadEcosystemCatalog(
	environment *session.Environment, explicit string,
) (*ecosystem.Catalog, error) {
	dir := resolveCatalogDirectory(environment, explicit)
	catalog, err := ecosystem.LoadCatalog(dir)
	if err != nil {
		if explicit == "" && environment.Overrides.Ecosystems == "" {
			if _, statErr := os.Stat(dir); errors.Is(statErr, fs.ErrNotExist) {
				return nil, fmt.Errorf(
					"no ecosystem catalog installed. Run `prison ecosystem update` " +
						"(or set PRISON_ECOSYSTEMS)")
			}
		}
		return nil, err
	}
	return catalog, nil
}

func runEcosystemUpdate(command *cobra.Command, source, ref string) error {
	environment, err := loadEnvironment()
	if err != nil {
		return err
	}
	ctx, cancel := commandContext()
	defer cancel()
	catalog, origin, err := ecosystem.UpdateCatalog(
		ctx, source, ref, environment.Root.EcosystemsDir())
	if err != nil {
		return err
	}
	fmt.Fprintf(command.OutOrStdout(),
		"loaded %d ecosystem(s) from %s\n", len(catalog.Ecosystems), origin.Source)
	return nil
}

func runEcosystemList(command *cobra.Command, source string) error {
	environment, err := loadEnvironment()
	if err != nil {
		return err
	}
	catalog, err := loadEcosystemCatalog(environment, source)
	if err != nil {
		return err
	}
	rows := [][]string{{"NAME", "GROUP", "DESCRIPTION"}}
	for _, eco := range catalog.Ecosystems {
		group := eco.Group
		if group == "" {
			group = "-"
		}
		rows = append(rows, []string{eco.Name, group, eco.Description})
	}
	return ui.Table(command.OutOrStdout(), rows)
}

func runEcosystemShow(command *cobra.Command, name, source string) error {
	environment, err := loadEnvironment()
	if err != nil {
		return err
	}
	catalog, err := loadEcosystemCatalog(environment, source)
	if err != nil {
		return err
	}
	eco, ok := catalog.Get(name)
	if !ok {
		return fmt.Errorf("no ecosystem called %q. Run `prison ecosystem list`", name)
	}
	out := command.OutOrStdout()
	writeField := func(key, value string) {
		fmt.Fprintf(out, "%-12s %s\n", key, value)
	}
	writeField("name", eco.Name)
	writeField("description", eco.Description)
	if eco.Group != "" {
		writeField("group", fmt.Sprintf("%s (priority %d)", eco.Group, eco.Priority))
	}
	if len(eco.Detect.Files) > 0 {
		writeField("files", strings.Join(eco.Detect.Files, ", "))
	}
	if len(eco.Detect.AllFiles) > 0 {
		writeField("all files", strings.Join(eco.Detect.AllFiles, ", "))
	}
	for _, matcher := range eco.Detect.Match {
		writeField("match", fmt.Sprintf("%s =~ %s", matcher.File, matcher.Pattern))
	}
	if len(eco.Box.Shadow) > 0 {
		writeField("shadow", strings.Join(eco.Box.Shadow, ", "))
	}
	for _, cmd := range eco.Setup.Commands {
		writeField("setup", cmd)
	}
	if len(eco.Checkpoint.Ignore) > 0 {
		writeField("ignore", strings.Join(eco.Checkpoint.Ignore, ", "))
	}
	for _, host := range eco.Egress.Hosts {
		writeField("egress", host)
	}
	for _, host := range eco.Egress.Docs {
		writeField("docs", host)
	}
	return nil
}

func runEcosystemInit(
	command *cobra.Command, source string, dryRun, assumeYes, noDocs bool,
) error {
	environment, err := loadEnvironment()
	if err != nil {
		return err
	}
	catalog, err := loadEcosystemCatalog(environment, source)
	if err != nil {
		return err
	}
	directory, err := os.Getwd()
	if err != nil {
		return err
	}
	detection, err := ecosystem.Detect(directory, catalog)
	if err != nil {
		return err
	}
	out := command.OutOrStdout()
	if len(detection.Matched) == 0 {
		fmt.Fprintln(out, "no known ecosystems detected in this directory")
		return nil
	}

	names := make([]string, 0, len(detection.Matched))
	for _, eco := range detection.Matched {
		names = append(names, eco.Name)
	}
	fmt.Fprintf(out, "detected ecosystems: %s\n", strings.Join(names, ", "))

	configPath := filepath.Join(directory, session.ProjectConfigFileName)
	existing, err := os.ReadFile(configPath)
	existed := true
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			existed = false
			existing = nil
		} else {
			return fmt.Errorf("cannot read %s: %w", configPath, err)
		}
	}

	proposed, changed, err := ecosystem.ProposeProjectConfig(
		existing, detection, ecosystem.ProposeOptions{SkipDocs: noDocs})
	if err != nil {
		return err
	}
	if !changed {
		fmt.Fprintln(out, "prison.toml already includes the detected ecosystem settings")
		return nil
	}

	showProposedConfig(out, environment, existing, proposed, existed)
	if dryRun {
		return nil
	}
	if !assumeYes && !ui.Confirm("write prison.toml?") {
		return ui.Exit(1, "not written")
	}
	if err := state.WriteFileAtomic(configPath, proposed, 0o644); err != nil {
		return err
	}
	fmt.Fprintln(out, "wrote prison.toml. Run `prison trust` to approve it")
	return nil
}

func showProposedConfig(
	out io.Writer, environment *session.Environment,
	existing, proposed []byte, existed bool,
) {
	if !existed {
		fmt.Fprintf(out, "\nproposed prison.toml:\n%s\n",
			ui.Indent(string(proposed), "  "))
		return
	}
	beforeLines := splitTerminatedLines(string(existing))
	afterLines := splitTerminatedLines(string(proposed))
	unified := textdiff.Unified(
		beforeLines, afterLines,
		"prison.toml (current)", "prison.toml (proposed)",
		3, true)
	diffText := strings.Join(unified, "\n") + "\n"
	diffTool := config.ResolveDiffTool(environment.Overrides, environment.Global)
	if out == os.Stdout && diffTool != "" {
		if err := pipeThroughCommand(diffTool, strings.NewReader(diffText)); err == nil {
			fmt.Fprintln(out)
			return
		}
	}
	fmt.Fprintf(out, "\n%s\n", diffText)
}

func splitTerminatedLines(text string) []string {
	if text == "" {
		return nil
	}
	var lines []string
	start := 0
	for index := 0; index < len(text); index++ {
		if text[index] == '\n' {
			lines = append(lines, text[start:index+1])
			start = index + 1
		}
	}
	if start < len(text) {
		lines = append(lines, text[start:])
	}
	return lines
}
