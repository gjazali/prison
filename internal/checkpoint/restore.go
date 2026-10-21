package checkpoint

import (
	"cmp"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
)

// restoreTemporarySuffix is in `DefaultIgnores`, so scans skip these files.
const restoreTemporarySuffix = ".prison-partial"

type RestoreResult struct {
	Automatic string
	Written   int
	Removed   int
}

func (store *Store) Restore(wanted string) (RestoreResult, error) {
	identifier, recorded, err := store.restorable(wanted)
	if err != nil {
		return RestoreResult{}, err
	}
	automatic, err := store.Take(
		fmt.Sprintf("automatic, before restoring %s", identifier))
	if err != nil {
		return RestoreResult{}, err
	}
	result, err := store.restore(identifier, recorded)
	result.Automatic = automatic.ID
	return result, err
}

// RestoreWithoutAutomatic serves `back` and `forward`.
func (store *Store) RestoreWithoutAutomatic(
	wanted string) (RestoreResult, error) {
	identifier, recorded, err := store.restorable(wanted)
	if err != nil {
		return RestoreResult{}, err
	}
	head, err := store.DiffTarget("")
	if err != nil {
		return RestoreResult{}, err
	}
	changes, _, _, _, err := store.Diff(head)
	if err != nil {
		return RestoreResult{}, err
	}
	if !changes.Empty() {
		return RestoreResult{}, fmt.Errorf(
			"the project changed since checkpoint %s. "+
				"Run `prison checkpoint` first", head)
	}
	return store.restore(identifier, recorded)
}

// restorable runs before any removal so that a restore will not stop
// halfway.
func (store *Store) restorable(wanted string) (string, Manifest, error) {
	identifier, err := store.Resolve(wanted)
	if err != nil {
		return "", nil, err
	}
	recorded, err := store.Manifest(identifier)
	if err != nil {
		return "", nil, err
	}
	for _, entry := range recorded {
		if entry.Kind == KindDirectory {
			continue
		}
		if _, err := os.Lstat(store.BlobPath(entry.Digest)); err != nil {
			return "", nil, fmt.Errorf(
				"the stored contents of %s are missing", entry.Path)
		}
	}
	return identifier, recorded, nil
}

func (store *Store) restore(
	identifier string, recorded Manifest) (RestoreResult, error) {
	present, _, err := store.Scan()
	if err != nil {
		return RestoreResult{}, err
	}
	project, err := store.openProject()
	if err != nil {
		return RestoreResult{}, err
	}
	defer project.Close()

	var result RestoreResult
	keep := recorded.Index()
	obsolete := make([]string, 0, len(present))
	for _, entry := range present {
		if _, wanted := keep[entry.Path]; !wanted {
			obsolete = append(obsolete, entry.Path)
		}
	}
	slices.Sort(obsolete)
	for index := len(obsolete) - 1; index >= 0; index-- {
		err := removePath(project, obsolete[index])
		if errors.Is(err, errDirectoryNotEmpty) {
			continue
		}
		if err != nil {
			return result, fmt.Errorf("cannot remove %s: %w",
				obsolete[index], err)
		}
		result.Removed++
	}

	ordered := slices.Clone(recorded)
	slices.SortFunc(ordered, func(left, right Entry) int {
		if left.Kind != right.Kind {
			if left.Kind == KindDirectory {
				return -1
			}
			if right.Kind == KindDirectory {
				return 1
			}
		}
		return cmp.Compare(left.Path, right.Path)
	})
	for _, entry := range ordered {
		content, err := store.entryContent(entry)
		if err != nil {
			return result, err
		}
		if err := WriteEntry(project, entry, content); err != nil {
			return result, err
		}
		result.Written++
	}

	if err := store.SetHead(identifier); err != nil {
		return result, err
	}
	return result, nil
}

func (store *Store) entryContent(entry Entry) ([]byte, error) {
	if entry.Kind == KindDirectory {
		return nil, nil
	}
	content, err := store.Blob(entry.Digest)
	if err != nil {
		return nil, fmt.Errorf("the stored contents of %s are missing",
			entry.Path)
	}
	return content, nil
}

func (store *Store) Step(direction int) (string, error) {
	identifiers, err := store.identifiers()
	if err != nil {
		return "", err
	}
	if len(identifiers) == 0 {
		return "", fmt.Errorf("no checkpoints for this project. " +
			"Run `prison checkpoint` to take one")
	}
	position := len(identifiers) - 1
	if head, ok := store.Head(); ok {
		if found := slices.Index(identifiers, head); found >= 0 {
			position = found
		}
	}
	target := position + direction
	if target < 0 {
		return "", fmt.Errorf(
			"checkpoint %s is the earliest one",
			identifiers[position])
	}
	if target >= len(identifiers) {
		return "", fmt.Errorf(
			"checkpoint %s is the latest one",
			identifiers[position])
	}
	return identifiers[target], nil
}

func WriteEntry(project *os.Root, entry Entry, content []byte) error {
	name := filepath.FromSlash(entry.Path)
	if entry.Kind == KindDirectory {
		if information, err := project.Lstat(name); err == nil &&
			!information.IsDir() {
			if err := removePath(project, name); err != nil {
				return fmt.Errorf("cannot replace %s: %w", entry.Path, err)
			}
		}
		if err := project.MkdirAll(name, 0o755); err != nil {
			return fmt.Errorf("cannot create %s: %w", entry.Path, err)
		}
		return nil
	}
	if err := project.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		return fmt.Errorf("cannot create the parent directory of %s: %w",
			entry.Path, err)
	}

	if entry.Kind == KindLink {
		if err := removePath(project, name); err != nil {
			return fmt.Errorf("cannot replace %s: %w", entry.Path, err)
		}
		if err := project.Symlink(string(content), name); err != nil {
			return fmt.Errorf("cannot create the link %s: %w",
				entry.Path, err)
		}
		return nil
	}

	mode := entry.Mode.Perm()
	if mode == 0 {
		mode = 0o644
	}
	if information, err := project.Lstat(name); err == nil &&
		!information.Mode().IsRegular() {
		if err := removePath(project, name); err != nil {
			return fmt.Errorf("cannot replace %s: %w", entry.Path, err)
		}
	}
	temporary := name + restoreTemporarySuffix
	// The agent can leave a link at the temporary path.
	project.Remove(temporary)
	if err := project.WriteFile(temporary, content, mode); err != nil {
		return fmt.Errorf("cannot write %s: %w", entry.Path, err)
	}
	if err := project.Chmod(temporary, mode); err != nil {
		project.Remove(temporary)
		return fmt.Errorf("cannot set the permissions of %s: %w",
			entry.Path, err)
	}
	if err := project.Rename(temporary, name); err != nil {
		project.Remove(temporary)
		return fmt.Errorf("cannot write %s: %w", entry.Path, err)
	}
	return nil
}

var errDirectoryNotEmpty = errors.New("the directory is not empty")

// removePath will keep a directory that is not empty because it can hold
// ignored files.
func removePath(project *os.Root, name string) error {
	information, err := project.Lstat(name)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	err = project.Remove(name)
	if err != nil && information.IsDir() {
		if children, readErr := fs.ReadDir(project.FS(),
			filepath.ToSlash(name)); readErr == nil && len(children) > 0 {
			return errDirectoryNotEmpty
		}
	}
	return err
}
