package checkpoint

import (
	"cmp"
	"fmt"
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
	identifier, err := store.Resolve(wanted)
	if err != nil {
		return RestoreResult{}, err
	}
	automatic, err := store.Take(
		fmt.Sprintf("automatic, before restoring %s", identifier))
	if err != nil {
		return RestoreResult{}, err
	}
	result, err := store.RestoreWithoutAutomatic(identifier)
	result.Automatic = automatic.ID
	return result, err
}

// RestoreWithoutAutomatic serves `back` and `forward`.
func (store *Store) RestoreWithoutAutomatic(
	wanted string) (RestoreResult, error) {
	identifier, err := store.Resolve(wanted)
	if err != nil {
		return RestoreResult{}, err
	}
	recorded, err := store.Manifest(identifier)
	if err != nil {
		return RestoreResult{}, err
	}
	present, _, err := store.Scan()
	if err != nil {
		return RestoreResult{}, err
	}

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
		path := filepath.Join(store.Project, obsolete[index])
		if err := removePath(path); err != nil {
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
		if err := WriteEntry(store.Project, entry, content); err != nil {
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

func WriteEntry(projectDirectory string, entry Entry, content []byte) error {
	absolute := filepath.Join(projectDirectory, entry.Path)
	if entry.Kind == KindDirectory {
		if information, err := os.Lstat(absolute); err == nil &&
			!information.IsDir() {
			if err := removePath(absolute); err != nil {
				return fmt.Errorf("cannot replace %s: %w", entry.Path, err)
			}
		}
		if err := os.MkdirAll(absolute, 0o755); err != nil {
			return fmt.Errorf("cannot create %s: %w", entry.Path, err)
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(absolute), 0o755); err != nil {
		return fmt.Errorf("cannot create the parent directory of %s: %w",
			entry.Path, err)
	}

	if entry.Kind == KindLink {
		if err := removePath(absolute); err != nil {
			return fmt.Errorf("cannot replace %s: %w", entry.Path, err)
		}
		if err := os.Symlink(string(content), absolute); err != nil {
			return fmt.Errorf("cannot create the link %s: %w",
				entry.Path, err)
		}
		return nil
	}

	mode := entry.Mode.Perm()
	if mode == 0 {
		mode = 0o644
	}
	if information, err := os.Lstat(absolute); err == nil &&
		!information.Mode().IsRegular() {
		if err := removePath(absolute); err != nil {
			return fmt.Errorf("cannot replace %s: %w", entry.Path, err)
		}
	}
	temporary := absolute + restoreTemporarySuffix
	if err := os.WriteFile(temporary, content, mode); err != nil {
		return fmt.Errorf("cannot write %s: %w", entry.Path, err)
	}
	if err := os.Chmod(temporary, mode); err != nil {
		os.Remove(temporary)
		return fmt.Errorf("cannot set the permissions of %s: %w",
			entry.Path, err)
	}
	if err := os.Rename(temporary, absolute); err != nil {
		os.Remove(temporary)
		return fmt.Errorf("cannot write %s: %w", entry.Path, err)
	}
	return nil
}

func removePath(absolute string) error {
	information, err := os.Lstat(absolute)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if information.IsDir() {
		return os.RemoveAll(absolute)
	}
	return os.Remove(absolute)
}
