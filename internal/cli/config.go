package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"prison/internal/config"
	"prison/internal/ecosystem"
	"prison/internal/plugin"
	"prison/internal/policy"
	"prison/internal/session"
	"prison/internal/state"
	"prison/internal/ui"
)

func init() {
	registerGroup(addConfigCommands)
}

func addConfigCommands(root *cobra.Command) {
	group := &cobra.Command{
		Use:   "config",
		Short: "manage this project's prison.toml",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, arguments []string) error {
			return command.Help()
		},
	}
	group.AddCommand(newConfigInitCommand())
	root.AddCommand(group)
}

type configInitOptions struct {
	source   string
	noDetect bool
	noDocs   bool
}

func newConfigInitCommand() *cobra.Command {
	options := &configInitOptions{}
	command := &cobra.Command{
		Use:   "init",
		Short: "interactively create or update this project's prison.toml",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, arguments []string) error {
			return runConfigInit(command, options)
		},
	}
	flags := command.Flags()
	flags.StringVar(&options.source, "source", "",
		"local ecosystem catalog directory to use instead of the installed catalog")
	flags.BoolVar(&options.noDetect, "no-detect", false,
		"do not auto-detect project ecosystems")
	flags.BoolVar(&options.noDocs, "no-docs", false,
		"do not add official documentation hosts from detected ecosystems")
	return command
}

func runConfigInit(command *cobra.Command, options *configInitOptions) error {
	if !ui.StdinIsTerminal() {
		return errors.New("`prison config init` needs an interactive terminal")
	}
	environment, err := loadEnvironment()
	if err != nil {
		return err
	}
	directory, err := os.Getwd()
	if err != nil {
		return err
	}
	return runConfigInitWizard(
		environment, directory, options,
		os.Stdin, command.OutOrStdout(), os.Stderr,
	)
}

type wizardPrompter struct {
	reader *bufio.Reader
	out    io.Writer
}

func (p *wizardPrompter) line(prompt string) (string, error) {
	fmt.Fprint(p.out, prompt)
	text, err := p.reader.ReadString('\n')
	if err != nil && text == "" {
		return "", err
	}
	return strings.TrimSpace(text), nil
}

func (p *wizardPrompter) confirm(question string, defaultYes bool) (bool, error) {
	hint := "[y/N]"
	if defaultYes {
		hint = "[Y/n]"
	}
	for {
		answer, err := p.line(fmt.Sprintf("%s %s ", question, hint))
		if err != nil {
			if errors.Is(err, io.EOF) {
				return defaultYes, nil
			}
			return false, err
		}
		switch strings.ToLower(answer) {
		case "":
			return defaultYes, nil
		case "y", "yes":
			return true, nil
		case "n", "no":
			return false, nil
		default:
			fmt.Fprintln(p.out, "please answer y or n")
		}
	}
}

func runConfigInitWizard(
	environment *session.Environment,
	directory string,
	options *configInitOptions,
	in io.Reader,
	out io.Writer,
	promptOut io.Writer,
) error {
	configPath := filepath.Join(directory, session.ProjectConfigFileName)
	existingBytes, err := os.ReadFile(configPath)
	existed := true
	var existing *config.Project
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			existed = false
			existingBytes = nil
		} else {
			return fmt.Errorf("cannot read %s: %w", configPath, err)
		}
	} else {
		parsed, parseErr := config.LoadProject(configPath, existingBytes)
		if parseErr != nil {
			return parseErr
		}
		existing = parsed
	}

	prompter := &wizardPrompter{reader: bufio.NewReader(in), out: promptOut}
	patch := config.ProjectPatch{}

	if !options.noDetect {
		if err := applyWizardEcosystemDetection(
			environment, directory, options, prompter, &patch,
		); err != nil {
			return err
		}
	}

	inmates, err := promptWizardInmates(environment, existing, prompter)
	if err != nil {
		return err
	}
	patch.Inmates = inmates

	ports, err := promptWizardPorts(existing, prompter)
	if err != nil {
		return err
	}
	patch.Ports = ports

	sudoChoice, err := promptWizardSudo(existing, prompter)
	if err != nil {
		return err
	}
	patch.Sudo = sudoChoice

	existingShadow := existingShadowList(existing)
	extraShadow, err := promptWizardShadow(existingShadow, patch.AddShadow, prompter)
	if err != nil {
		return err
	}
	patch.AddShadow = append(patch.AddShadow, extraShadow...)

	existingEgress := existingEgressList(existing)
	extraEgress, err := promptWizardEgress(existingEgress, patch.AddEgressHosts, prompter)
	if err != nil {
		return err
	}
	patch.AddEgressHosts = append(patch.AddEgressHosts, extraEgress...)

	proposed, changed, err := config.ApplyProjectPatch(existingBytes, patch)
	if err != nil {
		return err
	}
	if !changed {
		fmt.Fprintln(out, "no changes to prison.toml")
		return nil
	}

	showProposedConfig(out, environment, existingBytes, proposed, existed)
	writeConfirmed, err := prompter.confirm("write prison.toml?", true)
	if err != nil {
		return err
	}
	if !writeConfirmed {
		return ui.Exit(1, "not written")
	}
	if err := state.WriteFileAtomic(configPath, proposed, 0o644); err != nil {
		return err
	}

	trustConfirmed, err := prompter.confirm("trust prison.toml now?", true)
	if err != nil {
		return err
	}
	if trustConfirmed {
		currentSession, err := environment.Open(directory)
		if err != nil {
			return err
		}
		return recordTrustedConfiguration(currentSession, proposed)
	}
	fmt.Fprintln(out, "wrote prison.toml. Run `prison trust` to approve it")
	return nil
}

func applyWizardEcosystemDetection(
	environment *session.Environment,
	directory string,
	options *configInitOptions,
	prompter *wizardPrompter,
	patch *config.ProjectPatch,
) error {
	catalog, err := loadEcosystemCatalog(environment, options.source)
	if err != nil {
		if options.source != "" {
			return err
		}
		return nil
	}
	detection, err := ecosystem.Detect(directory, catalog)
	if err != nil || len(detection.Matched) == 0 {
		return err
	}
	names := make([]string, 0, len(detection.Matched))
	for _, eco := range detection.Matched {
		names = append(names, eco.Name)
	}
	fmt.Fprintf(prompter.out, "detected ecosystems: %s\n", strings.Join(names, ", "))
	include, err := prompter.confirm(
		"include detected ecosystem settings (shadow, setup, ignore, egress)?", true)
	if err != nil {
		return err
	}
	if !include {
		return nil
	}
	patch.AddShadow = append(patch.AddShadow, detection.Shadow...)
	patch.AddSetupCommands = append(patch.AddSetupCommands, detection.SetupCommands...)
	patch.AddIgnore = append(patch.AddIgnore, detection.CheckpointIgnore...)
	patch.AddEgressHosts = append(patch.AddEgressHosts, detection.EgressHosts...)
	if !options.noDocs {
		patch.AddEgressHosts = append(patch.AddEgressHosts, detection.EgressDocs...)
	}
	return nil
}

func promptWizardInmates(
	environment *session.Environment,
	existing *config.Project,
	prompter *wizardPrompter,
) (*[]string, error) {
	available, err := usableInmates(environment.Registry)
	if err != nil {
		return nil, err
	}
	if len(available) > 0 {
		fmt.Fprintln(prompter.out, "\navailable inmates:")
		for index, inmate := range available {
			if inmate.Inmate.Description != "" {
				fmt.Fprintf(prompter.out, "  %d) %s (%s)\n",
					index+1, inmate.Name, inmate.Inmate.Description)
			} else {
				fmt.Fprintf(prompter.out, "  %d) %s\n", index+1, inmate.Name)
			}
		}
	}
	switch {
	case existing != nil && existing.Prison.Inmates != nil:
		fmt.Fprintf(prompter.out, "current [prison] inmates: %s\n",
			describeStringList(*existing.Prison.Inmates))
	case environment.Global != nil && environment.Global.Prison.Inmates != nil &&
		len(*environment.Global.Prison.Inmates) > 0:
		fmt.Fprintf(prompter.out, "global default inmates: %s\n",
			strings.Join(*environment.Global.Prison.Inmates, ", "))
	}

	for {
		answer, err := prompter.line(
			"inmates (numbers or names, comma-separated; Enter to keep/default, \"none\" for []): ")
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil, nil
			}
			return nil, err
		}
		if answer == "" {
			return nil, nil
		}
		if strings.EqualFold(answer, "none") {
			empty := []string{}
			return &empty, nil
		}
		selected, parseErr := parseWizardInmates(answer, available, environment.Registry)
		if parseErr != nil {
			fmt.Fprintf(prompter.out, "invalid inmates: %v\n", parseErr)
			continue
		}
		return &selected, nil
	}
}

func usableInmates(registry *plugin.Registry) ([]*plugin.Inmate, error) {
	all, err := registry.List()
	if err != nil {
		return nil, err
	}
	var usable []*plugin.Inmate
	for _, inmate := range all {
		if inmate.Problem != nil {
			continue
		}
		if !inmate.Bundled && !inmate.Trusted {
			continue
		}
		usable = append(usable, inmate)
	}
	return usable, nil
}

func parseWizardInmates(
	input string, available []*plugin.Inmate, registry *plugin.Registry,
) ([]string, error) {
	tokens := splitCommaList(input)
	if len(tokens) == 0 {
		return nil, errors.New("enter at least one inmate or \"none\"")
	}
	seen := map[string]bool{}
	selected := make([]string, 0, len(tokens))
	for _, token := range tokens {
		var spec string
		if number, err := strconv.Atoi(token); err == nil {
			if number < 1 || number > len(available) {
				return nil, fmt.Errorf("choice %d is out of range (1-%d)",
					number, len(available))
			}
			spec = available[number-1].Name
		} else {
			name, version, hasVersion := strings.Cut(token, ":")
			resolved, err := registry.Get(name)
			if err != nil {
				return nil, err
			}
			if resolved.Problem != nil {
				return nil, fmt.Errorf("inmate %q cannot be used: %v", resolved.Name, resolved.Problem)
			}
			if !resolved.Bundled && !resolved.Trusted {
				return nil, fmt.Errorf("inmate %q is not trusted; run `prison inmate trust %s` first",
					resolved.Name, resolved.Name)
			}
			spec = resolved.Name
			if hasVersion {
				spec += ":" + version
			}
			if err := config.ValidateInmateSpec(spec); err != nil {
				return nil, err
			}
		}
		if !seen[spec] {
			seen[spec] = true
			selected = append(selected, spec)
		}
	}
	return selected, nil
}

func promptWizardPorts(existing *config.Project, prompter *wizardPrompter) (*[]int, error) {
	if existing != nil && existing.Box.Ports != nil {
		fmt.Fprintf(prompter.out, "current [box] ports: %s\n",
			describeIntList(*existing.Box.Ports))
	}
	for {
		answer, err := prompter.line(
			"ports to publish (comma-separated, e.g. 3000,8080; Enter for default, \"none\" for []): ")
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil, nil
			}
			return nil, err
		}
		if answer == "" {
			return nil, nil
		}
		if strings.EqualFold(answer, "none") {
			empty := []int{}
			return &empty, nil
		}
		ports, parseErr := parseWizardPorts(answer)
		if parseErr != nil {
			fmt.Fprintf(prompter.out, "invalid ports: %v\n", parseErr)
			continue
		}
		return &ports, nil
	}
}

func parseWizardPorts(input string) ([]int, error) {
	tokens := splitCommaList(input)
	if len(tokens) == 0 {
		return nil, errors.New("enter port numbers or \"none\"")
	}
	seen := map[int]bool{}
	ports := make([]int, 0, len(tokens))
	for _, token := range tokens {
		port, err := strconv.Atoi(token)
		if err != nil || port < 1 || port > 65535 {
			return nil, fmt.Errorf("%q must be a port from 1 to 65535", token)
		}
		if !seen[port] {
			seen[port] = true
			ports = append(ports, port)
		}
	}
	return ports, nil
}

func promptWizardSudo(existing *config.Project, prompter *wizardPrompter) (*bool, error) {
	current := false
	hasExisting := existing != nil && existing.Box.Sudo != nil
	if hasExisting {
		current = *existing.Box.Sudo
	}
	answer, err := prompter.confirm("allow sudo inside the box?", current)
	if err != nil {
		return nil, err
	}
	if !hasExisting && !answer {
		return nil, nil
	}
	return &answer, nil
}

func promptWizardShadow(
	existing, detected []string, prompter *wizardPrompter,
) ([]string, error) {
	prefilled := mergeUniqueStrings(existing, detected)
	prompt := "shadow directories (comma-separated relative paths, e.g. node_modules,.venv; Enter for none): "
	if len(prefilled) > 0 {
		fmt.Fprintf(prompter.out, "shadow directories (current/detected): %s\n",
			strings.Join(prefilled, ", "))
		prompt = "additional shadow directories (comma-separated; Enter for none): "
	}
	for {
		answer, err := prompter.line(prompt)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil, nil
			}
			return nil, err
		}
		if answer == "" {
			return nil, nil
		}
		paths := splitCommaList(answer)
		if err := validateWizardShadowPaths(prefilled, paths); err != nil {
			fmt.Fprintf(prompter.out, "invalid shadow directories: %v\n", err)
			continue
		}
		return paths, nil
	}
}

func validateWizardShadowPaths(existing, additions []string) error {
	combined := mergeUniqueStrings(existing, additions)
	candidate, _, err := config.ApplyProjectPatch(nil, config.ProjectPatch{
		AddShadow: combined,
	})
	if err != nil {
		return err
	}
	if len(candidate) == 0 {
		return nil
	}
	_, err = config.LoadProject("prison.toml", candidate)
	return err
}

func promptWizardEgress(
	existing, detected []string, prompter *wizardPrompter,
) ([]string, error) {
	prefilled := mergeUniqueStrings(existing, detected)
	prompt := "egress hosts (comma-separated, e.g. api.example.com,example.org:8081; Enter for none): "
	if len(prefilled) > 0 {
		fmt.Fprintf(prompter.out, "egress hosts (current/detected): %s\n",
			strings.Join(prefilled, ", "))
		prompt = "additional egress hosts (comma-separated; Enter for none): "
	}
	for {
		answer, err := prompter.line(prompt)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil, nil
			}
			return nil, err
		}
		if answer == "" {
			return nil, nil
		}
		hosts := splitCommaList(answer)
		var badErr error
		for _, host := range hosts {
			if _, err := policy.ParsePattern(host); err != nil {
				badErr = err
				break
			}
		}
		if badErr != nil {
			fmt.Fprintf(prompter.out, "invalid egress host: %v\n", badErr)
			continue
		}
		return hosts, nil
	}
}

func splitCommaList(input string) []string {
	var items []string
	for _, part := range strings.Split(input, ",") {
		trimmed := strings.TrimSpace(part)
		if trimmed != "" {
			items = append(items, trimmed)
		}
	}
	return items
}

func mergeUniqueStrings(lists ...[]string) []string {
	seen := map[string]bool{}
	var merged []string
	for _, list := range lists {
		for _, item := range list {
			if item == "" || seen[item] {
				continue
			}
			seen[item] = true
			merged = append(merged, item)
		}
	}
	return merged
}

func existingShadowList(existing *config.Project) []string {
	if existing == nil {
		return nil
	}
	return existing.Box.Shadow
}

func existingEgressList(existing *config.Project) []string {
	if existing == nil {
		return nil
	}
	return existing.Egress.Hosts
}

func describeStringList(values []string) string {
	if len(values) == 0 {
		return "none ([])"
	}
	return strings.Join(values, ", ")
}

func describeIntList(values []int) string {
	if len(values) == 0 {
		return "none ([])"
	}
	parts := make([]string, len(values))
	for index, value := range values {
		parts[index] = strconv.Itoa(value)
	}
	return strings.Join(parts, ", ")
}
