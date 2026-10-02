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

// changingFiles lists the per-project files that the broker watches.
var changingFiles = []string{"token", "grants", "egress-allow", "profile.json"}

type Project struct {
	ID            string
	Dir           string
	root          *Root
	directoryPath string
}

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

func (p *Project) RecordFile() string {
	return filepath.Join(p.Dir, "project.json")
}

func (p *Project) Record() (*ProjectRecord, error) {
	var record ProjectRecord
	found, err := readJSONFile(p.RecordFile(), &record)
	if err != nil || !found {
		return nil, err
	}
	return &record, nil
}

func (p *Project) SaveRecord(record *ProjectRecord) error {
	return writeJSONFile(p.RecordFile(), record)
}

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

func (p *Project) tokenFile() string {
	return filepath.Join(p.Dir, "token")
}

func (p *Project) grantsFile() string {
	return filepath.Join(p.Dir, "grants")
}

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

func (p *Project) SetGrants(names []string) error {
	var builder strings.Builder
	for _, name := range names {
		builder.WriteString(name)
		builder.WriteByte('\n')
	}
	return WriteFileAtomic(p.grantsFile(), []byte(builder.String()), fileMode)
}

func (p *Project) EgressAllowFile() string {
	return filepath.Join(p.Dir, "egress-allow")
}

func (p *Project) EgressAllow() ([]policy.Pattern, error) {
	return readAllowFile(p.EgressAllowFile())
}

func (p *Project) ProfileFile() string {
	return filepath.Join(p.Dir, "profile.json")
}

func (p *Project) Profile() (*Profile, error) {
	var profile Profile
	found, err := readJSONFile(p.ProfileFile(), &profile)
	if err != nil || !found {
		return nil, err
	}
	return &profile, nil
}

func (p *Project) SaveProfile(profile *Profile) error {
	return writeJSONFile(p.ProfileFile(), profile)
}

// TrustedConfigCopy returns the path to the approved copy of `prison.toml`.
func (p *Project) TrustedConfigCopy() string {
	return filepath.Join(p.Dir, "config-trusted.toml")
}

func (p *Project) HomesDir() string {
	return filepath.Join(p.Dir, "homes")
}

func (p *Project) HomeDir(inmate string) string {
	return filepath.Join(p.HomesDir(), inmate)
}

func (p *Project) ShadowDir(relativePath string) string {
	flattened := strings.ReplaceAll(relativePath, "/", "_")
	return filepath.Join(p.Dir, "shadow", flattened)
}

// EmptyDir returns an empty directory that hides paths from the box.
func (p *Project) EmptyDir() string {
	return filepath.Join(p.Dir, "empty")
}

func (p *Project) CheckpointsDir() string {
	return filepath.Join(p.Dir, "checkpoints")
}

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

func (p *Project) Remove() error {
	if !IsProjectID(p.ID) || p.root == nil {
		return fmt.Errorf(
			"cannot remove %s. It is not a project state directory", p.Dir)
	}
	if filepath.Dir(p.Dir) != p.root.ProjectsDir() || filepath.Base(p.Dir) != p.ID {
		return fmt.Errorf("cannot remove %s. It is not in %s",
			p.Dir, p.root.ProjectsDir())
	}
	if err := os.RemoveAll(p.Dir); err != nil {
		return fmt.Errorf("cannot remove %s: %w", p.Dir, err)
	}
	return nil
}

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

func readJSONFile(path string, value any) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("cannot read %s: %w", path, err)
	}
	if err := json.Unmarshal(data, value); err != nil {
		return false, fmt.Errorf("%s is not valid JSON: %w", path, err)
	}
	return true, nil
}

func writeJSONFile(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("cannot encode %s: %w", path, err)
	}
	return WriteFileAtomic(path, append(data, '\n'), fileMode)
}
