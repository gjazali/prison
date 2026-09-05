// Package policy provides the egress allowlist. It defines the
// pattern syntax, the matcher, and the built-in default list.
package policy

import (
	"bufio"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
)

// DefaultPorts lists the ports allowed when a pattern names no port.
var DefaultPorts = []int{80, 443}

// Pattern is one parsed allowlist entry. Wildcard means the pattern
// also matches subdomains. AllPorts means any port is allowed.
type Pattern struct {
	Host     string
	Wildcard bool
	AllPorts bool
	Ports    []int
}

// ParsePattern parses one allowlist entry. It takes a string like
// "host", "*.host", "host:port", or "host:*" and returns a Pattern.
// It returns an error for invalid input.
func ParsePattern(text string) (Pattern, error) {
	entry := strings.TrimSpace(text)
	if entry == "" {
		return Pattern{}, fmt.Errorf("an allowlist entry cannot be empty")
	}
	if strings.ContainsAny(entry, "/@ \t") {
		return Pattern{}, fmt.Errorf("%q is not a host pattern; use a bare host name with no scheme, path, or credentials", entry)
	}
	host, portText, hasPort := strings.Cut(entry, ":")
	if strings.Contains(portText, ":") {
		return Pattern{}, fmt.Errorf("%q is not a host pattern; only one `:port` suffix is allowed", entry)
	}
	pattern := Pattern{}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if strings.HasPrefix(host, "*.") {
		pattern.Wildcard = true
		host = host[2:]
	}
	if host == "" || strings.Contains(host, "*") || strings.HasPrefix(host, ".") || strings.HasSuffix(host, ".") {
		return Pattern{}, fmt.Errorf("%q is not a host pattern; a wildcard is only allowed as a leading `*.`", entry)
	}
	pattern.Host = host
	switch {
	case !hasPort:
		pattern.Ports = append([]int(nil), DefaultPorts...)
	case portText == "*":
		pattern.AllPorts = true
	default:
		port, err := strconv.Atoi(portText)
		if err != nil || port < 1 || port > 65535 {
			return Pattern{}, fmt.Errorf("%q names port %q, which is not `*` or a number from 1 to 65535", entry, portText)
		}
		pattern.Ports = []int{port}
	}
	return pattern, nil
}

// ParseList reads allowlist entries from r, one per line. It skips
// blank lines and comments. It returns an error on the first bad
// line.
func ParseList(r io.Reader) ([]Pattern, error) {
	var patterns []Pattern
	scanner := bufio.NewScanner(r)
	line := 0
	for scanner.Scan() {
		line++
		text := scanner.Text()
		if i := strings.IndexByte(text, '#'); i >= 0 {
			text = text[:i]
		}
		text = strings.TrimSpace(text)
		if text == "" {
			continue
		}
		pattern, err := ParsePattern(text)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}
		patterns = append(patterns, pattern)
	}
	return patterns, scanner.Err()
}

// ParseStrings parses a slice of allowlist entries. It returns an
// error on the first bad entry.
func ParseStrings(entries []string) ([]Pattern, error) {
	patterns := make([]Pattern, 0, len(entries))
	for i, entry := range entries {
		pattern, err := ParsePattern(entry)
		if err != nil {
			return nil, fmt.Errorf("entry %d: %w", i+1, err)
		}
		patterns = append(patterns, pattern)
	}
	return patterns, nil
}

// String returns the pattern as a parsable string.
func (p Pattern) String() string {
	host := p.Host
	if p.Wildcard {
		host = "*." + host
	}
	switch {
	case p.AllPorts:
		return host + ":*"
	case len(p.Ports) == 1 && !isDefaultPorts(p.Ports):
		return host + ":" + strconv.Itoa(p.Ports[0])
	default:
		return host
	}
}

// isDefaultPorts reports whether ports matches DefaultPorts.
func isDefaultPorts(ports []int) bool {
	if len(ports) != len(DefaultPorts) {
		return false
	}
	for i, port := range ports {
		if port != DefaultPorts[i] {
			return false
		}
	}
	return true
}

// MatchesHost reports whether the pattern matches host, ignoring
// ports.
func (p Pattern) MatchesHost(host string) bool {
	host = normalizeHost(host)
	if host == p.Host {
		return true
	}
	return p.Wildcard && strings.HasSuffix(host, "."+p.Host)
}

// MatchesPort reports whether the pattern allows port.
func (p Pattern) MatchesPort(port int) bool {
	if p.AllPorts {
		return true
	}
	for _, allowed := range p.Ports {
		if allowed == port {
			return true
		}
	}
	return false
}

// normalizeHost lowercases host and removes a trailing dot.
func normalizeHost(host string) string {
	return strings.ToLower(strings.TrimSuffix(host, "."))
}

// Decision is the result of an allowlist check. HostKnown is true
// when the host matches a pattern on a different port. Suggested
// holds the entry that would allow a refused request.
type Decision struct {
	Allowed   bool
	HostKnown bool
	Suggested string
}

// AllowList is an ordered set of unique patterns.
type AllowList struct {
	patterns []Pattern
}

// NewAllowList merges the given pattern lists, dropping duplicates.
func NewAllowList(lists ...[]Pattern) *AllowList {
	seen := map[string]bool{}
	list := &AllowList{}
	for _, patterns := range lists {
		for _, pattern := range patterns {
			key := pattern.String()
			if seen[key] {
				continue
			}
			seen[key] = true
			list.patterns = append(list.patterns, pattern)
		}
	}
	return list
}

// Patterns returns a copy of the patterns.
func (a *AllowList) Patterns() []Pattern {
	return append([]Pattern(nil), a.patterns...)
}

// Strings returns the patterns as a string slice.
func (a *AllowList) Strings() []string {
	out := make([]string, 0, len(a.patterns))
	for _, pattern := range a.patterns {
		out = append(out, pattern.String())
	}
	return out
}

// Allows checks whether host may be reached on port. It returns a
// Decision with the result and a suggestion if the port is wrong.
func (a *AllowList) Allows(host string, port int) Decision {
	decision := Decision{}
	for _, pattern := range a.patterns {
		if !pattern.MatchesHost(host) {
			continue
		}
		if pattern.MatchesPort(port) {
			return Decision{Allowed: true, HostKnown: true}
		}
		decision.HostKnown = true
	}
	if decision.HostKnown {
		decision.Suggested = normalizeHost(host) + ":" + strconv.Itoa(port)
	}
	return decision
}

// HostMatches reports whether any pattern matches host on any port.
func (a *AllowList) HostMatches(host string) bool {
	for _, pattern := range a.patterns {
		if pattern.MatchesHost(host) {
			return true
		}
	}
	return false
}

// PortsForHost returns the allowed ports for host (sorted) and
// whether all ports are open.
func (a *AllowList) PortsForHost(host string) (ports []int, all bool) {
	seen := map[int]bool{}
	for _, pattern := range a.patterns {
		if !pattern.MatchesHost(host) {
			continue
		}
		if pattern.AllPorts {
			all = true
			continue
		}
		for _, port := range pattern.Ports {
			if !seen[port] {
				seen[port] = true
				ports = append(ports, port)
			}
		}
	}
	sort.Ints(ports)
	return ports, all
}

// DefaultFloor returns the built-in allowlist of common registries
// and source hosts.
func DefaultFloor() []Pattern {
	entries := []string{
		"registry.npmjs.org",
		"registry.yarnpkg.com",
		"*.npmjs.org",
		"github.com",
		"*.github.com",
		"*.githubusercontent.com",
		"ghcr.io",
		"deb.debian.org",
		"security.debian.org",
		"pypi.org",
		"files.pythonhosted.org",
		"proxy.golang.org",
		"sum.golang.org",
		"crates.io",
		"static.crates.io",
	}
	patterns, err := ParseStrings(entries)
	if err != nil {
		panic("policy: the default floor does not parse: " + err.Error())
	}
	return patterns
}
