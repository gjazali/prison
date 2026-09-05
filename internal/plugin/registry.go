package plugin

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Inmate is one plugin found by the registry. Problem holds the
// reason it cannot be used, or nil if healthy.
type Inmate struct {
	Manifest
	Name    string
	Dir     string
	FS      fs.FS
	Bundled bool
	Trusted bool
	Trust   *TrustRecord
	Problem error
}

// TrustRecord holds the approved tree hash and origin repo for one
// installed inmate.
type TrustRecord struct {
	Hash      string `json:"hash"`
	OriginURL string `json:"origin_url"`
	OriginRef string `json:"origin_ref"`
}

// Registry finds inmates from bundled plugins and the install
// directory.
type Registry struct {
	bundled    fs.FS
	installDir string
	trustDir   string
}

// NewRegistry takes a bundled filesystem, an install directory, and a
// trust directory. Returns a new Registry. Neither directory needs
// to exist.
func NewRegistry(bundled fs.FS, installDir, trustDir string) *Registry {
	return &Registry{bundled: bundled, installDir: installDir, trustDir: trustDir}
}

// List returns all inmates sorted by name. Bundled inmates come
// before installed ones with the same name. Returns an error only
// if a directory cannot be read.
func (r *Registry) List() ([]*Inmate, error) {
	inmates, err := r.bundledInmates()
	if err != nil {
		return nil, err
	}
	shipped := make(map[string]bool, len(inmates))
	for _, inmate := range inmates {
		shipped[inmate.Name] = true
	}
	installed, err := r.installedInmates()
	if err != nil {
		return nil, err
	}
	for _, inmate := range installed {
		if shipped[inmate.Name] {
			inmate.Problem = fmt.Errorf("prison ships an inmate called %q, so the copy installed at %s is ignored; `prison inmate rm %s` removes it",
				inmate.Name, inmate.Dir, inmate.Name)
		}
		inmates = append(inmates, inmate)
	}
	sort.SliceStable(inmates, func(first, second int) bool {
		if inmates[first].Name != inmates[second].Name {
			return inmates[first].Name < inmates[second].Name
		}
		return inmates[first].Bundled && !inmates[second].Bundled
	})
	return inmates, nil
}

// Get takes a name and returns the matching inmate, preferring
// bundled copies. Returns an error only for unknown names.
func (r *Registry) Get(name string) (*Inmate, error) {
	inmates, err := r.List()
	if err != nil {
		return nil, err
	}
	var available []string
	for _, inmate := range inmates {
		if inmate.Name == name {
			return inmate, nil
		}
		if len(available) == 0 || available[len(available)-1] != inmate.Name {
			available = append(available, inmate.Name)
		}
	}
	listed := strings.Join(available, ", ")
	if listed == "" {
		listed = "none"
	}
	return nil, fmt.Errorf("no inmate called %q; installed inmates: %s", name, listed)
}

// Load takes a list of names and returns the inmates a session may
// use, in order and without repeats. Returns an error for unknown,
// broken, or untrusted inmates.
func (r *Registry) Load(names []string) ([]*Inmate, error) {
	loaded := make([]*Inmate, 0, len(names))
	seen := make(map[string]bool, len(names))
	for _, name := range names {
		if seen[name] {
			continue
		}
		seen[name] = true
		inmate, err := r.Get(name)
		if err != nil {
			return nil, err
		}
		if inmate.Problem != nil {
			return nil, fmt.Errorf("the %s inmate cannot be used: %w", name, inmate.Problem)
		}
		if !inmate.Trusted {
			return nil, fmt.Errorf("the %s inmate has changed since it was approved; `prison inmate trust %s` re-approves it",
				name, name)
		}
		loaded = append(loaded, inmate)
	}
	return loaded, nil
}

// BundledNames returns the sorted names of inmates that ship with
// prison.
func (r *Registry) BundledNames() ([]string, error) {
	inmates, err := r.bundledInmates()
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(inmates))
	for _, inmate := range inmates {
		names = append(names, inmate.Name)
	}
	sort.Strings(names)
	return names, nil
}

// bundledInmates reads inmates compiled into the binary. Skips
// entries without a manifest. Bundled inmates are always trusted.
func (r *Registry) bundledInmates() ([]*Inmate, error) {
	if r.bundled == nil {
		return nil, nil
	}
	entries, err := fs.ReadDir(r.bundled, ".")
	if err != nil {
		return nil, fmt.Errorf("cannot read the bundled inmates: %w", err)
	}
	var inmates []*Inmate
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		directory, err := fs.Sub(r.bundled, entry.Name())
		if err != nil {
			return nil, fmt.Errorf("cannot read the bundled %s inmate: %w", entry.Name(), err)
		}
		if _, err := fs.Stat(directory, ManifestFileName); err != nil {
			continue
		}
		inmate := newInmate(entry.Name(), "", directory)
		inmate.Bundled = true
		inmate.Trusted = true
		inmates = append(inmates, inmate)
	}
	return inmates, nil
}

// installedInmates reads inmates from the install directory and pairs
// each with its trust record. A missing directory is not an error.
func (r *Registry) installedInmates() ([]*Inmate, error) {
	if r.installDir == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(r.installDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("cannot read %s: %w", r.installDir, err)
	}
	var inmates []*Inmate
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		directory := filepath.Join(r.installDir, entry.Name())
		if _, err := os.Stat(filepath.Join(directory, ManifestFileName)); err != nil {
			continue
		}
		inmate := newInmate(entry.Name(), directory, os.DirFS(directory))
		record, err := r.readTrustRecord(entry.Name())
		if err != nil {
			return nil, err
		}
		inmate.Trust = record
		inmate.Trusted = r.hashMatchesRecord(inmate, record)
		inmates = append(inmates, inmate)
	}
	return inmates, nil
}

// newInmate takes a name, directory path, and filesystem. Reads and
// validates the manifest. Records failures in Problem instead of
// returning them.
func newInmate(name, directory string, fsys fs.FS) *Inmate {
	inmate := &Inmate{Name: name, Dir: directory, FS: fsys}
	data, err := fs.ReadFile(fsys, ManifestFileName)
	if err != nil {
		inmate.Problem = fmt.Errorf("cannot read its %s: %w", ManifestFileName, err)
		return inmate
	}
	manifest, err := ParseManifest(data)
	if err != nil {
		inmate.Problem = err
		return inmate
	}
	if manifest.Inmate.Name != name {
		inmate.Problem = fmt.Errorf("its %s names the inmate %q, but it sits in a directory called %q",
			ManifestFileName, manifest.Inmate.Name, name)
		return inmate
	}
	inmate.Manifest = *manifest
	return inmate
}

// hashMatchesRecord returns true if the inmate's tree hash matches
// the record. Returns false for missing records or unreadable trees.
func (r *Registry) hashMatchesRecord(inmate *Inmate, record *TrustRecord) bool {
	if record == nil || record.Hash == "" {
		return false
	}
	hash, err := TreeHash(inmate.FS)
	if err != nil {
		return false
	}
	return hash == record.Hash
}

// trustRecordPath takes a name and returns the path to its trust
// record file.
func (r *Registry) trustRecordPath(name string) string {
	return filepath.Join(r.trustDir, "inmates", name+".json")
}

// readTrustRecord takes a name and returns its trust record, or nil
// if never approved. Returns an error for unreadable or malformed
// records.
func (r *Registry) readTrustRecord(name string) (*TrustRecord, error) {
	data, err := os.ReadFile(r.trustRecordPath(name))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("cannot read the approval for the %s inmate: %w", name, err)
	}
	record := &TrustRecord{}
	if err := json.Unmarshal(data, record); err != nil {
		return nil, fmt.Errorf("the approval for the %s inmate is not readable JSON: %w", name, err)
	}
	return record, nil
}

// writeTrustRecord takes a name and record and writes it to disk.
// Creates the trust directory if missing.
func (r *Registry) writeTrustRecord(name string, record *TrustRecord) error {
	path := r.trustRecordPath(name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("cannot create %s: %w", filepath.Dir(path), err)
	}
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return fmt.Errorf("cannot record the approval for the %s inmate: %w", name, err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("cannot write %s: %w", path, err)
	}
	return nil
}

// removeTrustRecord deletes the trust record for the named inmate. A
// missing record is not an error.
func (r *Registry) removeTrustRecord(name string) error {
	if err := os.Remove(r.trustRecordPath(name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("cannot remove %s: %w", r.trustRecordPath(name), err)
	}
	return nil
}

// Origin returns the repository URL and ref the inmate was installed
// from. Both are empty if unknown.
func (i *Inmate) Origin() (string, string) {
	if i.Trust == nil {
		return "", ""
	}
	return i.Trust.OriginURL, i.Trust.OriginRef
}
