package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"prison/internal/broker/brokerlog"
	"prison/internal/broker/control"
	"prison/internal/session"
	"prison/internal/state"
	"prison/internal/ui"
	"prison/internal/vault"
)

const secretLabelWidth = 12

const secretLogDefaultLimit = 40

const secretLogTimeLayout = "2006-01-02T15:04:05"

// passphrasePrompt is a function type so that tests can run without a
// terminal.
type passphrasePrompt func(prompt string) (string, error)

func init() {
	registerGroup(addSecretCommands)
}

func addSecretCommands(root *cobra.Command) {
	group := &cobra.Command{
		Use:   "secret",
		Short: "manage secrets",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, arguments []string) error {
			return runSecretList(false, promptForVaultPassphrase)
		},
	}
	group.PersistentFlags().IntVar(&secretPassphraseDescriptor,
		"passphrase-fd", -1,
		"read the vault passphrase from this file descriptor")
	group.AddCommand(
		newSecretInitCommand(),
		newSecretAddCommand(),
		newSecretListCommand(),
		newSecretShowCommand(),
		newSecretRemoveCommand(),
		newSecretGrantCommand(),
		newSecretRevokeCommand(),
		newSecretGrantsCommand(),
		newSecretLogCommand(),
		newSecretUnlockCommand(),
	)
	root.AddCommand(group)
}

func newSecretInitCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "create the vault",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, arguments []string) error {
			return runSecretInit(promptForVaultPassphrase)
		},
	}
}

func newSecretAddCommand() *cobra.Command {
	options := &secretAddOptions{}
	command := &cobra.Command{
		Use:   "add <name>",
		Short: "add or replace a secret",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, arguments []string) error {
			options.name = arguments[0]
			return runSecretAdd(options, promptForVaultPassphrase)
		},
	}
	addSecretAddFlags(command, options)
	return command
}

func newSecretListCommand() *cobra.Command {
	var asJSON bool
	command := &cobra.Command{
		Use:   "list",
		Short: "list the secrets in the vault",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, arguments []string) error {
			return runSecretList(asJSON, promptForVaultPassphrase)
		},
	}
	command.Flags().BoolVar(&asJSON, "json", false,
		"print as JSON")
	return command
}

func newSecretShowCommand() *cobra.Command {
	var asJSON bool
	command := &cobra.Command{
		Use:   "show <name>",
		Short: "show a secret without its value",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, arguments []string) error {
			return runSecretShow(arguments[0], asJSON, promptForVaultPassphrase)
		},
	}
	command.Flags().BoolVar(&asJSON, "json", false,
		"print as JSON")
	return command
}

func newSecretRemoveCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "rm <name>",
		Short: "remove a secret",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, arguments []string) error {
			return runSecretRemove(arguments[0], promptForVaultPassphrase)
		},
	}
}

func newSecretGrantCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "grant <name>",
		Short: "let this project use a secret",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, arguments []string) error {
			return runSecretGrant(arguments[0], promptForVaultPassphrase)
		},
	}
}

func newSecretRevokeCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "revoke <name>",
		Short: "revoke a secret from this project",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, arguments []string) error {
			return runSecretRevoke(arguments[0])
		},
	}
}

func newSecretGrantsCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "grants",
		Short: "show the secrets granted to this project",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, arguments []string) error {
			return runSecretGrants()
		},
	}
}

func newSecretLogCommand() *cobra.Command {
	options := &secretLogOptions{}
	command := &cobra.Command{
		Use:   "log [name]",
		Short: "show recent uses of this project's secrets",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(command *cobra.Command, arguments []string) error {
			if len(arguments) == 1 && options.secretName == "" {
				options.secretName = arguments[0]
			}
			return runSecretLog(options)
		},
	}
	flags := command.Flags()
	flags.BoolVar(&options.allProjects, "all", false,
		"show uses for all projects")
	flags.StringVar(&options.secretName, "secret", "",
		"show uses of one secret")
	flags.IntVar(&options.limit, "limit", secretLogDefaultLimit,
		"number of entries to show")
	return command
}

func newSecretUnlockCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "unlock",
		Short: "unlock the vault in the running broker",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, arguments []string) error {
			return runSecretUnlock(promptForVaultPassphrase)
		},
	}
}

type secretAddOptions struct {
	name             string
	mode             string
	description      string
	fromEnvironment  string
	upstream         string
	pathPrefix       string
	header           string
	headerPrefix     string
	baseURLVariable  string
	tokenVariable    string
	tokenPlaceholder string
	methods          string
	rateLimit        int
	intercept        bool
	algorithm        string
	fingerprint      string
	namespace        string
	variable         string
}

func addSecretAddFlags(command *cobra.Command, options *secretAddOptions) {
	flags := command.Flags()
	flags.StringVar(&options.mode, "mode", "",
		"mode: {route|sign|expose}")
	flags.StringVar(&options.description, "description", "",
		"what the secret is for")
	flags.StringVar(&options.fromEnvironment, "from-environment", "",
		"read the value from this environment variable")
	flags.StringVar(&options.upstream, "upstream", "",
		"for `route`: the host to forward to")
	flags.StringVar(&options.pathPrefix, "path-prefix", "/",
		"for `route`: forward only this path")
	flags.StringVar(&options.header, "header", "Authorization",
		"for `route`: the header for the credential")
	flags.StringVar(&options.headerPrefix, "header-prefix", "",
		"for `route`: the text before the credential, such as \"Bearer \"")
	flags.StringVar(&options.baseURLVariable, "base-url-variable", "",
		"for `route`: the base URL variable set in the box")
	flags.StringVar(&options.tokenVariable, "token-variable", "",
		"for `route`: a dummy credential variable set in the box")
	flags.StringVar(&options.tokenPlaceholder, "token-placeholder", "",
		"for `route`: the value of `--token-variable`")
	flags.StringVar(&options.methods, "methods", "",
		"for `route`: comma-separated methods to allow. Default is any")
	flags.IntVar(&options.rateLimit, "rate-limit", 0,
		"for `route`: requests per minute. Default is none")
	flags.BoolVar(&options.intercept, "intercept", false,
		"for `route`: workaround for in-code hostname pinning")
	flags.StringVar(&options.algorithm, "algorithm", vault.AlgorithmSSHAgent,
		"for `sign`: the signing algorithm")
	flags.StringVar(&options.fingerprint, "fingerprint", "",
		"for `sign`: the fingerprint of the SSH agent key")
	flags.StringVar(&options.namespace, "namespace", "git",
		"for `sign`: the SSH signature namespace")
	flags.StringVar(&options.variable, "variable", "",
		"for `expose`: the variable for the value in the box")
	_ = command.MarkFlagRequired("mode")
}

type secretLogOptions struct {
	allProjects bool
	secretName  string
	limit       int
}

func runSecretInit(prompt passphrasePrompt) error {
	environment, err := loadEnvironment()
	if err != nil {
		return err
	}
	path := environment.Root.VaultFile()
	if vault.Exists(path) {
		return fmt.Errorf("a vault exists at %s", path)
	}
	passphrase, err := prompt("new vault passphrase: ")
	if err != nil {
		return err
	}
	again, err := prompt("vault passphrase again: ")
	if err != nil {
		return err
	}
	if passphrase != again {
		return errors.New("the passphrases do not match")
	}
	if passphrase == "" {
		return errors.New("the passphrase is empty")
	}
	if _, err := vault.Create(path, passphrase); err != nil {
		return err
	}
	fmt.Printf("created %s\n", path)
	return nil
}

func runSecretAdd(options *secretAddOptions, prompt passphrasePrompt) error {
	environment, err := loadEnvironment()
	if err != nil {
		return err
	}
	opened, err := openSecretVault(environment.Root, prompt)
	if err != nil {
		return err
	}
	secret, err := options.record()
	if err != nil {
		return err
	}
	if err := vault.ValidateSecret(secret); err != nil {
		return err
	}
	replaced, err := opened.PutSecret(secret)
	if err != nil {
		return err
	}
	if !replaced {
		fmt.Printf("added %s (%s)\n", secret.Name, describeSecret(secret))
		return nil
	}
	fmt.Printf("replaced %s (%s)\n", secret.Name, describeSecret(secret))
	return nil
}

func (options *secretAddOptions) record() (*vault.Secret, error) {
	secret := &vault.Secret{
		Name:        options.name,
		Description: options.description,
		Mode:        options.mode,
		Created:     time.Now(),
	}
	switch options.mode {
	case vault.ModeRoute:
		secret.Route = options.routeSpec()
	case vault.ModeSign:
		secret.Sign = options.signSpec()
	case vault.ModeExpose:
		secret.Expose = &vault.ExposeSpec{Variable: options.variable}
	default:
		return nil, fmt.Errorf("unknown mode %q. Use route, sign, or expose",
			options.mode)
	}
	if !options.holdsItsOwnValue() {
		return secret, nil
	}
	value, err := options.readValue()
	if err != nil {
		return nil, err
	}
	secret.Value = value
	return secret, nil
}

func (options *secretAddOptions) routeSpec() *vault.RouteSpec {
	var methods []string
	if options.methods != "" {
		methods = strings.Split(options.methods, ",")
	}
	return &vault.RouteSpec{
		Upstream:         options.upstream,
		PathPrefix:       options.pathPrefix,
		Header:           options.header,
		Prefix:           options.headerPrefix,
		BaseURLVariable:  options.baseURLVariable,
		TokenVariable:    options.tokenVariable,
		TokenPlaceholder: options.tokenPlaceholder,
		Methods:          methods,
		RateLimit:        options.rateLimit,
		Intercept:        options.intercept,
	}
}

func (options *secretAddOptions) signSpec() *vault.SignSpec {
	return &vault.SignSpec{
		Algorithm:   options.algorithm,
		Fingerprint: options.fingerprint,
		Namespace:   options.namespace,
		RateLimit:   options.rateLimit,
	}
}

func (options *secretAddOptions) holdsItsOwnValue() bool {
	return !(options.mode == vault.ModeSign &&
		options.algorithm == vault.AlgorithmSSHAgent)
}

func (options *secretAddOptions) readValue() (string, error) {
	value, err := options.readValueFromItsSource()
	if err != nil {
		return "", err
	}
	if value == "" {
		return "", errors.New("no value. Pipe it in or use `--from-environment`")
	}
	return value, nil
}

// readValueFromItsSource reads all of stdin so that a multi-line key stays
// whole.
func (options *secretAddOptions) readValueFromItsSource() (string, error) {
	if options.fromEnvironment != "" {
		value := os.Getenv(options.fromEnvironment)
		if value == "" {
			return "", fmt.Errorf("%s is not set", options.fromEnvironment)
		}
		return value, nil
	}
	if ui.StdinIsTerminal() {
		return ui.ReadSecret("value: ")
	}
	piped, err := io.ReadAll(os.Stdin)
	if err != nil {
		return "", fmt.Errorf(
			"cannot read the value from standard input: %w", err)
	}
	return strings.TrimSuffix(string(piped), "\n"), nil
}

func runSecretList(asJSON bool, prompt passphrasePrompt) error {
	environment, err := loadEnvironment()
	if err != nil {
		return err
	}
	opened, err := openSecretVault(environment.Root, prompt)
	if err != nil {
		return err
	}
	secrets := opened.Secrets()
	if len(secrets) == 0 {
		fmt.Println("no secrets. Run `prison secret add <name>`")
		return nil
	}
	if asJSON {
		return printSecretsAsJSON(secrets)
	}
	counts, err := secretGrantCounts(environment.Root)
	if err != nil {
		return err
	}
	rows := [][]string{{"NAME", "MODE", "DETAIL", "PROJECTS"}}
	for _, secret := range secrets {
		rows = append(rows, []string{
			secret.Name,
			secret.Mode,
			describeSecret(secret),
			strconv.Itoa(counts[secret.Name]),
		})
	}
	return ui.Table(os.Stdout, rows)
}

func runSecretShow(name string, asJSON bool, prompt passphrasePrompt) error {
	environment, err := loadEnvironment()
	if err != nil {
		return err
	}
	opened, err := openSecretVault(environment.Root, prompt)
	if err != nil {
		return err
	}
	secret, found := opened.Secret(name)
	if !found {
		return secretNotFoundError(name)
	}
	if asJSON {
		return printSecretJSON(secretWithDigestedValue(secret))
	}
	granting, err := secretGrantingProjects(environment.Root, name)
	if err != nil {
		return err
	}
	fmt.Println(describeSecretRecord(secret))
	fmt.Println(secretLine("granted to", "%s", describeGrantingProjects(granting)))
	return nil
}

func runSecretRemove(name string, prompt passphrasePrompt) error {
	environment, err := loadEnvironment()
	if err != nil {
		return err
	}
	opened, err := openSecretVault(environment.Root, prompt)
	if err != nil {
		return err
	}
	granting, err := secretGrantingProjects(environment.Root, name)
	if err != nil {
		return err
	}
	removed, err := opened.DeleteSecret(name)
	if err != nil {
		return err
	}
	if !removed {
		return secretNotFoundError(name)
	}
	fmt.Printf("removed %s\n", name)
	switch len(granting) {
	case 0:
	case 1:
		fmt.Println("1 project still has a grant for it")
	default:
		fmt.Printf("%d projects still have a grant for it\n", len(granting))
	}
	return nil
}

func runSecretGrant(name string, prompt passphrasePrompt) error {
	environment, err := loadEnvironment()
	if err != nil {
		return err
	}
	project, err := currentSecretProject(environment)
	if err != nil {
		return err
	}
	opened, err := openSecretVault(environment.Root, prompt)
	if err != nil {
		return err
	}
	secret, found := opened.Secret(name)
	if !found {
		return secretNotFoundError(name)
	}
	granted, err := project.Grants()
	if err != nil {
		return err
	}
	if slices.Contains(granted, name) {
		ui.Progress("%s is granted to this project", name)
	} else {
		if err := project.SetGrants(append(granted, name)); err != nil {
			return err
		}
		ui.Progress("granted %s to %s", name, project.ID)
	}
	fmt.Println(ui.Indent(describeSecretRecord(secret), "  "))
	if secret.Mode == vault.ModeExpose {
		ui.Warn("the box can read this secret as a variable")
	}
	ui.Progress("run `prison up` to apply it")
	return nil
}

func runSecretRevoke(name string) error {
	environment, err := loadEnvironment()
	if err != nil {
		return err
	}
	project, err := currentSecretProject(environment)
	if err != nil {
		return err
	}
	granted, err := project.Grants()
	if err != nil {
		return err
	}
	if !slices.Contains(granted, name) {
		return fmt.Errorf("%s is not granted to this project. "+
			"Run `prison secret grants`", name)
	}
	remaining := slices.DeleteFunc(granted, func(existing string) bool {
		return existing == name
	})
	if err := project.SetGrants(remaining); err != nil {
		return err
	}
	ui.Progress("revoked %s from %s", name, project.ID)
	ui.Warn("running processes keep an exposed value. Restart the session")
	return nil
}

func runSecretGrants() error {
	environment, err := loadEnvironment()
	if err != nil {
		return err
	}
	project, err := currentSecretProject(environment)
	if err != nil {
		return err
	}
	granted, err := project.Grants()
	if err != nil {
		return err
	}
	if len(granted) == 0 {
		fmt.Println("no secret is granted to this project")
		return nil
	}
	fmt.Printf("granted to %s\n", project.ID)
	for _, name := range granted {
		fmt.Printf("  %s\n", name)
	}
	return nil
}

func runSecretLog(options *secretLogOptions) error {
	ctx, cancel := commandContext()
	defer cancel()
	environment, err := loadEnvironment()
	if err != nil {
		return err
	}
	projectID := ""
	if !options.allProjects {
		project, err := currentSecretProject(environment)
		if err != nil {
			return err
		}
		projectID = project.ID
	}
	entries, err := readSecretLogEntries(
		ctx, environment.Root, projectID, options.secretName)
	if err != nil {
		return err
	}
	entries = entriesNamingASecret(entries)
	if len(entries) == 0 {
		fmt.Println("no secret uses")
		return nil
	}
	if options.limit > 0 && len(entries) > options.limit {
		entries = entries[len(entries)-options.limit:]
	}
	rows := [][]string{{"TIME", "WHERE", "SECRET", "WHAT", "STATUS"}}
	for _, entry := range entries {
		rows = append(rows, []string{
			formatSecretLogTime(entry.Time),
			entry.Kind,
			entry.Secret,
			describeSecretLogEntry(entry),
			formatSecretLogStatus(entry.Status),
		})
	}
	return ui.Table(os.Stdout, rows)
}

// runSecretUnlock unlocks the vault in the broker until the broker stops.
func runSecretUnlock(prompt passphrasePrompt) error {
	ctx, cancel := commandContext()
	defer cancel()
	environment, err := loadEnvironment()
	if err != nil {
		return err
	}
	if !vault.Exists(environment.Root.VaultFile()) {
		return missingSecretVaultError(environment.Root.VaultFile())
	}
	client := control.NewClient(environment.Root.BrokerSocket())
	if !client.Alive(ctx) {
		return errors.New("the broker is not running. Run `prison up`")
	}
	passphrase, err := prompt("vault passphrase: ")
	if err != nil {
		return err
	}
	if err := client.Unlock(ctx, passphrase); err != nil {
		return err
	}
	ui.Progress("the vault is unlocked")
	return nil
}

func readSecretLogEntries(ctx context.Context, root *state.Root,
	projectID, secretName string) ([]brokerlog.Entry, error) {
	client := control.NewClient(root.BrokerSocket())
	if client.Alive(ctx) {
		return client.Log(ctx, control.LogQuery{
			Project: projectID,
			Secret:  secretName,
		})
	}
	return brokerlog.Read(root.BrokerLog(), brokerlog.Filter{
		Project: projectID,
		Secret:  secretName,
	})
}

func entriesNamingASecret(entries []brokerlog.Entry) []brokerlog.Entry {
	kept := make([]brokerlog.Entry, 0, len(entries))
	for _, entry := range entries {
		if entry.Secret != "" {
			kept = append(kept, entry)
		}
	}
	return kept
}

func describeSecretLogEntry(entry brokerlog.Entry) string {
	if entry.Path != "" {
		method := entry.Method
		if method == "" {
			method = "?"
		}
		return method + " " + entry.Path
	}
	if entry.Host != "" {
		outcome := entry.Outcome
		if outcome == "" {
			outcome = entry.Kind
		}
		return fmt.Sprintf("%s %s:%d", outcome, entry.Host, entry.Port)
	}
	if entry.Outcome != "" {
		return entry.Outcome
	}
	return entry.Kind
}

func formatSecretLogTime(moment time.Time) string {
	if moment.IsZero() {
		return ""
	}
	return moment.Format(secretLogTimeLayout)
}

func formatSecretLogStatus(status int) string {
	if status == 0 {
		return ""
	}
	return strconv.Itoa(status)
}

func openSecretVault(root *state.Root, prompt passphrasePrompt) (
	*vault.Vault, error) {
	path := root.VaultFile()
	if !vault.Exists(path) {
		return nil, missingSecretVaultError(path)
	}
	passphrase, err := prompt("vault passphrase: ")
	if err != nil {
		return nil, err
	}
	return vault.Open(path, passphrase)
}

// secretPassphraseDescriptor is the only way for a script to open the vault.
var secretPassphraseDescriptor = -1

// secretPassphraseFromDescriptor caches the passphrase because a descriptor
// can be read only once.
var secretPassphraseFromDescriptor struct {
	read  bool
	value string
}

func readPassphraseFromDescriptor(descriptor int) (string, error) {
	if secretPassphraseFromDescriptor.read {
		return secretPassphraseFromDescriptor.value, nil
	}
	file := os.NewFile(uintptr(descriptor), "passphrase")
	if file == nil {
		return "", fmt.Errorf("file descriptor %d is not open", descriptor)
	}
	defer file.Close()
	value, err := io.ReadAll(file)
	if err != nil {
		return "", fmt.Errorf(
			"cannot read the vault passphrase from file descriptor %d: %w",
			descriptor, err)
	}
	secretPassphraseFromDescriptor.read = true
	secretPassphraseFromDescriptor.value =
		strings.TrimRight(string(value), "\r\n")
	return secretPassphraseFromDescriptor.value, nil
}

// promptForVaultPassphrase reads `/dev/tty` when stdin is not a terminal, so
// piped input cannot supply the passphrase.
func promptForVaultPassphrase(prompt string) (string, error) {
	if secretPassphraseDescriptor >= 0 {
		return readPassphraseFromDescriptor(secretPassphraseDescriptor)
	}
	if ui.StdinIsTerminal() {
		return ui.ReadSecret(prompt)
	}
	terminal, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return "", fmt.Errorf("the vault passphrase needs a terminal: %w", err)
	}
	defer terminal.Close()
	fmt.Fprint(terminal, prompt)
	typed, err := term.ReadPassword(int(terminal.Fd()))
	fmt.Fprintln(terminal)
	if err != nil {
		return "", fmt.Errorf("cannot read the passphrase: %w", err)
	}
	return string(typed), nil
}

func missingSecretVaultError(path string) error {
	return fmt.Errorf(
		"there is no vault at %s. Run `prison secret init`", path)
}

func secretNotFoundError(name string) error {
	return fmt.Errorf("no secret called %s. Run `prison secret list`", name)
}

func currentSecretProject(environment *session.Environment) (
	*state.Project, error) {
	directory, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	return environment.Root.Project(directory)
}

func secretGrantCounts(root *state.Root) (map[string]int, error) {
	projects, err := root.ListProjects()
	if err != nil {
		return nil, err
	}
	counts := make(map[string]int)
	for _, project := range projects {
		granted, err := project.Grants()
		if err != nil {
			return nil, err
		}
		for _, name := range granted {
			counts[name]++
		}
	}
	return counts, nil
}

func secretGrantingProjects(root *state.Root, name string) ([]string, error) {
	projects, err := root.ListProjects()
	if err != nil {
		return nil, err
	}
	var granting []string
	for _, project := range projects {
		granted, err := project.Grants()
		if err != nil {
			return nil, err
		}
		if slices.Contains(granted, name) {
			granting = append(granting, project.ID)
		}
	}
	return granting, nil
}

func describeGrantingProjects(granting []string) string {
	if len(granting) == 0 {
		return "no project"
	}
	return strings.Join(granting, ", ")
}

func describeSecret(secret *vault.Secret) string {
	switch {
	case secret.Route != nil:
		return secret.Route.Upstream + secret.Route.PathPrefix
	case secret.Sign != nil:
		if secret.Sign.Algorithm == vault.AlgorithmSSHAgent {
			fingerprint := secret.Sign.Fingerprint
			if fingerprint == "" {
				fingerprint = "any key"
			}
			return "ssh agent, " + fingerprint
		}
		return secret.Sign.Algorithm
	case secret.Expose != nil:
		return secret.Expose.Variable
	}
	return secret.Mode
}

func describeSecretRecord(secret *vault.Secret) string {
	lines := []string{
		secretLine("name", "%s", secret.Name),
		secretLine("mode", "%s", secret.Mode),
	}
	if secret.Description != "" {
		lines = append(lines,
			secretLine("description", "%s", secret.Description))
	}
	lines = append(lines,
		secretLine("value", "%s", vault.Digest(secret.Value)),
		secretLine("created", "%s", formatSecretCreated(secret.Created)))
	switch {
	case secret.Route != nil:
		lines = append(lines, describeRouteLines(secret.Route)...)
	case secret.Sign != nil:
		lines = append(lines, describeSignLines(secret.Sign)...)
	case secret.Expose != nil:
		lines = append(lines,
			secretLine("variable", "%s", secret.Expose.Variable))
	}
	return strings.Join(lines, "\n")
}

func describeRouteLines(route *vault.RouteSpec) []string {
	methods := "any"
	if len(route.Methods) > 0 {
		methods = strings.Join(route.Methods, ", ")
	}
	rateLimit := "none"
	if route.RateLimit > 0 {
		rateLimit = strconv.Itoa(route.RateLimit)
	}
	baseURL := route.BaseURLVariable
	if baseURL == "" {
		baseURL = "-"
	}
	tokenVar := route.TokenVariable
	if tokenVar == "" {
		tokenVar = "-"
	}
	tokenPlaceholder := route.TokenPlaceholder
	if tokenPlaceholder == "" {
		tokenPlaceholder = "-"
	}
	intercept := "no"
	if route.Intercept {
		intercept = "yes, terminates TLS for this host"
	}
	return []string{
		secretLine("upstream", "%s", route.Upstream),
		secretLine("path prefix", "%s", route.PathPrefix),
		secretLine("header", "%s: %s<value>", route.Header, route.Prefix),
		secretLine("base url", "%s", baseURL),
		secretLine("token var", "%s", tokenVar),
		secretLine("token placeholder", "%s", tokenPlaceholder),
		secretLine("methods", "%s", methods),
		secretLine("rate limit", "%s", rateLimit),
		secretLine("intercept", "%s", intercept),
	}
}

func describeSignLines(sign *vault.SignSpec) []string {
	lines := []string{secretLine("algorithm", "%s", sign.Algorithm)}
	if sign.Fingerprint != "" {
		lines = append(lines,
			secretLine("fingerprint", "%s", sign.Fingerprint))
	}
	namespace := sign.Namespace
	if namespace == "" {
		namespace = "-"
	}
	return append(lines, secretLine("namespace", "%s", namespace))
}

func formatSecretCreated(moment time.Time) string {
	if moment.IsZero() {
		return "unknown"
	}
	return moment.Format(time.RFC3339)
}

func secretLine(label, format string, arguments ...any) string {
	return fmt.Sprintf("%-*s %s", secretLabelWidth, label,
		fmt.Sprintf(format, arguments...))
}

func printSecretsAsJSON(secrets []*vault.Secret) error {
	records := make(map[string]*vault.Secret, len(secrets))
	for _, secret := range secrets {
		records[secret.Name] = secretWithDigestedValue(secret)
	}
	return printSecretJSON(records)
}

func secretWithDigestedValue(secret *vault.Secret) *vault.Secret {
	copied := *secret
	copied.Value = vault.Digest(secret.Value)
	return &copied
}

func printSecretJSON(value any) error {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("cannot encode JSON: %w", err)
	}
	fmt.Println(string(encoded))
	return nil
}
