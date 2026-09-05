package session

import (
	"context"
	"fmt"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"prison/internal/broker/control"
	"prison/internal/broker/protocol"
	"prison/internal/cage"
	"prison/internal/config"
	"prison/internal/plugin"
	"prison/internal/state"
)

// GuestHome is the box user's home directory.
const GuestHome = "/home/dev"

// WorkspaceDir is where the project directory is mounted in the box.
const WorkspaceDir = "/workspace"

// TrustStorePath is the CA bundle path inside the box.
const TrustStorePath = "/etc/ssl/certs/ca-certificates.crt"

// TrustStoreVariables are the environment variables pointed at
// TrustStorePath.
var TrustStoreVariables = []string{
	"SSL_CERT_FILE",
	"NODE_EXTRA_CA_CERTS",
	"REQUESTS_CA_BUNDLE",
	"CURL_CA_BUNDLE",
	"GIT_SSL_CAINFO",
}

// AutomaticShadowPaths are directories shadowed in the box when the
// project has them.
var AutomaticShadowPaths = []string{"node_modules", ".venv"}

// PlaceholderToken returns the API key given to inmates in the box.
// The broker replaces it with the real credential.
func (s *Session) PlaceholderToken() string {
	return "prison-placeholder-" + s.Project.ID
}

// PlaceholderTail returns the last twenty characters of the
// placeholder token.
func (s *Session) PlaceholderTail() string {
	placeholder := s.PlaceholderToken()
	if len(placeholder) <= 20 {
		return placeholder
	}
	return placeholder[len(placeholder)-20:]
}

// CreationEnvironment takes a token, sudo flag, and persist paths.
// Returns the environment variables set when the box is created.
func (s *Session) CreationEnvironment(token string, sudo bool,
	persistPaths []string) []string {
	assignments := []string{
		"PRISON_TOKEN=" + token,
		"PRISON_BROKER_PORT=" + strconv.Itoa(s.Overrides.BrokerPort),
		"PRISON_SUDO=" + yesNo(sudo),
		"LANG=C.UTF-8",
	}
	if len(persistPaths) > 0 {
		assignments = append(assignments,
			"PRISON_PERSIST_PATHS="+strings.Join(persistPaths, ":"))
	}
	return assignments
}

// ExecEnvironment takes a context, broker control client, and TERM
// value. Returns the environment variables for running commands in
// the box. A nil client skips granted secrets.
func (s *Session) ExecEnvironment(ctx context.Context, client *control.Client,
	term string) ([]string, error) {
	collected := newAssignments()
	collected.set("LANG", "C.UTF-8")
	for _, inmate := range s.Inmates {
		for _, assignment := range inmate.EnvironmentAssignments() {
			collected.add(assignment)
		}
	}
	for _, inmate := range s.Inmates {
		assignments, err := plugin.HostEnvironment(inmate, s.HomeDir)
		if err != nil {
			return nil, fmt.Errorf(
				"the %s inmate's [host] environment cannot be read: %w",
				inmate.Name, err)
		}
		for _, assignment := range assignments {
			collected.add(assignment)
		}
	}
	for _, inmate := range s.Inmates {
		if inmate.Auth == nil {
			continue
		}
		collected.set(inmate.Auth.BaseURLVariable,
			"http://"+protocol.InmateHost(inmate.Name))
		collected.set(inmate.Auth.TokenVariable, s.PlaceholderToken())
	}
	if client != nil {
		variables, err := client.ProjectEnvironment(ctx, s.Project.ID)
		if err != nil {
			return nil, fmt.Errorf(
				"cannot read this project's secrets from the broker: %w", err)
		}
		for _, assignment := range variables {
			collected.add(assignment)
		}
	}
	for _, name := range TrustStoreVariables {
		collected.set(name, TrustStorePath)
	}
	if s.Config != nil {
		for name, value := range s.Config.Environment {
			collected.set(name, value)
		}
	}
	if term != "" {
		collected.set("TERM", term)
	}
	return collected.sorted(), nil
}

// assignments collects `NAME=value` pairs. Later values replace
// earlier ones for the same name.
type assignments struct {
	values map[string]string
	order  []string
}

// newAssignments returns an empty assignments collection.
func newAssignments() *assignments {
	return &assignments{values: map[string]string{}}
}

// set takes a name and value. Replaces any previous value for that
// name. Ignores empty names.
func (a *assignments) set(name, value string) {
	if name == "" {
		return
	}
	if _, seen := a.values[name]; !seen {
		a.order = append(a.order, name)
	}
	a.values[name] = value
}

// add takes a `NAME=value` string and sets it. Ignores lines without
// `=`.
func (a *assignments) add(assignment string) {
	name, value, found := strings.Cut(assignment, "=")
	if !found {
		return
	}
	a.set(name, value)
}

// sorted returns all pairs as `NAME=value` strings, sorted by name.
func (a *assignments) sorted() []string {
	names := append([]string(nil), a.order...)
	sort.Strings(names)
	out := make([]string, 0, len(names))
	for _, name := range names {
		out = append(out, name+"="+a.values[name])
	}
	return out
}

// yesNo takes a bool and returns "yes" or "no".
func yesNo(value bool) string {
	if value {
		return "yes"
	}
	return "no"
}

// ShadowPaths returns the workspace-relative directories the box
// shadows, sorted and without duplicates.
func (s *Session) ShadowPaths() []string {
	seen := map[string]bool{}
	var paths []string
	add := func(candidate string) {
		candidate = strings.Trim(candidate, "/")
		if candidate == "" || seen[candidate] {
			return
		}
		seen[candidate] = true
		paths = append(paths, candidate)
	}
	for _, candidate := range AutomaticShadowPaths {
		if directoryExists(filepath.Join(s.Directory, candidate)) {
			add(candidate)
		}
	}
	if s.Config != nil {
		for _, candidate := range s.Config.Box.Shadow {
			add(candidate)
		}
	}
	sort.Strings(paths)
	return paths
}

// PersistPaths returns guest paths that survive box destruction, in
// inmate order and without duplicates.
func (s *Session) PersistPaths() []string {
	seen := map[string]bool{}
	var paths []string
	for _, inmate := range s.Inmates {
		for _, guestPath := range inmate.Persist.Paths {
			if !seen[guestPath] {
				seen[guestPath] = true
				paths = append(paths, guestPath)
			}
		}
	}
	return paths
}

// PersistedHome takes an inmate and returns the host directory backing
// its home in the box.
func (s *Session) PersistedHome(inmate *plugin.Inmate) string {
	if len(inmate.Persist.Paths) == 0 {
		return s.Project.HomeDir(inmate.Name)
	}
	return s.hostPathForGuestPath(inmate, inmate.Persist.Paths[0])
}

// hostPathForGuestPath takes an inmate and a guest path. Returns the
// host directory that backs it.
func (s *Session) hostPathForGuestPath(inmate *plugin.Inmate,
	guestPath string) string {
	relative := strings.TrimPrefix(guestPath, GuestHome+"/")
	relative = strings.TrimPrefix(relative, "/")
	return filepath.Join(s.Project.HomeDir(inmate.Name),
		filepath.FromSlash(relative))
}

// Mounts takes shadow paths and returns the bind mounts for the box.
func (s *Session) Mounts(shadowPaths []string) []cage.Mount {
	mounts := []cage.Mount{{
		Source: s.Directory,
		Target: WorkspaceDir,
	}}
	for _, relative := range shadowPaths {
		mounts = append(mounts, cage.Mount{
			Source: s.Project.ShadowDir(relative),
			Target: path.Join(WorkspaceDir, filepath.ToSlash(relative)),
		})
	}
	if directoryExists(filepath.Join(s.Directory, ".git")) {
		mounts = append(mounts, cage.Mount{
			Source:   s.Project.EmptyDir(),
			Target:   WorkspaceDir + "/.git/hooks",
			ReadOnly: true,
		})
	}
	for _, inmate := range s.Inmates {
		for _, guestPath := range inmate.Persist.Paths {
			mounts = append(mounts, cage.Mount{
				Source: s.hostPathForGuestPath(inmate, guestPath),
				Target: guestPath,
			})
		}
	}
	return mounts
}

// Shape takes an image name, port mappings, and shadow paths. Returns
// the BoxShape that a box created now would have.
func (s *Session) Shape(image string, ports []cage.PortMapping,
	shadowPaths []string) *state.BoxShape {
	recorded := make([][2]int, 0, len(ports))
	for _, mapping := range ports {
		recorded = append(recorded, [2]int{mapping.Host, mapping.Guest})
	}
	return &state.BoxShape{
		Image:   image,
		CPUs:    config.ResolveCPUs(s.Overrides, s.Config),
		Memory:  config.ResolveMemory(s.Overrides, s.Config),
		Sudo:    config.ResolveSudo(s.Overrides, s.Config),
		Network: s.Overrides.Network,
		Shadow:  shadowPaths,
		Ports:   recorded,
		Inmates: append([]string(nil), s.InmateNames...),
	}
}

// DescribeShapeDrift takes two BoxShapes and returns a sentence for
// each field that differs. Returns nil if nothing changed.
func DescribeShapeDrift(recorded, resolved *state.BoxShape) []string {
	if recorded == nil || resolved == nil {
		return nil
	}
	var drift []string
	compare := func(field, was, now string) {
		if was != now {
			drift = append(drift, fmt.Sprintf(
				"%s was %s at creation and is %s now", field, was, now))
		}
	}
	compare("the image", recorded.Image, resolved.Image)
	compare("cpus", strconv.Itoa(recorded.CPUs), strconv.Itoa(resolved.CPUs))
	compare("memory", recorded.Memory, resolved.Memory)
	compare("sudo", yesNo(recorded.Sudo), yesNo(resolved.Sudo))
	compare("the network", recorded.Network, resolved.Network)
	compare("the shadowed directories",
		joinOrNone(recorded.Shadow), joinOrNone(resolved.Shadow))
	compare("the inmates",
		joinOrNone(recorded.Inmates), joinOrNone(resolved.Inmates))
	compare("the published ports",
		joinPorts(recorded.Ports), joinPorts(resolved.Ports))
	return drift
}

// joinOrNone takes a string slice and returns it as a comma-separated
// list, or "none" if empty.
func joinOrNone(values []string) string {
	if len(values) == 0 {
		return "none"
	}
	return strings.Join(values, ", ")
}

// joinPorts takes port pairs and returns them as "host:guest" strings,
// or "none" if empty.
func joinPorts(ports [][2]int) string {
	if len(ports) == 0 {
		return "none"
	}
	rendered := make([]string, 0, len(ports))
	for _, mapping := range ports {
		rendered = append(rendered,
			strconv.Itoa(mapping[0])+":"+strconv.Itoa(mapping[1]))
	}
	return strings.Join(rendered, " ")
}

// Profile returns the broker profile for this project. Includes
// routes for inmates with `[auth]` and deduplicated egress patterns.
func (s *Session) Profile() *state.Profile {
	profile := &state.Profile{Written: time.Now()}
	for _, inmate := range s.Inmates {
		if inmate.Auth == nil {
			continue
		}
		route := state.InmateRoute{
			Name:       inmate.Name,
			Upstream:   inmate.Auth.Upstream,
			PathPrefix: inmate.Auth.PathPrefix,
		}
		for _, credential := range inmate.Auth.Credentials {
			route.Credentials = append(route.Credentials, state.CredentialSpec{
				Variable: credential.Variable,
				Header:   credential.Header,
				Prefix:   credential.Prefix,
			})
		}
		profile.Inmates = append(profile.Inmates, route)
	}
	seen := map[string]bool{}
	addHost := func(pattern string) {
		if pattern == "" || seen[pattern] {
			return
		}
		seen[pattern] = true
		profile.Egress = append(profile.Egress, pattern)
	}
	for _, inmate := range s.Inmates {
		for _, host := range inmate.Egress.Hosts {
			addHost(host)
		}
	}
	if s.Config != nil {
		for _, host := range s.Config.Egress.Hosts {
			addHost(host)
		}
	}
	return profile
}

// CredentialVariables returns the credential variable names from all
// enabled inmates, without duplicates.
func (s *Session) CredentialVariables() []string {
	seen := map[string]bool{}
	var variables []string
	for _, inmate := range s.Inmates {
		if inmate.Auth == nil {
			continue
		}
		for _, credential := range inmate.Auth.Credentials {
			if !seen[credential.Variable] {
				seen[credential.Variable] = true
				variables = append(variables, credential.Variable)
			}
		}
	}
	return variables
}
