package checkpoint

import (
	"cmp"
	"fmt"
	"os"
	"path/filepath"
	"slices"
)

// restoreTemporarySuffix is the suffix for temporary files during a
// restore. Listed in DefaultIgnores so scans skip them.
const restoreTemporarySuffix = ".prison-partial"

// RestoreResult holds the outcome of a restore. Automatic is the
// identifier of the checkpoint taken before restoring.
type RestoreResult struct {
	Automatic string
	Written   int
	Removed   int
}

// Restore puts the working tree back to a checkpoint. Records the
// current tree first. Takes an identifier in short or padded form.
// Returns the automatic checkpoint's identifier in the result.
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

// RestoreWithoutAutomatic puts the working tree back to a checkpoint
// without recording the current one first. Used by `back` and
// `forward`. Takes an identifier and moves HEAD to it.
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

// entryContent returns the stored bytes for an entry. Returns nil for
// a directory. A missing object is an error.
func (store *Store) entryContent(entry Entry) ([]byte, error) {
	if entry.Kind == KindDirectory {
		return nil, nil
	}
	content, err := store.Blob(entry.Digest)
	if err != nil {
		return nil, fmt.Errorf("the stored contents of %s are missing from "+
			"the object store, so this checkpoint cannot be restored in "+
			"full", entry.Path)
	}
	return content, nil
}

// Step returns the checkpoint one step before or after the current one.
// Takes a negative direction for back and positive for forward.
// Counts from HEAD, or from the newest checkpoint when HEAD is unset.
func (store *Store) Step(direction int) (string, error) {
	identifiers, err := store.identifiers()
	if err != nil {
		return "", err
	}
	if len(identifiers) == 0 {
		return "", fmt.Errorf(
			"no checkpoints for this project yet; `prison checkpoint` " +
				"takes one")
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
			"checkpoint %s is the earliest one; there is nothing before it",
			identifiers[position])
	}
	if target >= len(identifiers) {
		return "", fmt.Errorf(
			"checkpoint %s is the latest one; there is nothing after it",
			identifiers[position])
	}
	return identifiers[target], nil
}

// WriteEntry puts one recorded entry into a working tree. Takes the
// project directory, the entry, and its stored contents, which are the
// target string for a link and are ignored for a directory. A file is
// written through a temporary name and renamed over whatever was there,
// with its recorded permissions or 0644 when it has none.
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
		return fmt.Errorf("cannot create the directory holding %s: %w",
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

// removePath deletes a file, link, or directory tree. A path that is
// not there is not an error, and a directory that is not empty goes
// with everything under it.
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
