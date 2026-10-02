package vault

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Secret names are DNS labels under `prison.internal`, so the alphabet
// is narrow.
var (
	secretNamePattern          = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	environmentVariablePattern = regexp.MustCompile(
		`^[A-Za-z_][A-Za-z0-9_]*$`)
	fingerprintPattern = regexp.MustCompile(`^SHA256:[A-Za-z0-9+/]{43}=?$`)
	httpMethodPattern  = regexp.MustCompile(`^[A-Z]+$`)
)

const digestPreviewLength = 12

const defaultUpstreamPort = 443

// ValidateSecret also normalizes the fields that have a canonical form.
func ValidateSecret(secret *Secret) error {
	if secret == nil {
		return errors.New("no secret record given")
	}
	if !secretNamePattern.MatchString(secret.Name) {
		return fmt.Errorf(
			"%q is not a valid secret name. Use up to 32 lowercase letters, "+
				"digits, and hyphens, starting with a letter", secret.Name)
	}
	if secret.Name == ReservedSecretName {
		return fmt.Errorf(
			"%q is reserved for the broker", secret.Name)
	}
	if err := validateModeSpecs(secret); err != nil {
		return err
	}
	switch secret.Mode {
	case ModeRoute:
		return validateRoute(secret)
	case ModeSign:
		return validateSign(secret)
	default:
		return validateExpose(secret)
	}
}

func validateModeSpecs(secret *Secret) error {
	switch secret.Mode {
	case ModeRoute, ModeSign, ModeExpose:
	default:
		return fmt.Errorf(
			"%q is not a secret mode. Use `route`, `sign`, or `expose`",
			secret.Mode)
	}
	present := map[string]bool{
		ModeRoute:  secret.Route != nil,
		ModeSign:   secret.Sign != nil,
		ModeExpose: secret.Expose != nil,
	}
	if !present[secret.Mode] {
		return fmt.Errorf(
			"a %s secret needs its %s configuration", secret.Mode, secret.Mode)
	}
	for mode, set := range present {
		if set && mode != secret.Mode {
			return fmt.Errorf(
				"a %s secret cannot have %s configuration", secret.Mode, mode)
		}
	}
	return nil
}

func validateRoute(secret *Secret) error {
	route := secret.Route
	if route.Upstream == "" {
		return errors.New("a route secret needs an upstream")
	}
	host, port, err := parseUpstream(route.Upstream)
	if err != nil {
		return err
	}
	route.Upstream = formatUpstream(host, port)
	if !strings.HasPrefix(route.PathPrefix, "/") {
		return fmt.Errorf(
			"the path prefix %q must start with \"/\"", route.PathPrefix)
	}
	if route.Header == "" || strings.ContainsAny(route.Header, " \t:\r\n") {
		return fmt.Errorf(
			"%q is not a valid header name", route.Header)
	}
	if route.BaseURLVariable != "" &&
		!environmentVariablePattern.MatchString(route.BaseURLVariable) {
		return fmt.Errorf(
			"%q is not an environment variable name", route.BaseURLVariable)
	}
	if route.TokenVariable != "" &&
		!environmentVariablePattern.MatchString(route.TokenVariable) {
		return fmt.Errorf(
			"%q is not an environment variable name", route.TokenVariable)
	}
	if route.TokenVariable != "" && route.TokenPlaceholder == "" {
		return errors.New("a token variable needs a token placeholder")
	}
	if route.TokenPlaceholder != "" && route.TokenVariable == "" {
		return errors.New("a token placeholder needs a token variable")
	}
	if route.TokenPlaceholder != "" &&
		strings.ContainsAny(route.TokenPlaceholder, "\r\n") {
		return errors.New("a token placeholder cannot contain a newline")
	}
	methods, err := normaliseMethods(route.Methods)
	if err != nil {
		return err
	}
	route.Methods = methods
	if route.RateLimit < 0 {
		return errors.New("a rate limit cannot be negative")
	}
	if secret.Value == "" {
		return errors.New("a route secret needs a value")
	}
	return nil
}

func validateSign(secret *Secret) error {
	sign := secret.Sign
	switch sign.Algorithm {
	case AlgorithmSSHAgent:
		if !fingerprintPattern.MatchString(sign.Fingerprint) {
			return errors.New("an ssh-agent secret needs a `SHA256:` " +
				"fingerprint. Run `ssh-add -l` to list them")
		}
		if secret.Value != "" {
			return errors.New("an ssh-agent secret cannot have a value")
		}
	case AlgorithmHMACSHA256:
		if secret.Value == "" {
			return errors.New("an hmac-sha256 secret needs a value")
		}
	default:
		return fmt.Errorf(
			"%q is not a signing algorithm. Use %s or %s",
			sign.Algorithm, AlgorithmSSHAgent, AlgorithmHMACSHA256)
	}
	if sign.RateLimit < 0 {
		return errors.New("a rate limit cannot be negative")
	}
	return nil
}

func validateExpose(secret *Secret) error {
	if !environmentVariablePattern.MatchString(secret.Expose.Variable) {
		return fmt.Errorf(
			"%q is not an environment variable name", secret.Expose.Variable)
	}
	if secret.Value == "" {
		return errors.New("an exposed secret needs a value")
	}
	if strings.ContainsAny(secret.Value, "\r\n") {
		return errors.New("an exposed secret cannot contain a newline. " +
			"Use sign mode for a multi-line key")
	}
	return nil
}

// normaliseMethods returns nil for an empty list, which allows all
// methods.
func normaliseMethods(methods []string) ([]string, error) {
	seen := map[string]bool{}
	for _, method := range methods {
		method = strings.ToUpper(strings.TrimSpace(method))
		if method == "" {
			continue
		}
		if !httpMethodPattern.MatchString(method) {
			return nil, fmt.Errorf("%q is not an HTTP method", method)
		}
		seen[method] = true
	}
	if len(seen) == 0 {
		return nil, nil
	}
	normalised := make([]string, 0, len(seen))
	for method := range seen {
		normalised = append(normalised, method)
	}
	sort.Strings(normalised)
	return normalised, nil
}

func Digest(value string) string {
	if value == "" {
		return "-"
	}
	sum := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(sum[:])[:digestPreviewLength]
}

func SplitUpstream(upstream string) (host string, port int) {
	host, port, err := parseUpstream(upstream)
	if err != nil {
		return normaliseHost(upstream), defaultUpstreamPort
	}
	return host, port
}

func parseUpstream(upstream string) (host string, port int, err error) {
	trimmed := strings.TrimSpace(upstream)
	if trimmed == "" {
		return "", 0, errors.New("an upstream cannot be empty")
	}
	if strings.Contains(trimmed, "://") {
		return "", 0, fmt.Errorf(
			"upstream %q must not have a scheme", upstream)
	}
	if strings.ContainsAny(trimmed, "/@ \t\r\n") {
		return "", 0, fmt.Errorf(
			"upstream %q is not valid. Use host or host:port", upstream)
	}
	port = defaultUpstreamPort
	hostPart := trimmed
	if colon := strings.LastIndex(trimmed, ":"); colon >= 0 {
		hostPart = trimmed[:colon]
		portText := trimmed[colon+1:]
		parsedPort, parseErr := strconv.Atoi(portText)
		if parseErr != nil || parsedPort < 1 || parsedPort > 65535 ||
			strings.Contains(hostPart, ":") {
			return "", 0, fmt.Errorf(
				"upstream %q has an invalid port. Use 1 to 65535", upstream)
		}
		port = parsedPort
	}
	host = normaliseHost(hostPart)
	if host == "" {
		return "", 0, fmt.Errorf("upstream %q has no host", upstream)
	}
	return host, port, nil
}

func normaliseHost(host string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
}

func formatUpstream(host string, port int) string {
	if port == defaultUpstreamPort {
		return host
	}
	return host + ":" + strconv.Itoa(port)
}
