package state

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"prison/internal/policy"
)

// changingFiles lists the per-project files the broker watches.
var changingFiles = []string{"token", "grants", "egress-allow", "profile.json"}

// Project is one project's state directory. ID names it, Dir is the
// directory path.
type Project struct {
	ID            string
	Dir           string
	root          *Root
	directoryPath string
}

// BoxName returns the project's box name as `<slug>-<id>.<domain>`.
// Falls back to "project" if the path yields no usable slug.
func (p *Project) BoxName(domain string) string {
	path := p.directoryPath
	if record, err := p.Record(); err == nil && record != nil && record.Path != "" {
		path = record.Path
	}
	slug := Slug(path)
	if slug == "" {
		slug = "project"
	}
	return fmt.Sprintf("%s-%s.%s", slug, p.ID, domain)
}

// RecordFile returns the path to the project's project.json.
func (p *Project) RecordFile() string {
	return filepath.Join(p.Dir, "project.json")
}

// Record reads project.json. Returns nil with no error if absent.
// Returns an error if the file exists but cannot be parsed.
func (p *Project) Record() (*ProjectRecord, error) {
	var record ProjectRecord
	found, err := readJSONFile(p.RecordFile(), &record)
	if err != nil || !found {
		return nil, err
	}
	return &record, nil
}

// SaveRecord writes record to project.json atomically.
func (p *Project) SaveRecord(record *ProjectRecord) error {
	return writeJSONFile(p.RecordFile(), record)
}

// Token returns the project's token, creating one if the file is
// absent or empty.
func (p *Project) Token() (string, error) {
	token, found, err := p.TokenIfPresent()
	if err != nil {
		return "", err
	}
	if found {
		return token, nil
	}
	token, err = NewToken()
	if err != nil {
		return "", err
	}
	if err := WriteFileAtomic(p.tokenFile(), []byte(token+"\n"), fileMode); err != nil {
		return "", err
	}
	return token, nil
}

// TokenIfPresent reads the project's token without creating one. The
// bool is false if the file is absent or blank.
func (p *Project) TokenIfPresent() (string, bool, error) {
	data, err := os.ReadFile(p.tokenFile())
	if err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("cannot read %s: %w", p.tokenFile(), err)
	}
	token := strings.TrimSpace(string(data))
	if token == "" {
		return "", false, nil
	}
	return token, true, nil
}

// tokenFile returns the path to the project's token file.
func (p *Project) tokenFile() string {
	return filepath.Join(p.Dir, "token")
}

// grantsFile returns the path to the project's grants file.
func (p *Project) grantsFile() string {
	return filepath.Join(p.Dir, "grants")
}

// Grants returns the secret names granted to the project. Returns nil
// with no error if the file is absent.
func (p *Project) Grants() ([]string, error) {
	file, err := os.Open(p.grantsFile())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("cannot read %s: %w", p.grantsFile(), err)
	}
	defer file.Close()
	var names []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		names = append(names, line)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("cannot read %s: %w", p.grantsFile(), err)
	}
	return names, nil
}

// SetGrants replaces the grants file with the given names.
func (p *Project) SetGrants(names []string) error {
	var builder strings.Builder
	for _, name := range names {
		builder.WriteString(name)
		builder.WriteByte('\n')
	}
	return WriteFileAtomic(p.grantsFile(), []byte(builder.String()), fileMode)
}

// EgressAllowFile returns the path to the project's egress allowlist.
func (p *Project) EgressAllowFile() string {
	return filepath.Join(p.Dir, "egress-allow")
}

// EgressAllow parses the project's egress allowlist. Returns nil if
// the file is absent.
func (p *Project) EgressAllow() ([]policy.Pattern, error) {
	return readAllowFile(p.EgressAllowFile())
}

// ProfileFile returns the path to profile.json.
func (p *Project) ProfileFile() string {
	return filepath.Join(p.Dir, "profile.json")
}

// Profile reads profile.json. Returns nil with no error if absent.
func (p *Project) Profile() (*Profile, error) {
	var profile Profile
	found, err := readJSONFile(p.ProfileFile(), &profile)
	if err != nil || !found {
		return nil, err
	}
	return &profile, nil
}

// SaveProfile writes profile to profile.json atomically.
func (p *Project) SaveProfile(profile *Profile) error {
	return writeJSONFile(p.ProfileFile(), profile)
}

// TrustedConfigCopy returns the path to the approved copy of
// prison.toml.
func (p *Project) TrustedConfigCopy() string {
	return filepath.Join(p.Dir, "config-trusted.toml")
}

// HomesDir returns the path to the persisted inmate home directories.
func (p *Project) HomesDir() string {
	return filepath.Join(p.Dir, "homes")
}

// HomeDir returns the persisted home directory for the given inmate.
func (p *Project) HomeDir(inmate string) string {
	return filepath.Join(p.HomesDir(), inmate)
}

// ShadowDir returns the shadow directory for relativePath.
func (p *Project) ShadowDir(relativePath string) string {
	flattened := strings.ReplaceAll(relativePath, "/", "_")
	return filepath.Join(p.Dir, "shadow", flattened)
}

// EmptyDir returns the path to the empty directory used for hiding
// paths from the box.
func (p *Project) EmptyDir() string {
	return filepath.Join(p.Dir, "empty")
}

// CheckpointsDir returns the path to the project's checkpoint store.
func (p *Project) CheckpointsDir() string {
	return filepath.Join(p.Dir, "checkpoints")
}

// Size returns the total bytes of regular files under the state
// directory. Unreadable entries are skipped.
func (p *Project) Size() (int64, error) {
	var total int64
	err := filepath.WalkDir(p.Dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return nil
		}
		total += info.Size()
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("cannot measure %s: %w", p.Dir, err)
	}
	return total, nil
}

// Remove deletes the project's state directory. Refuses if the id or
// directory does not match the expected layout.
func (p *Project) Remove() error {
	if !IsProjectID(p.ID) || p.root == nil {
		return fmt.Errorf("refusing to remove %s: it is not a project state directory", p.Dir)
	}
	if filepath.Dir(p.Dir) != p.root.ProjectsDir() || filepath.Base(p.Dir) != p.ID {
		return fmt.Errorf("refusing to remove %s: it is not inside %s", p.Dir, p.root.ProjectsDir())
	}
	if err := os.RemoveAll(p.Dir); err != nil {
		return fmt.Errorf("cannot remove %s: %w", p.Dir, err)
	}
	return nil
}

// ModTimes returns the modification time of each watched file, keyed
// by name. Absent files are omitted.
func (p *Project) ModTimes() (map[string]time.Time, error) {
	times := map[string]time.Time{}
	for _, name := range changingFiles {
		path := filepath.Join(p.Dir, name)
		info, err := os.Stat(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("cannot stat %s: %w", path, err)
		}
		times[name] = info.ModTime()
	}
	return times, nil
}

// readJSONFile decodes JSON from path into value. Returns false if
// the file does not exist.
func readJSONFile(path string, value any) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("cannot read %s: %w", path, err)
	}
	if err := json.Unmarshal(data, value); err != nil {
		return false, fmt.Errorf("%s is not the JSON prison writes: %w", path, err)
	}
	return true, nil
}

// writeJSONFile writes value to path as indented JSON, atomically.
func writeJSONFile(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("cannot encode %s: %w", path, err)
	}
	return WriteFileAtomic(path, append(data, '\n'), fileMode)
}
