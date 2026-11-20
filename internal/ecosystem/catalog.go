package ecosystem

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"prison/internal/state"
)

const (
	DefaultCatalogURL   = "https://github.com/gjazali/prison-ecosystems.git"
	ecosystemsSubdir    = "ecosystems"
	originMetadataFile  = "origin.json"
	catalogDirectoryMod = 0o755
)

type Catalog struct {
	Dir        string
	Ecosystems []*Ecosystem
}

type CatalogOrigin struct {
	Source  string    `json:"source"`
	Ref     string    `json:"ref,omitempty"`
	Updated time.Time `json:"updated"`
}

func (c *Catalog) Get(name string) (*Ecosystem, bool) {
	for _, eco := range c.Ecosystems {
		if eco.Name == name {
			return eco, true
		}
	}
	return nil, false
}

// LoadCatalog reads every `*.toml` definition from `dir/ecosystems` if present,
// or from `dir` directly.
func LoadCatalog(dir string) (*Catalog, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, errors.New("ecosystem catalog directory is empty")
	}
	searchDir := dir
	nested := filepath.Join(dir, ecosystemsSubdir)
	if info, err := os.Stat(nested); err == nil && info.IsDir() {
		searchDir = nested
	}
	entries, err := os.ReadDir(searchDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("ecosystem catalog not found at %s", dir)
		}
		return nil, fmt.Errorf("cannot read ecosystem catalog %s: %w", searchDir, err)
	}
	catalog := &Catalog{Dir: dir}
	seen := map[string]string{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".toml") {
			continue
		}
		path := filepath.Join(searchDir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("cannot read %s: %w", path, err)
		}
		eco, err := Parse(data)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", entry.Name(), err)
		}
		if previous, dup := seen[eco.Name]; dup {
			return nil, fmt.Errorf("duplicate ecosystem %q in %s and %s",
				eco.Name, previous, entry.Name())
		}
		seen[eco.Name] = entry.Name()
		catalog.Ecosystems = append(catalog.Ecosystems, eco)
	}
	if len(catalog.Ecosystems) == 0 {
		return nil, fmt.Errorf("no ecosystem definitions found in %s", searchDir)
	}
	sort.Slice(catalog.Ecosystems, func(i, j int) bool {
		return catalog.Ecosystems[i].Name < catalog.Ecosystems[j].Name
	})
	return catalog, nil
}

func ReadOrigin(dir string) (*CatalogOrigin, error) {
	data, err := os.ReadFile(filepath.Join(dir, originMetadataFile))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	origin := &CatalogOrigin{}
	if err := json.Unmarshal(data, origin); err != nil {
		return nil, fmt.Errorf("cannot read %s: %w", originMetadataFile, err)
	}
	return origin, nil
}

// UpdateCatalog fetches ecosystem definitions from a local path or git URL,
// validates every file, and atomically replaces `destination`.
func UpdateCatalog(
	ctx context.Context, source, ref, destination string,
) (*Catalog, *CatalogOrigin, error) {
	if source == "" {
		if existing, err := ReadOrigin(destination); err == nil && existing != nil && existing.Source != "" {
			source = existing.Source
			if ref == "" {
				ref = existing.Ref
			}
		} else {
			source = DefaultCatalogURL
		}
	}

	work, err := os.MkdirTemp("", "prison-ecosystems-")
	if err != nil {
		return nil, nil, fmt.Errorf("cannot create a working directory: %w", err)
	}
	defer os.RemoveAll(work)

	fetched := filepath.Join(work, "catalog")
	if isLocalDirectory(source) {
		if ref != "" {
			return nil, nil, errors.New("cannot use `--ref` with a local directory")
		}
		absSource, err := filepath.Abs(source)
		if err != nil {
			return nil, nil, fmt.Errorf("cannot resolve %s: %w", source, err)
		}
		source = absSource
		if err := copyEcosystemFiles(source, fetched); err != nil {
			return nil, nil, err
		}
	} else {
		if err := cloneCatalog(ctx, source, ref, fetched); err != nil {
			return nil, nil, err
		}
	}

	if _, err := LoadCatalog(fetched); err != nil {
		return nil, nil, fmt.Errorf("the fetched catalog is not valid: %w", err)
	}

	origin := &CatalogOrigin{
		Source:  source,
		Ref:     ref,
		Updated: time.Now(),
	}
	if err := installCatalogFiles(fetched, destination, origin); err != nil {
		return nil, nil, err
	}
	installed, err := LoadCatalog(destination)
	if err != nil {
		return nil, nil, err
	}
	return installed, origin, nil
}

func isLocalDirectory(source string) bool {
	if strings.Contains(source, "://") {
		return false
	}
	info, err := os.Stat(source)
	return err == nil && info.IsDir()
}

func cloneCatalog(ctx context.Context, url, ref, destination string) error {
	arguments := []string{"clone", "--depth", "1", "--quiet"}
	if ref != "" {
		arguments = append(arguments, "--branch", ref)
	}
	arguments = append(arguments, "--", url, destination)
	command := exec.CommandContext(ctx, "git", arguments...)
	output, err := command.CombinedOutput()
	if err != nil {
		detail := strings.TrimSpace(string(output))
		if detail != "" {
			return fmt.Errorf("cannot fetch %s: %s", url, detail)
		}
		return fmt.Errorf("cannot fetch %s: %w", url, err)
	}
	return nil
}

func copyEcosystemFiles(source, destination string) error {
	from := source
	nested := filepath.Join(source, ecosystemsSubdir)
	if info, err := os.Stat(nested); err == nil && info.IsDir() {
		from = nested
	}
	targetDir := filepath.Join(destination, ecosystemsSubdir)
	if err := os.MkdirAll(targetDir, catalogDirectoryMod); err != nil {
		return fmt.Errorf("cannot create %s: %w", targetDir, err)
	}
	entries, err := os.ReadDir(from)
	if err != nil {
		return fmt.Errorf("cannot read %s: %w", from, err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".toml") {
			continue
		}
		if err := copyRegularFile(
			filepath.Join(from, entry.Name()),
			filepath.Join(targetDir, entry.Name()),
		); err != nil {
			return err
		}
	}
	return nil
}

func installCatalogFiles(fetched, destination string, origin *CatalogOrigin) error {
	parent := filepath.Dir(destination)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return fmt.Errorf("cannot create %s: %w", parent, err)
	}
	staged, err := os.MkdirTemp(parent, ".ecosystems-*")
	if err != nil {
		return fmt.Errorf("cannot stage catalog in %s: %w", parent, err)
	}
	defer os.RemoveAll(staged)

	if err := copyEcosystemFiles(fetched, staged); err != nil {
		return err
	}
	encodedOrigin, err := json.MarshalIndent(origin, "", "  ")
	if err != nil {
		return fmt.Errorf("cannot encode origin metadata: %w", err)
	}
	if err := state.WriteFileAtomic(
		filepath.Join(staged, originMetadataFile),
		append(encodedOrigin, '\n'), 0o600); err != nil {
		return err
	}

	backup := ""
	if _, err := os.Stat(destination); err == nil {
		backup = destination + ".previous"
		_ = os.RemoveAll(backup)
		if err := os.Rename(destination, backup); err != nil {
			return fmt.Errorf("cannot replace %s: %w", destination, err)
		}
	}
	if err := os.Rename(staged, destination); err != nil {
		if backup != "" {
			_ = os.Rename(backup, destination)
		}
		return fmt.Errorf("cannot install catalog at %s: %w", destination, err)
	}
	if backup != "" {
		_ = os.RemoveAll(backup)
	}
	return nil
}

func copyRegularFile(source, target string) error {
	in, err := os.Open(source)
	if err != nil {
		return fmt.Errorf("cannot read %s: %w", source, err)
	}
	defer in.Close()
	out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, fs.FileMode(0o644))
	if err != nil {
		return fmt.Errorf("cannot write %s: %w", target, err)
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return fmt.Errorf("cannot write %s: %w", target, err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("cannot write %s: %w", target, err)
	}
	return nil
}
