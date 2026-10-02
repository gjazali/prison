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

type TrustRecord struct {
	Hash      string `json:"hash"`
	OriginURL string `json:"origin_url"`
	OriginRef string `json:"origin_ref"`
}

type Registry struct {
	bundled    fs.FS
	installDir string
	trustDir   string
}

func NewRegistry(bundled fs.FS, installDir, trustDir string) *Registry {
	return &Registry{bundled: bundled, installDir: installDir, trustDir: trustDir}
}

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
			inmate.Problem = fmt.Errorf(
				"%q is taken by a bundled inmate, so %s is ignored. "+
					"Run `prison inmate rm %s`",
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

func (r *Registry) Get(name string) (*Inmate, error) {
	inmates, err := r.List()
	if err != nil {
		return nil, err
	}
	var available []string
	var claiming []*Inmate
	for _, inmate := range inmates {
		if inmate.Name == name {
			return inmate, nil
		}
		if inmate.HasAlias(name) {
			claiming = append(claiming, inmate)
		}
		if len(available) == 0 || available[len(available)-1] != inmate.Name {
			available = append(available, inmate.Name)
		}
	}
	if len(claiming) > 0 {
		return inmateClaimingAlias(name, claiming)
	}
	listed := strings.Join(available, ", ")
	if listed == "" {
		listed = "none"
	}
	return nil, fmt.Errorf(
		"no inmate called %q. Installed inmates: %s", name, listed)
}

// inmateClaimingAlias relies on `List` to sort the bundled copy of a name
// first.
func inmateClaimingAlias(alias string, claiming []*Inmate) (*Inmate, error) {
	contested := []string{claiming[0].Name}
	for _, inmate := range claiming[1:] {
		if inmate.Name != claiming[0].Name {
			contested = append(contested, inmate.Name)
		}
	}
	if len(contested) == 1 {
		return claiming[0], nil
	}
	return nil, fmt.Errorf("%q is an alias of %s. Use the full name",
		alias, strings.Join(contested, " and "))
}

func (r *Registry) Load(names []string) ([]*Inmate, error) {
	loaded := make([]*Inmate, 0, len(names))
	seen := make(map[string]bool, len(names))
	for _, name := range names {
		inmate, err := r.Get(name)
		if err != nil {
			return nil, err
		}
		if seen[inmate.Name] {
			continue
		}
		seen[inmate.Name] = true
		if inmate.Problem != nil {
			return nil, fmt.Errorf("the %s inmate cannot be used: %w",
				inmate.Name, inmate.Problem)
		}
		if !inmate.Trusted {
			return nil, fmt.Errorf("the %s inmate changed after approval. "+
				"Run `prison inmate trust %s`", inmate.Name, inmate.Name)
		}
		loaded = append(loaded, inmate)
	}
	return loaded, nil
}

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
		inmate.Problem = fmt.Errorf(
			"its %s names the inmate %q but the directory is %q",
			ManifestFileName, manifest.Inmate.Name, name)
		return inmate
	}
	inmate.Manifest = *manifest
	return inmate
}

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

func (r *Registry) trustRecordPath(name string) string {
	return filepath.Join(r.trustDir, "inmates", name+".json")
}

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
		return nil, fmt.Errorf(
			"the approval for the %s inmate is not valid JSON: %w", name, err)
	}
	return record, nil
}

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

func (r *Registry) removeTrustRecord(name string) error {
	if err := os.Remove(r.trustRecordPath(name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("cannot remove %s: %w", r.trustRecordPath(name), err)
	}
	return nil
}

func (i *Inmate) HasAlias(name string) bool {
	for _, alias := range i.Inmate.Aliases {
		if alias == name {
			return true
		}
	}
	return false
}

func (i *Inmate) Origin() (string, string) {
	if i.Trust == nil {
		return "", ""
	}
	return i.Trust.OriginURL, i.Trust.OriginRef
}
