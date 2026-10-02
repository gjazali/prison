// Package plugin reads inmate manifests, tracks approval status, and
// runs declarative host operations.
package plugin

import (
	"fmt"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"

	"prison/internal/config"
	"prison/internal/policy"
)

const ManifestFileName = "inmate.toml"

// PersistedRoot is the only box directory that is kept when the box is
// destroyed.
const PersistedRoot = "/home/dev"

const DefaultDockerfile = "Dockerfile"

type Manifest struct {
	Inmate      InmateTable    `toml:"inmate"`
	Image       ImageTable     `toml:"image"`
	Command     CommandTable   `toml:"command"`
	Auth        *AuthTable     `toml:"auth"`
	Persist     PersistTable   `toml:"persist"`
	Environment map[string]any `toml:"environment"`
	Egress      EgressTable    `toml:"egress"`
	Hooks       HooksTable     `toml:"hooks"`
	Host        HostTable      `toml:"host"`
}

type InmateTable struct {
	Name        string   `toml:"name"`
	Description string   `toml:"description"`
	Aliases     []string `toml:"aliases"`
}

type ImageTable struct {
	Foundation string `toml:"foundation"`
	Dockerfile string `toml:"dockerfile"`
}

type CommandTable struct {
	Run    string `toml:"run"`
	Unsafe string `toml:"unsafe"`
}

// AuthTable describes the upstream that the broker proxies for the inmate.
type AuthTable struct {
	Upstream        string       `toml:"upstream"`
	PathPrefix      string       `toml:"path_prefix"`
	BaseURLVariable string       `toml:"base_url_variable"`
	TokenVariable   string       `toml:"token_variable"`
	Credentials     []Credential `toml:"credentials"`
}

// Credential is one host variable that can hold the secret. Prison uses the
// first credential that has a value.
type Credential struct {
	Variable string `toml:"variable"`
	Header   string `toml:"header"`
	Prefix   string `toml:"prefix"`
}

type PersistTable struct {
	Paths []string `toml:"paths"`
}

type EgressTable struct {
	Hosts []string `toml:"hosts"`
}

type HooksTable struct {
	BoxCommand string `toml:"box_command"`
	HostConfig string `toml:"host_config"`
}

// HostTable lists the operations that prison runs on the host before a
// session. Root is relative to the home directory.
type HostTable struct {
	Root        string                  `toml:"root"`
	Copy        []string                `toml:"copy"`
	JSON        []HostJSONOperation     `toml:"json"`
	Environment []HostEnvironmentSource `toml:"environment"`
}

type HostJSONOperation struct {
	From   string            `toml:"from"`
	To     string            `toml:"to"`
	Keep   []string          `toml:"keep"`
	Append map[string]string `toml:"append"`
	Set    map[string]string `toml:"set"`
}

// HostEnvironmentSource copies one value from a host JSON file into the
// box environment. The value must match Pattern in full.
type HostEnvironmentSource struct {
	Name    string `toml:"name"`
	From    string `toml:"from"`
	Path    string `toml:"path"`
	Pattern string `toml:"pattern"`
}

var acceptedKeys = map[string][]string{
	"": {"inmate", "image", "command", "auth", "persist", "environment",
		"egress", "hooks", "host"},
	"inmate":           {"name", "description", "aliases"},
	"image":            {"foundation", "dockerfile"},
	"command":          {"run", "unsafe"},
	"auth":             {"upstream", "path_prefix", "base_url_variable", "token_variable", "credentials"},
	"auth.credentials": {"variable", "header", "prefix"},
	"persist":          {"paths"},
	"egress":           {"hosts"},
	"hooks":            {"box_command"},
	"host":             {"root", "copy", "json", "environment"},
	"host.json":        {"from", "to", "keep", "append", "set"},
	"host.environment": {"name", "from", "path", "pattern"},
}

var arrayTables = map[string]bool{
	"auth.credentials": true,
	"host.json":        true,
	"host.environment": true,
}

func ParseManifest(data []byte) (*Manifest, error) {
	manifest := &Manifest{}
	metadata, err := toml.Decode(string(data), manifest)
	if err != nil {
		return nil, fmt.Errorf("the manifest is not valid TOML: %w", err)
	}
	if err := refuseUnknownKeys(metadata.Undecoded()); err != nil {
		return nil, err
	}
	manifest.applyDefaults()
	if err := manifest.Validate(); err != nil {
		return nil, err
	}
	return manifest, nil
}

func refuseUnknownKeys(undecoded []toml.Key) error {
	if len(undecoded) == 0 {
		return nil
	}
	dotted := make([]string, 0, len(undecoded))
	for _, key := range undecoded {
		dotted = append(dotted, key.String())
	}
	sort.Strings(dotted)
	first := dotted[0]
	isTable := false
	for _, other := range dotted[1:] {
		if strings.HasPrefix(other, first+".") {
			isTable = true
			break
		}
	}
	parts := strings.Split(first, ".")
	for depth := len(parts) - 1; depth >= 0; depth-- {
		prefix := strings.Join(parts[:depth], ".")
		accepted, known := acceptedKeys[prefix]
		if !known {
			continue
		}
		listed := strings.Join(accepted, ", ")
		if isTable || depth < len(parts)-1 {
			return fmt.Errorf("unknown table %s in %s. Use one of %s",
				tableName(strings.Join(parts[:depth+1], ".")),
				tableName(prefix), listed)
		}
		return fmt.Errorf("unknown key %q in %s. Use one of %s",
			parts[depth], tableName(prefix), listed)
	}
	return fmt.Errorf("unknown key %q in the manifest", first)
}

func tableName(prefix string) string {
	if prefix == "" {
		return "the manifest"
	}
	if arrayTables[prefix] {
		return "[[" + prefix + "]]"
	}
	return "[" + prefix + "]"
}

func (m *Manifest) applyDefaults() {
	if m.Inmate.Description == "" {
		m.Inmate.Description = m.Inmate.Name
	}
	if m.Image.Dockerfile == "" {
		m.Image.Dockerfile = DefaultDockerfile
	}
	if m.Auth != nil && m.Auth.PathPrefix == "" {
		m.Auth.PathPrefix = "/"
	}
	if m.Host.Root == "" && len(m.Persist.Paths) > 0 {
		first := m.Persist.Paths[0]
		if strings.HasPrefix(first, PersistedRoot) {
			m.Host.Root = "~" + strings.TrimPrefix(first, PersistedRoot)
		}
	}
}

// Validate expects `applyDefaults` to run first.
func (m *Manifest) Validate() error {
	if err := config.ValidateName("inmate", m.Inmate.Name); err != nil {
		return err
	}
	if err := requireLine(m.Inmate.Description, "inmate.description"); err != nil {
		return err
	}
	if err := m.validateAliases(); err != nil {
		return err
	}
	if err := m.validateImage(); err != nil {
		return err
	}
	if err := m.validateCommand(); err != nil {
		return err
	}
	if err := m.validateAuth(); err != nil {
		return err
	}
	if err := m.validatePersist(); err != nil {
		return err
	}
	if err := m.validateEnvironment(); err != nil {
		return err
	}
	if err := m.validateEgress(); err != nil {
		return err
	}
	if err := m.validateHooks(); err != nil {
		return err
	}
	return m.validateHost()
}

func (m *Manifest) validateAliases() error {
	taken := map[string]bool{m.Inmate.Name: true}
	for _, alias := range m.Inmate.Aliases {
		if err := config.ValidateName("inmate alias", alias); err != nil {
			return err
		}
		if reservedNames[alias] {
			return fmt.Errorf("the alias %q is taken by a prison command", alias)
		}
		if taken[alias] {
			return fmt.Errorf("the alias %q is a duplicate", alias)
		}
		taken[alias] = true
	}
	return nil
}

func (m *Manifest) validateImage() error {
	if m.Image.Foundation != "" {
		if err := requireLine(m.Image.Foundation, "image.foundation"); err != nil {
			return err
		}
	}
	return requireInsideRelative(m.Image.Dockerfile, "image.dockerfile")
}

func (m *Manifest) validateCommand() error {
	if strings.TrimSpace(m.Command.Run) == "" {
		return fmt.Errorf("command.run is required")
	}
	if err := requireLine(m.Command.Run, "command.run"); err != nil {
		return err
	}
	if m.Command.Unsafe == "" {
		return nil
	}
	return requireLine(m.Command.Unsafe, "command.unsafe")
}

func (m *Manifest) validateAuth() error {
	if m.Auth == nil {
		return nil
	}
	if err := requireLine(m.Auth.Upstream, "auth.upstream"); err != nil {
		return err
	}
	if !strings.HasPrefix(m.Auth.PathPrefix, "/") {
		return fmt.Errorf("auth.path_prefix %q must start with `/`", m.Auth.PathPrefix)
	}
	if err := requireLine(m.Auth.PathPrefix, "auth.path_prefix"); err != nil {
		return err
	}
	for _, named := range []struct {
		value, key string
	}{
		{m.Auth.BaseURLVariable, "auth.base_url_variable"},
		{m.Auth.TokenVariable, "auth.token_variable"},
	} {
		if err := requireLine(named.value, named.key); err != nil {
			return err
		}
		if err := config.ValidateEnvironmentName(named.value); err != nil {
			return fmt.Errorf("%s: %w", named.key, err)
		}
	}
	if len(m.Auth.Credentials) == 0 {
		return fmt.Errorf("[[auth.credentials]] must have at least one entry")
	}
	for _, credential := range m.Auth.Credentials {
		if err := requireLine(credential.Variable, "auth.credentials.variable"); err != nil {
			return err
		}
		if err := config.ValidateEnvironmentName(credential.Variable); err != nil {
			return fmt.Errorf("auth.credentials.variable: %w", err)
		}
		if err := requireLine(credential.Header, "auth.credentials.header"); err != nil {
			return err
		}
		if strings.ContainsAny(credential.Prefix, "\r\n") {
			return fmt.Errorf("auth.credentials.prefix must be a single line")
		}
	}
	return nil
}

func (m *Manifest) validatePersist() error {
	for _, guestPath := range m.Persist.Paths {
		if err := requireLine(guestPath, "persist.paths"); err != nil {
			return err
		}
		if !strings.HasPrefix(guestPath, PersistedRoot+"/") {
			return fmt.Errorf("persist path %q must be under %s/",
				guestPath, PersistedRoot)
		}
		if hasClimbingSegment(guestPath) {
			return fmt.Errorf("persist path %q must not contain `..`", guestPath)
		}
	}
	return nil
}

func (m *Manifest) validateEnvironment() error {
	for _, name := range sortedKeys(m.Environment) {
		if err := config.ValidateBoxEnvironmentName(name); err != nil {
			return fmt.Errorf("[environment]: %w", err)
		}
		if _, err := environmentValueText(m.Environment[name]); err != nil {
			return fmt.Errorf("environment.%s %w", name, err)
		}
	}
	return nil
}

func (m *Manifest) validateEgress() error {
	for _, host := range m.Egress.Hosts {
		if _, err := policy.ParsePattern(host); err != nil {
			return fmt.Errorf("egress.hosts: %w", err)
		}
	}
	return nil
}

func (m *Manifest) validateHooks() error {
	if m.Hooks.HostConfig != "" {
		return fmt.Errorf("hooks.host_config is not supported. Use a [host] table")
	}
	if m.Hooks.BoxCommand == "" {
		return nil
	}
	return requireLine(m.Hooks.BoxCommand, "hooks.box_command")
}

func (m *Manifest) validateHost() error {
	host := m.Host
	if host.Root != "" {
		if err := requireHomeRelative(host.Root, "host.root"); err != nil {
			return err
		}
	}
	needsRoot := len(host.Copy) > 0
	for _, operation := range host.JSON {
		if operation.From != "" {
			needsRoot = true
		}
	}
	if needsRoot && host.Root == "" {
		return fmt.Errorf("host.root is required because [persist] has no paths")
	}
	for _, entry := range host.Copy {
		if err := requireInsideRelative(strings.TrimSuffix(entry, "/"), "host.copy"); err != nil {
			return err
		}
	}
	for _, operation := range host.JSON {
		if err := validateJSONOperation(operation); err != nil {
			return err
		}
	}
	for _, source := range host.Environment {
		if err := validateEnvironmentSource(source); err != nil {
			return err
		}
	}
	return nil
}

func validateJSONOperation(operation HostJSONOperation) error {
	if operation.From != "" {
		if err := requireInsideRelative(operation.From, "host.json.from"); err != nil {
			return err
		}
	}
	if err := requireInsideRelative(operation.To, "host.json.to"); err != nil {
		return err
	}
	if operation.From == "" && len(operation.Keep) == 0 &&
		len(operation.Set) == 0 && len(operation.Append) == 0 {
		return fmt.Errorf("the [[host.json]] entry for %q is empty. "+
			"Add `from`, `keep`, `set`, or `append`", operation.To)
	}
	for _, key := range operation.Keep {
		if err := requireLine(key, "host.json.keep"); err != nil {
			return err
		}
	}
	for _, named := range []struct {
		values map[string]string
		key    string
	}{
		{operation.Set, "host.json.set"},
		{operation.Append, "host.json.append"},
	} {
		for _, dotted := range sortedKeys(named.values) {
			if err := requireDottedPath(dotted, named.key); err != nil {
				return err
			}
			if err := requireLine(named.values[dotted], named.key); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateEnvironmentSource(source HostEnvironmentSource) error {
	if err := config.ValidateBoxEnvironmentName(source.Name); err != nil {
		return fmt.Errorf("host.environment.name: %w", err)
	}
	if err := requireHomeRelative(source.From, "host.environment.from"); err != nil {
		return err
	}
	if err := requireDottedPath(source.Path, "host.environment.path"); err != nil {
		return err
	}
	if source.Pattern == "" {
		return fmt.Errorf("host.environment.pattern is required")
	}
	if _, err := regexp.Compile(source.Pattern); err != nil {
		return fmt.Errorf("host.environment.pattern %q is not a regular expression: %w", source.Pattern, err)
	}
	return nil
}

func (m *Manifest) EnvironmentAssignments() []string {
	assignments := make([]string, 0, len(m.Environment))
	for _, name := range sortedKeys(m.Environment) {
		text, err := environmentValueText(m.Environment[name])
		if err != nil {
			continue
		}
		assignments = append(assignments, name+"="+text)
	}
	return assignments
}

func environmentValueText(value any) (string, error) {
	switch typed := value.(type) {
	case string:
		if strings.ContainsAny(typed, "\r\n") {
			return "", fmt.Errorf("must be a single line")
		}
		return typed, nil
	case bool:
		return strconv.FormatBool(typed), nil
	case int64:
		return strconv.FormatInt(typed, 10), nil
	case float64:
		return strconv.FormatFloat(typed, 'g', -1, 64), nil
	default:
		return "", fmt.Errorf("must be a string, number, or boolean")
	}
}

func requireLine(value, key string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s must be a non-empty string", key)
	}
	if strings.ContainsAny(value, "\r\n\t") {
		return fmt.Errorf("%s must be a single line without tabs", key)
	}
	return nil
}

func requireInsideRelative(value, key string) error {
	if err := requireLine(value, key); err != nil {
		return err
	}
	if strings.HasPrefix(value, "/") {
		return fmt.Errorf("%s %q must be a relative path", key, value)
	}
	if hasClimbingSegment(value) {
		return fmt.Errorf("%s %q must not contain `..`", key, value)
	}
	return nil
}

func requireHomeRelative(value, key string) error {
	if err := requireLine(value, key); err != nil {
		return err
	}
	return requireInsideRelative(homeRelativePath(value), key)
}

func homeRelativePath(value string) string {
	trimmed := strings.TrimPrefix(value, "~")
	trimmed = strings.TrimPrefix(trimmed, "/")
	if trimmed == "" {
		return "."
	}
	return trimmed
}

func requireDottedPath(value, key string) error {
	if err := requireLine(value, key); err != nil {
		return err
	}
	for _, segment := range strings.Split(value, ".") {
		if segment == "" {
			return fmt.Errorf("%s %q has an empty segment", key, value)
		}
	}
	return nil
}

func hasClimbingSegment(value string) bool {
	for _, segment := range strings.Split(path.Clean(value), "/") {
		if segment == ".." {
			return true
		}
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == ".." {
			return true
		}
	}
	return false
}

func sortedKeys[Value any](values map[string]Value) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
