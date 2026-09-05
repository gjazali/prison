// Package session sets up the host environment and project context
// that commands need to run.
package session

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"prison"
	"prison/internal/cage"
	"prison/internal/cages"
	"prison/internal/config"
	"prison/internal/plugin"
	"prison/internal/state"
)

// ProjectConfigFileName is the per-project configuration file.
const ProjectConfigFileName = "prison.toml"

// Environment is the host-side context shared by every command.
// Loaded once and never mutated.
type Environment struct {
	Assets     fs.FS
	Version    string
	Executable string
	Overrides  *config.Overrides
	Global     *config.Global
	Root       *state.Root
	Cage       cage.Cage
	Registry   *plugin.Registry
	HostUID    int
	HostGID    int
	HomeDir    string
}

// Load takes an embedded asset filesystem and a version string.
// Returns the host Environment. Fails if the global config is
// unusable or the cage is unknown.
func Load(assets fs.FS, version string) (*Environment, error) {
	overrides, err := config.ReadOverrides(os.LookupEnv)
	if err != nil {
		return nil, err
	}
	root, err := state.OpenRoot(overrides.Root)
	if err != nil {
		return nil, err
	}
	global, err := config.LoadGlobal(root.ConfigFile())
	if err != nil {
		return nil, err
	}
	cageName := cages.DefaultName
	if global.Prison.Cage != nil && *global.Prison.Cage != "" {
		cageName = *global.Prison.Cage
	}
	if overrides.Cage != nil && *overrides.Cage != "" {
		cageName = *overrides.Cage
	}
	selected, err := cages.Lookup(cageName)
	if err != nil {
		return nil, err
	}
	bundled, err := fs.Sub(assets, prison.BundledInmatesDir)
	if err != nil {
		return nil, fmt.Errorf("the bundled inmates are missing from this build: %w", err)
	}
	executable, err := os.Executable()
	if err != nil {
		executable = os.Args[0]
	}
	homeDir, _ := os.UserHomeDir()
	return &Environment{
		Assets:     assets,
		Version:    version,
		Executable: executable,
		Overrides:  overrides,
		Global:     global,
		Root:       root,
		Cage:       selected,
		Registry:   plugin.NewRegistry(bundled, root.InmatesDir(), root.TrustDir()),
		HostUID:    os.Getuid(),
		HostGID:    os.Getgid(),
		HomeDir:    homeDir,
	}, nil
}

// Session is a project as a command sees it. Config is nil unless the
// file exists and is trusted.
type Session struct {
	*Environment
	Directory     string
	Project       *state.Project
	Record        *state.ProjectRecord
	ConfigPath    string
	ConfigPresent bool
	ConfigTrusted bool
	ConfigHash    string
	Config        *config.Project
	InmateNames   []string
	Inmates       []*plugin.Inmate
	BoxName       string
}

// OpenCurrent opens a session for the current working directory.
func (e *Environment) OpenCurrent() (*Session, error) {
	directory, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	return e.Open(directory)
}

// Open takes a directory path and returns a Session for that project.
// Returns an error if a trusted config no longer parses or an inmate
// is unknown or untrusted.
func (e *Environment) Open(directory string) (*Session, error) {
	project, err := e.Root.Project(directory)
	if err != nil {
		return nil, err
	}
	record, err := project.Record()
	if err != nil {
		return nil, err
	}
	realDirectory, err := filepath.EvalSymlinks(directory)
	if err != nil {
		realDirectory = directory
	}
	session := &Session{
		Environment: e,
		Directory:   realDirectory,
		Project:     project,
		Record:      record,
		ConfigPath:  filepath.Join(realDirectory, ProjectConfigFileName),
		BoxName:     project.BoxName(e.Overrides.Domain),
	}
	session.ConfigPresent, session.ConfigHash, err = configPresence(session.ConfigPath)
	if err != nil {
		return nil, err
	}
	if session.ConfigPresent && record != nil &&
		record.TrustedConfigSHA256 != "" &&
		record.TrustedConfigSHA256 == session.ConfigHash {
		session.ConfigTrusted = true
		session.Config, err = config.LoadProject(session.ConfigPath)
		if err != nil {
			return nil, err
		}
	}
	session.InmateNames = config.ResolveInmates(e.Overrides, session.Config, e.Global)
	session.Inmates, err = e.Registry.Load(session.InmateNames)
	if err != nil {
		return nil, err
	}
	return session, nil
}

// configPresence takes a file path. Returns whether the file exists
// and its SHA-256 hex digest.
func configPresence(path string) (present bool, hash string, err error) {
	file, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, "", nil
	}
	if err != nil {
		return false, "", err
	}
	defer file.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return false, "", err
	}
	return true, hex.EncodeToString(digest.Sum(nil)), nil
}

// HashFile takes a file path and returns its SHA-256 hex digest.
func HashFile(path string) (string, error) {
	_, hash, err := configPresence(path)
	if err != nil {
		return "", err
	}
	if hash == "" {
		return "", fmt.Errorf("%s does not exist", path)
	}
	return hash, nil
}

// ResolveBoxArgument takes a box name, project ID, or empty string
// and returns the matching project. An empty argument uses the
// working directory.
func (e *Environment) ResolveBoxArgument(argument string) (*state.Project, error) {
	if argument == "" {
		directory, err := os.Getwd()
		if err != nil {
			return nil, err
		}
		return e.Root.Project(directory)
	}
	candidate := strings.TrimSuffix(argument, "."+e.Overrides.Domain)
	if i := strings.LastIndex(candidate, "-"); i >= 0 {
		candidate = candidate[i+1:]
	}
	if !state.IsProjectID(candidate) {
		return nil, fmt.Errorf("%q is not a box name or project id; `prison list` to see all", argument)
	}
	project, err := e.Root.ProjectByID(candidate)
	if err != nil {
		return nil, fmt.Errorf("no state for %q; `prison list` shows the boxes", argument)
	}
	return project, nil
}

// RequireProjectRecord returns the project record. Returns an error
// if no box exists yet.
func (s *Session) RequireProjectRecord() (*state.ProjectRecord, error) {
	if s.Record == nil {
		return nil, fmt.Errorf("this project has no box yet; `prison up` creates one")
	}
	return s.Record, nil
}
