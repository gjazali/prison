// Package state reads and writes ~/.prison. It owns the on-disk layout,
// project directories, path-derived identifiers, and atomic writes.
// Both the CLI and the broker use it.
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

// directoryMode is the permission mode for directories under the state root.
const directoryMode os.FileMode = 0o700

// fileMode is the permission mode for files this package writes.
const fileMode os.FileMode = 0o600

// projectIDLength is the number of hex characters in a project id.
const projectIDLength = 12

// slugLength is the maximum character count for a path slug.
const slugLength = 40

// Root is an opened state root, usually ~/.prison. Path is absolute
// and clean.
type Root struct {
	Path string
}

// OpenRoot opens the state root at path, creating it and its
// subdirectories if missing. Returns an error if the path cannot be
// made absolute or a directory cannot be created.
func OpenRoot(path string) (*Root, error) {
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("the state root %q is not a usable path: %w", path, err)
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

// ConfigFile returns the path to the host configuration, config.toml.
func (r *Root) ConfigFile() string {
	return filepath.Join(r.Path, "config.toml")
}

// EgressAllowFile returns the path to the host-wide egress allowlist.
func (r *Root) EgressAllowFile() string {
	return filepath.Join(r.Path, "egress-allow")
}

// VaultFile returns the path to the encrypted secret store.
func (r *Root) VaultFile() string {
	return filepath.Join(r.Path, "secrets.vault")
}

// BrokerSocket returns the path to the broker's Unix socket.
func (r *Root) BrokerSocket() string {
	return filepath.Join(r.Path, "broker.sock")
}

// BrokerLog returns the path to the broker's log file.
func (r *Root) BrokerLog() string {
	return filepath.Join(r.Path, "broker.log")
}

// BrokerPIDFile returns the path to the broker's PID file.
func (r *Root) BrokerPIDFile() string {
	return filepath.Join(r.Path, "broker.pid")
}

// InmatesDir returns the path to the installed inmate plugins directory.
func (r *Root) InmatesDir() string {
	return filepath.Join(r.Path, "inmates")
}

// TrustDir returns the path to the trust records directory.
func (r *Root) TrustDir() string {
	return filepath.Join(r.Path, "trust")
}

// ProjectsDir returns the path to the per-project state directories.
func (r *Root) ProjectsDir() string {
	return filepath.Join(r.Path, "projects")
}

// FloorAllowList parses the host-wide egress-allow file. Returns nil
// with no error when the file is absent or empty. Returns an error if
// a pattern fails to parse.
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

// readAllowFile parses one allowlist file. Returns nil with no error
// when the file does not exist.
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

// Project opens the state for the project at projectDirectory. It
// resolves symlinks, rejects the home directory and filesystem root,
// and creates the state directory and its subdirectories.
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

// refuseUnjailablePath returns an error if realPath is the filesystem
// root or the home directory.
func refuseUnjailablePath(realPath string) error {
	if realPath == string(filepath.Separator) {
		return fmt.Errorf("refusing to jail /; run prison from a project directory")
	}
	for _, home := range homeDirectoryForms() {
		if home != "" && realPath == home {
			return fmt.Errorf("refusing to jail your home directory %s; run prison from a project directory", realPath)
		}
	}
	return nil
}

// homeDirectoryForms returns the home directory in both its literal and
// symlink-resolved forms.
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

// ProjectByID opens the state directory for the given id. Returns an
// error if the id is not valid or the directory does not exist.
func (r *Root) ProjectByID(id string) (*Project, error) {
	if !IsProjectID(id) {
		return nil, fmt.Errorf("%q is not a project id; ids are twelve hex characters", id)
	}
	directory := filepath.Join(r.ProjectsDir(), id)
	info, err := os.Stat(directory)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("no project %s is known; `prison list --state` shows what exists", id)
	}
	return &Project{ID: id, Dir: directory, root: r}, nil
}

// ListProjects returns all projects in directory order. Returns an
// error only if the projects directory cannot be read.
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

// LegacyLayoutDetected reports whether the root holds the old layout
// where project directories lived directly under it.
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

// ClaimedPortBlocks returns a map from project id to claimed first host
// port. Projects with no port block are skipped. An unreadable record
// is an error, not a skip.
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

// BrokerPID reads the recorded broker process id. The bool is false
// when the file is absent or invalid.
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

// SaveBrokerPID writes the broker's process id to the PID file. Only
// the broker calls this.
func (r *Root) SaveBrokerPID(pid int) error {
	line := strconv.Itoa(pid) + "\n"
	return WriteFileAtomic(r.BrokerPIDFile(), []byte(line), fileMode)
}

// ProjectID returns the first twelve hex characters of the SHA-256 of
// realPath.
func ProjectID(realPath string) string {
	sum := sha256.Sum256([]byte(realPath))
	return hex.EncodeToString(sum[:])[:projectIDLength]
}

// IsProjectID reports whether text is twelve lowercase hex characters.
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

// Slug returns a short lowercase label from the last element of
// realPath. Can return empty.
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

// NewToken returns a new random token as sixty-four hex characters.
func NewToken() (string, error) {
	buffer := make([]byte, 32)
	if _, err := rand.Read(buffer); err != nil {
		return "", fmt.Errorf("cannot read random bytes for a token: %w", err)
	}
	return hex.EncodeToString(buffer), nil
}

// WriteFileAtomic writes data to path atomically using a temp file and
// rename. The file ends up with the given mode.
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
