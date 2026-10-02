// Package policy defines the egress allowlist patterns, the matcher, and
// the built-in default list.
package policy

import (
	"bufio"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
)

var DefaultPorts = []int{80, 443}

// Pattern is one allowlist entry. A wildcard pattern matches the host and
// all of its subdomains.
type Pattern struct {
	Host     string
	Wildcard bool
	AllPorts bool
	Ports    []int
}

// ParsePattern accepts `host`, `*.host`, `host:port`, and `host:*`.
func ParsePattern(text string) (Pattern, error) {
	entry := strings.TrimSpace(text)
	if entry == "" {
		return Pattern{}, fmt.Errorf("allowlist entry is empty")
	}
	if strings.ContainsAny(entry, "/@ \t") {
		return Pattern{}, fmt.Errorf("host pattern %q is not valid. Use a host "+
			"name with no scheme, path, or credentials", entry)
	}
	host, portText, hasPort := strings.Cut(entry, ":")
	if strings.Contains(portText, ":") {
		return Pattern{}, fmt.Errorf(
			"host pattern %q has more than one `:port` suffix", entry)
	}
	pattern := Pattern{}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if strings.HasPrefix(host, "*.") {
		pattern.Wildcard = true
		host = host[2:]
	}
	if host == "" || strings.Contains(host, "*") || strings.HasPrefix(host, ".") || strings.HasSuffix(host, ".") {
		return Pattern{}, fmt.Errorf("host pattern %q is not valid. "+
			"A wildcard must be a leading `*.`", entry)
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
			return Pattern{}, fmt.Errorf("port %q in %q is not `*` or "+
				"a number from 1 to 65535", portText, entry)
		}
		pattern.Ports = []int{port}
	}
	return pattern, nil
}

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

func (p Pattern) MatchesHost(host string) bool {
	host = normalizeHost(host)
	if host == p.Host {
		return true
	}
	return p.Wildcard && strings.HasSuffix(host, "."+p.Host)
}

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

func normalizeHost(host string) string {
	return strings.ToLower(strings.TrimSuffix(host, "."))
}

// Decision is the result of an allowlist lookup. HostKnown is true when a
// pattern matches the host but not the port. Suggested holds the entry
// that allows a refused request.
type Decision struct {
	Allowed   bool
	HostKnown bool
	Suggested string
}

type AllowList struct {
	patterns []Pattern
}

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

func (a *AllowList) Patterns() []Pattern {
	return append([]Pattern(nil), a.patterns...)
}

func (a *AllowList) Strings() []string {
	out := make([]string, 0, len(a.patterns))
	for _, pattern := range a.patterns {
		out = append(out, pattern.String())
	}
	return out
}

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

func (a *AllowList) HostMatches(host string) bool {
	for _, pattern := range a.patterns {
		if pattern.MatchesHost(host) {
			return true
		}
	}
	return false
}

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
		panic("policy: default floor does not parse: " + err.Error())
	}
	return patterns
}
