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

// secretLabelWidth is the column width for labels in the record block.
const secretLabelWidth = 12

// secretLogDefaultLimit is the default number of log entries to show.
const secretLogDefaultLimit = 40

// secretLogTimeLayout is the time format for log entries.
const secretLogTimeLayout = "2006-01-02T15:04:05"

// passphrasePrompt reads a vault passphrase. Takes a prompt string.
// Returns the passphrase and an error. Injected so tests can answer
// without a terminal.
type passphrasePrompt func(prompt string) (string, error)

// init registers the secret command group.
func init() {
	registerGroup(addSecretCommands)
}

// addSecretCommands adds `prison secret` and its subcommands to root.
// With no subcommand it lists the vault.
func addSecretCommands(root *cobra.Command) {
	group := &cobra.Command{
		Use:   "secret",
		Short: "manage the secrets Prison holds",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, arguments []string) error {
			return runSecretList(false, promptForVaultPassphrase)
		},
	}
	group.PersistentFlags().IntVar(&secretPassphraseDescriptor,
		"passphrase-fd", -1,
		"Read the vault passphrase from this file descriptor")
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

// newSecretInitCommand builds `prison secret init`. Creates the vault.
// Returns a *cobra.Command.
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

// newSecretAddCommand builds `prison secret add <name>`. Stores a
// secret record. Returns a *cobra.Command.
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

// newSecretListCommand builds `prison secret list`. Prints the vault
// without values. Returns a *cobra.Command.
func newSecretListCommand() *cobra.Command {
	var asJSON bool
	command := &cobra.Command{
		Use:   "list",
		Short: "lists every secret in the vault",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, arguments []string) error {
			return runSecretList(asJSON, promptForVaultPassphrase)
		},
	}
	command.Flags().BoolVar(&asJSON, "json", false,
		"Print as JSON")
	return command
}

// newSecretShowCommand builds `prison secret show <name>`. Prints one
// record and its grants. Returns a *cobra.Command.
func newSecretShowCommand() *cobra.Command {
	var asJSON bool
	command := &cobra.Command{
		Use:   "show <name>",
		Short: "shows a secret without its value",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, arguments []string) error {
			return runSecretShow(arguments[0], asJSON, promptForVaultPassphrase)
		},
	}
	command.Flags().BoolVar(&asJSON, "json", false,
		"Print as JSON")
	return command
}

// newSecretRemoveCommand builds `prison secret rm <name>`. Removes a
// record from the vault. Returns a *cobra.Command.
func newSecretRemoveCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "rm <name>",
		Short: "removes a secret",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, arguments []string) error {
			return runSecretRemove(arguments[0], promptForVaultPassphrase)
		},
	}
}

// newSecretGrantCommand builds `prison secret grant <name>`. Lets the
// current project use a secret. Returns a *cobra.Command.
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

// newSecretRevokeCommand builds `prison secret revoke <name>`. Stops
// the current project from using a secret. Returns a *cobra.Command.
func newSecretRevokeCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "revoke <name>",
		Short: "stop this project using a secret",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, arguments []string) error {
			return runSecretRevoke(arguments[0])
		},
	}
}

// newSecretGrantsCommand builds `prison secret grants`. Lists the
// secrets granted to the current project. Returns a *cobra.Command.
func newSecretGrantsCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "grants",
		Short: "shows the secrets granted to this project",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, arguments []string) error {
			return runSecretGrants()
		},
	}
}

// newSecretLogCommand builds `prison secret log`. Prints recent uses
// of secrets. The name can be an argument or a --secret flag. Returns
// a *cobra.Command.
func newSecretLogCommand() *cobra.Command {
	options := &secretLogOptions{}
	command := &cobra.Command{
		Use:   "log [name]",
		Short: "shows recent uses of this project's secrets",
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
		"Lists every project's uses")
	flags.StringVar(&options.secretName, "secret", "",
		"Lists a secret's uses")
	flags.IntVar(&options.limit, "limit", secretLogDefaultLimit,
		"The number of entries to show")
	return command
}

// newSecretUnlockCommand builds `prison secret unlock`. Sends the
// passphrase to a running broker. Returns a *cobra.Command.
func newSecretUnlockCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "unlock",
		Short: "sends the passphrase to a running broker",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, arguments []string) error {
			return runSecretUnlock(promptForVaultPassphrase)
		},
	}
}

// secretAddOptions holds every flag for the `add` command. Each mode
// uses only its own fields.
type secretAddOptions struct {
	name             string
	mode             string
	description      string
	confirm          bool
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

// addSecretAddFlags adds all flags for the `add` command. Makes --mode
// required.
func addSecretAddFlags(command *cobra.Command, options *secretAddOptions) {
	flags := command.Flags()
	flags.StringVar(&options.mode, "mode", "",
		"Options: {route|sign|expose}")
	flags.StringVar(&options.description, "description", "",
		"What it is for")
	flags.BoolVar(&options.confirm, "confirm", false,
		"Ask on the host every time this secret is going to be used")
	flags.StringVar(&options.fromEnvironment, "from-environment", "",
		"Read the value from this environment variable")
	flags.StringVar(&options.upstream, "upstream", "",
		"For `route`: The host to forward to")
	flags.StringVar(&options.pathPrefix, "path-prefix", "/",
		"For `route`: Nothing outside this path will be forwarded")
	flags.StringVar(&options.header, "header", "Authorization",
		"For `route`: The header the credential is sent in")
	flags.StringVar(&options.headerPrefix, "header-prefix", "",
		"For `route`: What goes before the credential value, e.g., \"Bearer \"")
	flags.StringVar(&options.baseURLVariable, "base-url-variable", "",
		"For `route`: The variable the box is pointed at Prison with")
	flags.StringVar(&options.tokenVariable, "token-variable", "",
		"For `route`: A dummy credential variable set in the box")
	flags.StringVar(&options.tokenPlaceholder, "token-placeholder", "",
		"For `route`: The value that fills `--token-variable`")
	flags.StringVar(&options.methods, "methods", "",
		"For `route`: Comma-separated methods to allow, defaults to any")
	flags.IntVar(&options.rateLimit, "rate-limit", 0,
		"For `route`: Requests a minute, default none")
	flags.BoolVar(&options.intercept, "intercept", false,
		"For `route`: Workaround for in-code hostname pinning")
	flags.StringVar(&options.algorithm, "algorithm", vault.AlgorithmSSHAgent,
		"For `sign`: Who holds the key and how the signature is made")
	flags.StringVar(&options.fingerprint, "fingerprint", "",
		"For `sign`: Which key in your SSH agent may be used")
	flags.StringVar(&options.namespace, "namespace", "git",
		"For `sign`: The SSH signature namespace, which Git wants as \"git\"")
	flags.StringVar(&options.variable, "variable", "",
		"For `expose`: The variable the box reads the value as")
	_ = command.MarkFlagRequired("mode")
}

// secretLogOptions holds the flags for the `log` command.
type secretLogOptions struct {
	allProjects bool
	secretName  string
	limit       int
}

// runSecretInit creates the vault. Asks for the passphrase twice. Fails
// if a vault already exists, the passphrases differ, or the passphrase
// is empty.
func runSecretInit(prompt passphrasePrompt) error {
	environment, err := loadEnvironment()
	if err != nil {
		return err
	}
	path := environment.Root.VaultFile()
	if vault.Exists(path) {
		return fmt.Errorf("there is already a vault at %s; `prison secret "+
			"list` shows what it holds", path)
	}
	passphrase, err := prompt("New vault passphrase: ")
	if err != nil {
		return err
	}
	again, err := prompt("Enter the passphrase again: ")
	if err != nil {
		return err
	}
	if passphrase != again {
		return errors.New("the two passphrases do not match")
	}
	if passphrase == "" {
		return errors.New("the passphrase is empty")
	}
	if _, err := vault.Create(path, passphrase); err != nil {
		return err
	}
	fmt.Printf("Created %s\n", path)
	fmt.Println("Prison asks for this passphrase once per broker, at " +
		"`prison up`")
	return nil
}

// runSecretAdd opens the vault, builds a record from the flags, and
// stores it. Replaces an existing record of the same name.
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
		fmt.Printf("Added %s (%s)\n", secret.Name, describeSecret(secret))
		return nil
	}
	fmt.Printf("Replaced %s (%s)\n", secret.Name, describeSecret(secret))
	fmt.Println("Every project already granted it uses the new value on the " +
		"next request")
	fmt.Println("`add` writes a whole record")
	return nil
}

// record builds a vault record from the flag values. Reads the secret
// value unless the mode does not need one. Does not validate the
// result.
func (options *secretAddOptions) record() (*vault.Secret, error) {
	secret := &vault.Secret{
		Name:        options.name,
		Description: options.description,
		Mode:        options.mode,
		Confirm:     options.confirm,
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
		return nil, fmt.Errorf(
			"%q is not a secret mode; use route, sign, or expose",
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

// routeSpec returns the route settings from the flags. Splits the
// method list on commas.
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

// signSpec returns the signing settings from the flags.
func (options *secretAddOptions) signSpec() *vault.SignSpec {
	return &vault.SignSpec{
		Algorithm:   options.algorithm,
		Fingerprint: options.fingerprint,
		Namespace:   options.namespace,
		RateLimit:   options.rateLimit,
	}
}

// holdsItsOwnValue reports whether this mode needs a stored value.
// Agent-backed signatures do not.
func (options *secretAddOptions) holdsItsOwnValue() bool {
	return !(options.mode == vault.ModeSign &&
		options.algorithm == vault.AlgorithmSSHAgent)
}

// readValue returns the value to store. Fails if the value is empty.
func (options *secretAddOptions) readValue() (string, error) {
	value, err := options.readValueFromItsSource()
	if err != nil {
		return "", err
	}
	if value == "" {
		return "", errors.New(
			"no value given; pipe it in or use `--from-environment`")
	}
	return value, nil
}

// readValueFromItsSource reads the value from --from-environment, from
// piped stdin, or from a terminal prompt. Reads all of stdin so a
// piped key arrives whole. Strips one trailing newline.
func (options *secretAddOptions) readValueFromItsSource() (string, error) {
	if options.fromEnvironment != "" {
		value := os.Getenv(options.fromEnvironment)
		if value == "" {
			return "", fmt.Errorf("%s is not set in this shell, so there is "+
				"nothing to store", options.fromEnvironment)
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

// runSecretList prints the vault as a table or as JSON. Values are
// replaced by their digests in JSON output.
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
		fmt.Println("no secrets yet; add one with `prison secret add <name>`")
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

// runSecretShow prints one secret record and its grants. Can output as
// a labelled block or JSON.
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

// runSecretRemove removes a secret from the vault. Reports how many
// projects still grant it.
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
		fmt.Println("one project still names it in its grants, which now " +
			"resolves to nothing")
	default:
		fmt.Printf("%d projects still name it in their grants, which now "+
			"resolves to nothing\n", len(granting))
	}
	return nil
}

// runSecretGrant grants a secret to the current project. Prints what
// the grant covers.
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
		ui.Progress("%s was already granted to this project", name)
	} else {
		if err := project.SetGrants(append(granted, name)); err != nil {
			return err
		}
		ui.Progress("granted %s to %s", name, project.ID)
	}
	fmt.Println(ui.Indent(describeSecretRecord(secret), "  "))
	if secret.Mode == vault.ModeExpose {
		ui.Warn("an exposed secret is handed to the box as a variable")
	}
	ui.Progress("run `prison up` to hand it to the box")
	return nil
}

// runSecretRevoke revokes a secret from the current project. Warns
// that values already handed out remain in running processes.
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
		return fmt.Errorf("%s is not granted to this project; `prison secret "+
			"grants` lists what is", name)
	}
	remaining := slices.DeleteFunc(granted, func(existing string) bool {
		return existing == name
	})
	if err := project.SetGrants(remaining); err != nil {
		return err
	}
	ui.Progress("revoked %s from %s", name, project.ID)
	ui.Progress("routes and signatures stop answering on the next request")
	ui.Warn("a value already handed to a running process stays there; " +
		"restart the session have it gone")
	return nil
}

// runSecretGrants prints the secret names granted to the current
// project. Needs no passphrase.
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
		fmt.Println("`prison secret list` shows what the vault holds")
		return nil
	}
	fmt.Printf("granted to %s\n", project.ID)
	for _, name := range granted {
		fmt.Printf("  %s\n", name)
	}
	return nil
}

// runSecretLog prints recent secret usage. Filters to one project
// unless --all is given. Omits entries that named no secret.
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
		fmt.Println("no secret has been used yet")
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

// runSecretUnlock sends the passphrase to a running broker. The broker
// holds the vault open until it stops.
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
		return errors.New("no broker is running, so there is nothing to " +
			"unlock; `prison up` starts one and asks for the passphrase")
	}
	passphrase, err := prompt("vault passphrase: ")
	if err != nil {
		return err
	}
	if err := client.Unlock(ctx, passphrase); err != nil {
		return err
	}
	ui.Progress("the vault is open")
	return nil
}

// readSecretLogEntries returns log entries for one project, or all
// projects if projectID is empty. Reads from the broker if running,
// from the log file otherwise.
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

// entriesNamingASecret filters entries to those that name a secret.
// Keeps the original order.
func entriesNamingASecret(entries []brokerlog.Entry) []brokerlog.Entry {
	kept := make([]brokerlog.Entry, 0, len(entries))
	for _, entry := range entries {
		if entry.Secret != "" {
			kept = append(kept, entry)
		}
	}
	return kept
}

// describeSecretLogEntry returns a short description of what a log
// entry did. Shows the request path, host, outcome, or entry kind.
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

// formatSecretLogTime formats an entry's time. Returns empty string if
// zero.
func formatSecretLogTime(moment time.Time) string {
	if moment.IsZero() {
		return ""
	}
	return moment.Format(secretLogTimeLayout)
}

// formatSecretLogStatus formats a response status. Returns empty string
// if zero.
func formatSecretLogStatus(status int) string {
	if status == 0 {
		return ""
	}
	return strconv.Itoa(status)
}

// openSecretVault asks for the passphrase and opens the vault. Takes
// the state root and a prompt function. Returns the opened vault or an
// error.
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

// secretPassphraseDescriptor is the file descriptor from
// --passphrase-fd. Defaults to -1 when the flag is not used. This is
// the only way to use the vault from a script.
var secretPassphraseDescriptor = -1

// secretPassphraseFromDescriptor caches the passphrase read from the
// descriptor. A descriptor drains once, so repeated reads return the
// cached value.
var secretPassphraseFromDescriptor struct {
	read  bool
	value string
}

// readPassphraseFromDescriptor reads the passphrase from a file
// descriptor. Takes the descriptor number. The first call drains and
// closes it. Later calls return the cached value. Strips trailing
// newlines.
func readPassphraseFromDescriptor(descriptor int) (string, error) {
	if secretPassphraseFromDescriptor.read {
		return secretPassphraseFromDescriptor.value, nil
	}
	file := os.NewFile(uintptr(descriptor), "passphrase")
	if file == nil {
		return "", fmt.Errorf(
			"file descriptor %d is not open, so no passphrase could be "+
				"read from it", descriptor)
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

// promptForVaultPassphrase reads a passphrase without echoing it. Uses
// stdin if it is a terminal, otherwise opens /dev/tty so piped input
// does not replace the prompt.
func promptForVaultPassphrase(prompt string) (string, error) {
	if secretPassphraseDescriptor >= 0 {
		return readPassphraseFromDescriptor(secretPassphraseDescriptor)
	}
	if ui.StdinIsTerminal() {
		return ui.ReadSecret(prompt)
	}
	terminal, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return "", fmt.Errorf("the vault passphrase has to be typed at a "+
			"terminal, and this command has none: %w", err)
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

// missingSecretVaultError returns the error for a missing vault.
func missingSecretVaultError(path string) error {
	return fmt.Errorf(
		"there is no vault at %s yet; `prison secret init` creates one", path)
}

// secretNotFoundError returns the error for a secret name not in the
// vault.
func secretNotFoundError(name string) error {
	return fmt.Errorf("no secret called %s; `prison secret list` shows what "+
		"the vault holds", name)
}

// currentSecretProject returns the state project for the working
// directory. Grants are recorded against this project.
func currentSecretProject(environment *session.Environment) (
	*state.Project, error) {
	directory, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	return environment.Root.Project(directory)
}

// secretGrantCounts returns a map of secret name to the number of
// projects that grant it.
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

// secretGrantingProjects returns the project ids that grant the named
// secret.
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

// describeGrantingProjects joins the project ids into one string.
// Returns "no project" if the list is empty.
func describeGrantingProjects(granting []string) string {
	if len(granting) == 0 {
		return "no project"
	}
	return strings.Join(granting, ", ")
}

// describeSecret returns a one-line summary of a secret for list
// display.
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

// describeSecretRecord returns a multi-line labelled block for one
// secret. Includes everything except the value and the grant line.
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
		secretLine("created", "%s", formatSecretCreated(secret.Created)),
		secretLine("confirm", "%s", describeConfirmation(secret.Confirm)))
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

// describeRouteLines returns the labelled lines for a route's settings.
// Shows defaults where values are empty.
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
		intercept = "yes, by terminating TLS for this host"
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

// describeSignLines returns the labelled lines for a signing secret.
// Omits the fingerprint if not set.
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

// describeConfirmation returns "yes, every use asks" or "no".
func describeConfirmation(confirm bool) string {
	if confirm {
		return "yes, every use asks"
	}
	return "no"
}

// formatSecretCreated formats the creation time. Returns "unknown" if
// zero.
func formatSecretCreated(moment time.Time) string {
	if moment.IsZero() {
		return "unknown"
	}
	return moment.Format(time.RFC3339)
}

// secretLine formats one labelled line, aligning the value at
// secretLabelWidth.
func secretLine(label, format string, arguments ...any) string {
	return fmt.Sprintf("%-*s %s", secretLabelWidth, label,
		fmt.Sprintf(format, arguments...))
}

// printSecretsAsJSON prints all secrets as JSON. Values are replaced
// by digests.
func printSecretsAsJSON(secrets []*vault.Secret) error {
	records := make(map[string]*vault.Secret, len(secrets))
	for _, secret := range secrets {
		records[secret.Name] = secretWithDigestedValue(secret)
	}
	return printSecretJSON(records)
}

// secretWithDigestedValue returns a copy of the secret with its value
// replaced by the value's digest.
func secretWithDigestedValue(secret *vault.Secret) *vault.Secret {
	copied := *secret
	copied.Value = vault.Digest(secret.Value)
	return &copied
}

// printSecretJSON writes a value as indented JSON to stdout.
func printSecretJSON(value any) error {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("cannot render the records as JSON: %w", err)
	}
	fmt.Println(string(encoded))
	return nil
}
