// Package state reads and writes the prison state root, usually
// `~/.prison`. The CLI and the broker both use it.
package state

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"prison/internal/policy"
)

const directoryMode os.FileMode = 0o700

const fileMode os.FileMode = 0o600

const projectIDLength = 12

const slugLength = 40

type Root struct {
	Path string
}

func OpenRoot(path string) (*Root, error) {
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("state root %q is not valid: %w", path, err)
	}
	root := &Root{Path: filepath.Clean(absolutePath)}
	directories := []string{
		root.Path,
		root.ProjectsDir(),
		root.InmatesDir(),
		root.TrustDir(),
	}
	for _, directory := range directories {
		if err := os.MkdirAll(directory, directoryMode); err != nil {
			return nil, fmt.Errorf("cannot create %s: %w", directory, err)
		}
	}
	return root, nil
}

func (r *Root) IsolatorDir() string {
	return filepath.Join(r.Path, "isolator")
}

func (r *Root) ConfigFile() string {
	return filepath.Join(r.Path, "config.toml")
}

func (r *Root) EgressAllowFile() string {
	return filepath.Join(r.Path, "egress-allow")
}

func (r *Root) VaultFile() string {
	return filepath.Join(r.Path, "secrets.vault")
}

func (r *Root) BrokerSocket() string {
	return filepath.Join(r.Path, "broker.sock")
}

func (r *Root) BrokerLog() string {
	return filepath.Join(r.Path, "broker.log")
}

func (r *Root) BrokerPIDFile() string {
	return filepath.Join(r.Path, "broker.pid")
}

func (r *Root) InmatesDir() string {
	return filepath.Join(r.Path, "inmates")
}

func (r *Root) TrustDir() string {
	return filepath.Join(r.Path, "trust")
}

func (r *Root) ProjectsDir() string {
	return filepath.Join(r.Path, "projects")
}

func (r *Root) FloorAllowList() ([]policy.Pattern, error) {
	patterns, err := readAllowFile(r.EgressAllowFile())
	if err != nil {
		return nil, err
	}
	if len(patterns) == 0 {
		return nil, nil
	}
	return patterns, nil
}

func readAllowFile(path string) ([]policy.Pattern, error) {
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("cannot read %s: %w", path, err)
	}
	defer file.Close()
	patterns, err := policy.ParseList(file)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return patterns, nil
}

func (r *Root) Project(projectDirectory string) (*Project, error) {
	realPath, err := filepath.EvalSymlinks(projectDirectory)
	if err != nil {
		return nil, fmt.Errorf("cannot resolve %s: %w", projectDirectory, err)
	}
	realPath = filepath.Clean(realPath)
	if err := refuseUnjailablePath(realPath); err != nil {
		return nil, err
	}
	project := &Project{
		ID:            ProjectID(realPath),
		root:          r,
		directoryPath: realPath,
	}
	project.Dir = filepath.Join(r.ProjectsDir(), project.ID)
	subdirectories := []string{
		project.Dir,
		project.HomesDir(),
		filepath.Join(project.Dir, "shadow"),
		project.EmptyDir(),
		project.CheckpointsDir(),
	}
	for _, directory := range subdirectories {
		if err := os.MkdirAll(directory, directoryMode); err != nil {
			return nil, fmt.Errorf("cannot create %s: %w", directory, err)
		}
	}
	return project, nil
}

func refuseUnjailablePath(realPath string) error {
	if realPath == string(filepath.Separator) {
		return fmt.Errorf("cannot use /. Run prison in a project directory")
	}
	for _, home := range homeDirectoryForms() {
		if home != "" && realPath == home {
			return fmt.Errorf("cannot use the home directory %s. "+
				"Run prison in a project directory", realPath)
		}
	}
	return nil
}

func homeDirectoryForms() []string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return nil
	}
	home = filepath.Clean(home)
	forms := []string{home}
	if resolved, err := filepath.EvalSymlinks(home); err == nil {
		if cleaned := filepath.Clean(resolved); cleaned != home {
			forms = append(forms, cleaned)
		}
	}
	return forms
}

func (r *Root) ProjectByID(id string) (*Project, error) {
	if !IsProjectID(id) {
		return nil, fmt.Errorf(
			"%q is not a project id. An id is 12 hex characters", id)
	}
	directory := filepath.Join(r.ProjectsDir(), id)
	info, err := os.Stat(directory)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("unknown project %s. "+
			"Run `prison list --state` to see the projects", id)
	}
	return &Project{ID: id, Dir: directory, root: r}, nil
}

func (r *Root) ListProjects() ([]*Project, error) {
	entries, err := os.ReadDir(r.ProjectsDir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("cannot read %s: %w", r.ProjectsDir(), err)
	}
	var projects []*Project
	for _, entry := range entries {
		if !entry.IsDir() || !IsProjectID(entry.Name()) {
			continue
		}
		projects = append(projects, &Project{
			ID:   entry.Name(),
			Dir:  filepath.Join(r.ProjectsDir(), entry.Name()),
			root: r,
		})
	}
	return projects, nil
}

// LegacyLayoutDetected reports whether the root holds the old layout. That
// layout kept the project directories directly under the root.
func (r *Root) LegacyLayoutDetected() bool {
	if _, err := os.Stat(filepath.Join(r.Path, "auth-routes.json")); err == nil {
		return true
	}
	entries, err := os.ReadDir(r.Path)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if entry.IsDir() && IsProjectID(entry.Name()) {
			return true
		}
	}
	return false
}

func (r *Root) ClaimedPortBlocks() (map[string]int, error) {
	projects, err := r.ListProjects()
	if err != nil {
		return nil, err
	}
	blocks := map[string]int{}
	for _, project := range projects {
		record, err := project.Record()
		if err != nil {
			return nil, err
		}
		if record == nil || record.PortBlock == 0 {
			continue
		}
		blocks[project.ID] = record.PortBlock
	}
	return blocks, nil
}

func (r *Root) BrokerPID() (int, bool) {
	data, err := os.ReadFile(r.BrokerPIDFile())
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return 0, false
	}
	return pid, true
}

// SaveBrokerPID is called only by the broker.
func (r *Root) SaveBrokerPID(pid int) error {
	line := strconv.Itoa(pid) + "\n"
	return WriteFileAtomic(r.BrokerPIDFile(), []byte(line), fileMode)
}

func ProjectID(realPath string) string {
	sum := sha256.Sum256([]byte(realPath))
	return hex.EncodeToString(sum[:])[:projectIDLength]
}

func IsProjectID(text string) bool {
	if len(text) != projectIDLength {
		return false
	}
	for i := 0; i < len(text); i++ {
		c := text[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func Slug(realPath string) string {
	base := filepath.Base(realPath)
	lowered := make([]byte, 0, len(base))
	for i := 0; i < len(base); i++ {
		c := base[i]
		switch {
		case c >= 'A' && c <= 'Z':
			lowered = append(lowered, c+('a'-'A'))
		case (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9'):
			lowered = append(lowered, c)
		default:
			lowered = append(lowered, '-')
		}
	}
	if len(lowered) > slugLength {
		lowered = lowered[:slugLength]
	}
	return strings.Trim(string(lowered), "-")
}

func NewToken() (string, error) {
	buffer := make([]byte, 32)
	if _, err := rand.Read(buffer); err != nil {
		return "", fmt.Errorf("cannot make a token: %w", err)
	}
	return hex.EncodeToString(buffer), nil
}

func WriteFileAtomic(path string, data []byte, mode os.FileMode) error {
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, "."+filepath.Base(path)+".*")
	if err != nil {
		return fmt.Errorf("cannot write %s: %w", path, err)
	}
	temporaryPath := temporary.Name()
	if err := temporary.Chmod(mode); err != nil {
		temporary.Close()
		os.Remove(temporaryPath)
		return fmt.Errorf("cannot set the mode of %s: %w", path, err)
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		os.Remove(temporaryPath)
		return fmt.Errorf("cannot write %s: %w", path, err)
	}
	if err := temporary.Close(); err != nil {
		os.Remove(temporaryPath)
		return fmt.Errorf("cannot write %s: %w", path, err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		os.Remove(temporaryPath)
		return fmt.Errorf("cannot replace %s: %w", path, err)
	}
	return nil
}
