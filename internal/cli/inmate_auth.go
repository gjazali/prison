package cli

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"prison/internal/broker/control"
	"prison/internal/plugin"
	"prison/internal/state"
	"prison/internal/ui"
	"prison/internal/vault"
)

func newInmateAuthCommand() *cobra.Command {
	group := &cobra.Command{
		Use:   "auth",
		Short: "manage inmate login credentials in the vault",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, arguments []string) error {
			return runInmateAuthList(command, false)
		},
	}
	group.PersistentFlags().IntVar(&secretPassphraseDescriptor,
		"passphrase-fd", -1,
		"read the vault passphrase from this file descriptor")
	group.AddCommand(
		newInmateAuthSetCommand(),
		newInmateAuthListCommand(),
		newInmateAuthRemoveCommand(),
		newInmateAuthUseCommand(),
		newInmateAuthClearCommand(),
	)
	return group
}

type inmateAuthSetOptions struct {
	inmate                      string
	profile                     string
	variable                    string
	fromEnvironment             string
	tokenURL                    string
	clientID                    string
	clientSecretFromEnvironment string
}

func newInmateAuthSetCommand() *cobra.Command {
	options := &inmateAuthSetOptions{}
	command := &cobra.Command{
		Use:   "set <inmate>",
		Short: "store or replace an inmate credential",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, arguments []string) error {
			options.inmate = arguments[0]
			return runInmateAuthSet(command, options)
		},
	}
	flags := command.Flags()
	flags.StringVar(&options.profile, "profile", vault.DefaultInmateProfile,
		"credential profile name, such as \"work\"")
	flags.StringVar(&options.variable, "variable", "",
		"credential variable from the inmate's manifest")
	flags.StringVar(&options.fromEnvironment, "from-environment", "",
		"read the value from this host environment variable")
	flags.StringVar(&options.tokenURL, "token-url", "",
		"OAuth token endpoint for a refresh token")
	flags.StringVar(&options.clientID, "client-id", "",
		"OAuth client ID for a refresh token")
	flags.StringVar(&options.clientSecretFromEnvironment,
		"client-secret-from-environment", "",
		"read the OAuth client secret from this host environment variable")
	return command
}

func newInmateAuthListCommand() *cobra.Command {
	var asJSON bool
	command := &cobra.Command{
		Use:   "list",
		Short: "list stored inmate credentials",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, arguments []string) error {
			return runInmateAuthList(command, asJSON)
		},
	}
	command.Flags().BoolVar(&asJSON, "json", false,
		"print as JSON")
	return command
}

type inmateAuthRemoveOptions struct {
	inmate   string
	profile  string
	variable string
}

func newInmateAuthRemoveCommand() *cobra.Command {
	options := &inmateAuthRemoveOptions{}
	command := &cobra.Command{
		Use:   "rm <inmate>",
		Short: "remove stored credentials for an inmate",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, arguments []string) error {
			options.inmate = arguments[0]
			return runInmateAuthRemove(command, options)
		},
	}
	flags := command.Flags()
	flags.StringVar(&options.profile, "profile", vault.DefaultInmateProfile,
		"credential profile name")
	flags.StringVar(&options.variable, "variable", "",
		"remove only this credential variable")
	return command
}

func newInmateAuthUseCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "use <inmate> <profile>",
		Short: "select a credential profile for this project",
		Args:  cobra.ExactArgs(2),
		RunE: func(command *cobra.Command, arguments []string) error {
			return runInmateAuthUse(arguments[0], arguments[1])
		},
	}
}

func newInmateAuthClearCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "clear <inmate>",
		Short: "revert this project to the default profile for an inmate",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, arguments []string) error {
			return runInmateAuthClear(arguments[0])
		},
	}
}

func runInmateAuthSet(command *cobra.Command, options *inmateAuthSetOptions) error {
	environment, err := loadEnvironment()
	if err != nil {
		return err
	}
	inmate, err := environment.Registry.Get(options.inmate)
	if err != nil {
		return fmt.Errorf("%w. Run `prison inmate list`", err)
	}
	if inmate.Auth == nil || len(inmate.Auth.Credentials) == 0 {
		return fmt.Errorf("the %s inmate has no [auth] configuration", inmate.Name)
	}
	profile := options.profile
	if profile == "" {
		profile = vault.DefaultInmateProfile
	}
	if err := vault.ValidateInmateProfile(profile); err != nil {
		return err
	}
	variable, err := resolveInmateCredentialVariable(
		inmate, options.variable, options.fromEnvironment)
	if err != nil {
		return err
	}
	spec := findInmateCredentialSpec(inmate, variable)
	oauthSpec, err := resolveInmateOAuthSpec(spec, options)
	if err != nil {
		return err
	}
	opened, err := openSecretVault(environment.Root)
	if err != nil {
		return err
	}
	defer opened.Lock()

	value, err := readInmateCredentialValue(variable, options.fromEnvironment)
	if err != nil {
		return err
	}
	credential := &vault.InmateCredential{
		Inmate:   inmate.Name,
		Profile:  profile,
		Variable: variable,
		Value:    value,
		OAuth:    oauthSpec,
		Created:  time.Now(),
	}
	replaced, err := opened.PutInmateCredential(credential)
	if err != nil {
		return err
	}
	verb := "saved"
	if replaced {
		verb = "replaced"
	}
	if profile == vault.DefaultInmateProfile {
		fmt.Fprintf(command.OutOrStdout(), "%s %s credential (%s)\n",
			verb, inmate.Name, variable)
		return nil
	}
	fmt.Fprintf(command.OutOrStdout(), "%s %s credential (%s, profile %s)\n",
		verb, inmate.Name, variable, profile)
	return nil
}

func resolveInmateCredentialVariable(
	inmate *plugin.Inmate, requested, fromEnvironment string,
) (string, error) {
	accepted := inmateCredentialNames(inmate)
	if requested != "" {
		for _, candidate := range accepted {
			if candidate == requested {
				return requested, nil
			}
		}
		return "", fmt.Errorf(
			"%s is not a credential of the %s inmate. Use %s",
			requested, inmate.Name, strings.Join(accepted, " or "))
	}
	if len(accepted) == 1 {
		return accepted[0], nil
	}
	if fromEnvironment != "" {
		for _, candidate := range accepted {
			if candidate == fromEnvironment {
				return candidate, nil
			}
		}
	}
	if !ui.StdinIsTerminal() {
		return "", fmt.Errorf(
			"the %s inmate accepts %s. Pass `--variable`",
			inmate.Name, strings.Join(accepted, " or "))
	}
	return promptForInmateCredentialVariable(inmate.Name, accepted)
}

func promptForInmateCredentialVariable(inmate string, choices []string) (string, error) {
	fmt.Fprintf(os.Stderr, "credential variable for %s:\n", inmate)
	for index, name := range choices {
		fmt.Fprintf(os.Stderr, "  %d) %s\n", index+1, name)
	}
	fmt.Fprintf(os.Stderr, "choose [1-%d]: ", len(choices))
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil && line == "" {
		return "", fmt.Errorf("cannot read selection: %w", err)
	}
	number, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil || number < 1 || number > len(choices) {
		return "", fmt.Errorf("choose a number from 1 to %d", len(choices))
	}
	return choices[number-1], nil
}

func inmateCredentialNames(inmate *plugin.Inmate) []string {
	names := make([]string, 0, len(inmate.Auth.Credentials))
	for _, credential := range inmate.Auth.Credentials {
		names = append(names, credential.Variable)
	}
	return names
}

func findInmateCredentialSpec(inmate *plugin.Inmate, variable string) plugin.Credential {
	for _, credential := range inmate.Auth.Credentials {
		if credential.Variable == variable {
			return credential
		}
	}
	return plugin.Credential{}
}

func resolveInmateOAuthSpec(spec plugin.Credential,
	options *inmateAuthSetOptions) (*vault.InmateOAuthSpec, error) {
	isOAuth := spec.TokenURL != "" || options.tokenURL != "" ||
		options.clientID != "" || options.clientSecretFromEnvironment != ""
	if !isOAuth {
		return nil, nil
	}
	tokenURL := options.tokenURL
	if tokenURL == "" {
		tokenURL = spec.TokenURL
	}
	if tokenURL == "" {
		return nil, errors.New("OAuth credentials need `--token-url`")
	}
	if err := vault.ValidateOAuthTokenURL(tokenURL); err != nil {
		return nil, err
	}
	clientID := options.clientID
	if clientID == "" {
		clientID = spec.ClientID
	}
	if clientID == "" && ui.StdinIsTerminal() {
		line, err := promptOptionalLine("OAuth client ID (optional): ")
		if err != nil {
			return nil, err
		}
		clientID = line
	}
	clientSecret := ""
	if options.clientSecretFromEnvironment != "" {
		clientSecret = os.Getenv(options.clientSecretFromEnvironment)
		if clientSecret == "" {
			return nil, fmt.Errorf("%s is not set", options.clientSecretFromEnvironment)
		}
	} else if ui.StdinIsTerminal() {
		secret, err := ui.ReadSecret("OAuth client secret (optional): ")
		if err != nil {
			return nil, err
		}
		clientSecret = secret
	}
	return &vault.InmateOAuthSpec{
		TokenURL:     tokenURL,
		ClientID:     clientID,
		ClientSecret: clientSecret,
	}, nil
}

func promptOptionalLine(prompt string) (string, error) {
	fmt.Fprint(os.Stderr, prompt)
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil && line == "" {
		return "", fmt.Errorf("cannot read input: %w", err)
	}
	return strings.TrimSpace(line), nil
}

func readInmateCredentialValue(variable, fromEnvironment string) (string, error) {
	if fromEnvironment != "" {
		value := os.Getenv(fromEnvironment)
		if value == "" {
			return "", fmt.Errorf("%s is not set", fromEnvironment)
		}
		return value, nil
	}
	if ui.StdinIsTerminal() {
		value, err := ui.ReadSecret(variable + " value: ")
		if err != nil {
			return "", err
		}
		if value == "" {
			return "", errors.New("no value given")
		}
		return value, nil
	}
	piped, err := io.ReadAll(os.Stdin)
	if err != nil {
		return "", fmt.Errorf("cannot read the value from standard input: %w", err)
	}
	value := strings.TrimSuffix(string(piped), "\n")
	if value == "" {
		return "", errors.New("no value. Pipe it in or use `--from-environment`")
	}
	return value, nil
}

type inmateCredentialJSON struct {
	Inmate   string    `json:"inmate"`
	Profile  string    `json:"profile"`
	Variable string    `json:"variable"`
	OAuth    bool      `json:"oauth,omitempty"`
	TokenURL string    `json:"token_url,omitempty"`
	Digest   string    `json:"digest"`
	Created  time.Time `json:"created"`
}

func runInmateAuthList(command *cobra.Command, asJSON bool) error {
	environment, err := loadEnvironment()
	if err != nil {
		return err
	}
	opened, err := openSecretVault(environment.Root)
	if err != nil {
		return err
	}
	defer opened.Lock()

	credentials := opened.InmateCredentials()
	if asJSON {
		items := make([]inmateCredentialJSON, 0, len(credentials))
		for _, credential := range credentials {
			item := inmateCredentialJSON{
				Inmate:   credential.Inmate,
				Profile:  credential.Profile,
				Variable: credential.Variable,
				Digest:   vault.Digest(credential.Value),
				Created:  credential.Created,
			}
			if credential.OAuth != nil {
				item.OAuth = true
				item.TokenURL = credential.OAuth.TokenURL
			}
			items = append(items, item)
		}
		encoded, err := json.MarshalIndent(items, "", "  ")
		if err != nil {
			return err
		}
		fmt.Fprintln(command.OutOrStdout(), string(encoded))
		return nil
	}
	if len(credentials) == 0 {
		fmt.Fprintln(command.OutOrStdout(),
			"no inmate credentials. Run `prison inmate auth set <inmate>`")
		return nil
	}
	boundProjects, err := inmateProfileProjects(environment.Root)
	if err != nil {
		return err
	}
	rows := [][]string{{"INMATE", "PROFILE", "VARIABLE", "TYPE", "DIGEST", "PROJECTS"}}
	for _, credential := range credentials {
		projectsColumn := "(default)"
		if credential.Profile != vault.DefaultInmateProfile {
			projectsColumn = strconv.Itoa(
				len(boundProjects[inmateProfileKey(credential.Inmate, credential.Profile)]))
		}
		kind := "static"
		if credential.OAuth != nil {
			kind = "oauth"
		}
		rows = append(rows, []string{
			credential.Inmate,
			credential.Profile,
			credential.Variable,
			kind,
			vault.Digest(credential.Value),
			projectsColumn,
		})
	}
	return ui.Table(command.OutOrStdout(), rows)
}

func runInmateAuthRemove(command *cobra.Command, options *inmateAuthRemoveOptions) error {
	environment, err := loadEnvironment()
	if err != nil {
		return err
	}
	inmateName := options.inmate
	if resolved, err := environment.Registry.Get(options.inmate); err == nil {
		inmateName = resolved.Name
	}
	profile := options.profile
	if profile == "" {
		profile = vault.DefaultInmateProfile
	}
	if err := vault.ValidateInmateProfile(profile); err != nil {
		return err
	}
	opened, err := openSecretVault(environment.Root)
	if err != nil {
		return err
	}
	defer opened.Lock()

	if options.variable != "" {
		removed, err := opened.DeleteInmateCredential(inmateName, profile, options.variable)
		if err != nil {
			return err
		}
		if !removed {
			return fmt.Errorf("no %s credential for %s in profile %q",
				options.variable, inmateName, profile)
		}
		fmt.Fprintf(command.OutOrStdout(), "removed %s credential (%s, profile %s)\n",
			inmateName, options.variable, profile)
	} else {
		count, err := opened.DeleteInmateProfileCredentials(inmateName, profile)
		if err != nil {
			return err
		}
		if count == 0 {
			return fmt.Errorf("no credentials for %s in profile %q",
				inmateName, profile)
		}
		if profile == vault.DefaultInmateProfile {
			fmt.Fprintf(command.OutOrStdout(), "removed %s credentials\n", inmateName)
		} else {
			fmt.Fprintf(command.OutOrStdout(), "removed %s credentials (profile %s)\n",
				inmateName, profile)
		}
	}

	if profile != vault.DefaultInmateProfile && !vaultHasInmateProfile(opened, inmateName, profile) {
		if bound, err := inmateProfileProjects(environment.Root); err == nil {
			count := len(bound[inmateProfileKey(inmateName, profile)])
			switch count {
			case 0:
			case 1:
				fmt.Fprintln(command.OutOrStdout(),
					"1 project still uses this profile")
			default:
				fmt.Fprintf(command.OutOrStdout(),
					"%d projects still use this profile\n", count)
			}
		}
	}
	return nil
}

func runInmateAuthUse(inmateArg, profile string) error {
	environment, err := loadEnvironment()
	if err != nil {
		return err
	}
	inmate, err := environment.Registry.Get(inmateArg)
	if err != nil {
		return fmt.Errorf("%w. Run `prison inmate list`", err)
	}
	if inmate.Auth == nil || len(inmate.Auth.Credentials) == 0 {
		return fmt.Errorf("the %s inmate has no [auth] configuration", inmate.Name)
	}
	if err := vault.ValidateInmateProfile(profile); err != nil {
		return err
	}
	project, err := currentSecretProject(environment)
	if err != nil {
		return err
	}
	if profile != vault.DefaultInmateProfile {
		opened, err := openSecretVault(environment.Root)
		if err != nil {
			return err
		}
		hasProfile := vaultHasInmateProfile(opened, inmate.Name, profile)
		opened.Lock()
		if !hasProfile {
			return fmt.Errorf(
				"no credential for %s in profile %q. "+
					"Run `prison inmate auth set %s --profile %s`",
				inmate.Name, profile, inmate.Name, profile)
		}
	}
	if err := project.SetInmateAuthProfile(inmate.Name, profile); err != nil {
		return err
	}
	reloadRunningBroker(environment.Root)
	if profile == vault.DefaultInmateProfile {
		ui.Progress("using default profile for %s in %s", inmate.Name, project.ID)
		return nil
	}
	ui.Progress("using profile %s for %s in %s", profile, inmate.Name, project.ID)
	return nil
}

func runInmateAuthClear(inmateArg string) error {
	environment, err := loadEnvironment()
	if err != nil {
		return err
	}
	inmateName := inmateArg
	if resolved, err := environment.Registry.Get(inmateArg); err == nil {
		inmateName = resolved.Name
	}
	project, err := currentSecretProject(environment)
	if err != nil {
		return err
	}
	profiles, err := project.InmateAuthProfiles()
	if err != nil {
		return err
	}
	if profiles[inmateName] == "" {
		ui.Progress("this project already uses the default profile for %s", inmateName)
		return nil
	}
	if err := project.SetInmateAuthProfile(inmateName, ""); err != nil {
		return err
	}
	reloadRunningBroker(environment.Root)
	ui.Progress("using default profile for %s in %s", inmateName, project.ID)
	return nil
}

func reloadRunningBroker(root *state.Root) {
	ctx, cancel := commandContext()
	defer cancel()
	client := control.NewClient(root.BrokerSocket())
	if client.Alive(ctx) {
		_ = client.Reload(ctx)
	}
}

func vaultHasInmateProfile(opened *vault.Vault, inmate, profile string) bool {
	for _, credential := range opened.InmateCredentials() {
		if credential.Inmate == inmate && credential.Profile == profile {
			return true
		}
	}
	return false
}

func inmateProfileKey(inmate, profile string) string {
	return inmate + "/" + profile
}

func inmateProfileProjects(root *state.Root) (map[string][]string, error) {
	projects, err := root.ListProjects()
	if err != nil {
		return nil, err
	}
	byProfile := map[string][]string{}
	for _, project := range projects {
		profiles, err := project.InmateAuthProfiles()
		if err != nil {
			continue
		}
		for inmate, profile := range profiles {
			if profile == "" || profile == vault.DefaultInmateProfile {
				continue
			}
			key := inmateProfileKey(inmate, profile)
			byProfile[key] = append(byProfile[key], project.ID)
		}
	}
	return byProfile, nil
}
