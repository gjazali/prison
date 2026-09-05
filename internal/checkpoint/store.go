package checkpoint

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// On-disk names used by the store format.
const (
	objectsDirectory   = "objects"
	snapshotsDirectory = "snapshots"
	headFile           = "HEAD"
	manifestSuffix     = ".manifest"
	metadataSuffix     = ".meta"
)

// Store is one project's checkpoint store on disk.
type Store struct {
	Dir     string
	Project string
	Ignore  *Ignore
}

// Open returns a store rooted at dir for the given project directory.
// Creates subdirectories if missing. A nil ignore excludes nothing.
func Open(dir, projectDirectory string, ignore *Ignore) (*Store, error) {
	store := &Store{Dir: dir, Project: projectDirectory, Ignore: ignore}
	for _, name := range []string{objectsDirectory, snapshotsDirectory} {
		if err := os.MkdirAll(filepath.Join(dir, name), 0o755); err != nil {
			return nil, fmt.Errorf("cannot create the checkpoint store "+
				"under %s: %w", dir, err)
		}
	}
	return store, nil
}

// formatCheckpointID formats a number as a zero-padded four-digit
// string.
func formatCheckpointID(number int) string {
	return fmt.Sprintf("%04d", number)
}

// snapshotPath returns the path to a checkpoint's manifest or metadata
// file.
func (store *Store) snapshotPath(identifier, suffix string) string {
	return filepath.Join(
		store.Dir, snapshotsDirectory, identifier+suffix)
}

// BlobPath returns the object store path for the given digest.
func (store *Store) BlobPath(digest string) string {
	if len(digest) < 3 {
		return filepath.Join(store.Dir, objectsDirectory, digest)
	}
	return filepath.Join(
		store.Dir, objectsDirectory, digest[:2], digest[2:])
}

// Blob returns the stored contents for the given digest.
func (store *Store) Blob(digest string) ([]byte, error) {
	data, err := os.ReadFile(store.BlobPath(digest))
	if err != nil {
		return nil, fmt.Errorf("cannot read stored object %s: %w",
			digest, err)
	}
	return data, nil
}

// identifiers returns all checkpoint identifiers in numeric order.
func (store *Store) identifiers() ([]string, error) {
	children, err := os.ReadDir(filepath.Join(store.Dir, snapshotsDirectory))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("cannot read the checkpoint store: %w", err)
	}
	var numbers []int
	for _, child := range children {
		name := child.Name()
		if !strings.HasSuffix(name, manifestSuffix) {
			continue
		}
		number, err := strconv.Atoi(strings.TrimSuffix(name, manifestSuffix))
		if err != nil {
			continue
		}
		numbers = append(numbers, number)
	}
	slices.Sort(numbers)
	identifiers := make([]string, 0, len(numbers))
	for _, number := range numbers {
		identifiers = append(identifiers, formatCheckpointID(number))
	}
	return identifiers, nil
}

// List returns all checkpoints in numeric order with metadata.
// Missing metadata yields a zero time and no label.
func (store *Store) List() ([]Checkpoint, error) {
	identifiers, err := store.identifiers()
	if err != nil {
		return nil, err
	}
	checkpoints := make([]Checkpoint, 0, len(identifiers))
	for _, identifier := range identifiers {
		metadata, _ := store.Metadata(identifier)
		checkpoints = append(checkpoints,
			Checkpoint{ID: identifier, Metadata: metadata})
	}
	return checkpoints, nil
}

// Head returns the current checkpoint identifier and whether one is
// set.
func (store *Store) Head() (string, bool) {
	data, err := os.ReadFile(filepath.Join(store.Dir, headFile))
	if err != nil {
		return "", false
	}
	identifier := strings.TrimSpace(string(data))
	return identifier, identifier != ""
}

// SetHead records the given checkpoint as the current one.
func (store *Store) SetHead(identifier string) error {
	path := filepath.Join(store.Dir, headFile)
	if err := os.WriteFile(path, []byte(identifier+"\n"), 0o644); err != nil {
		return fmt.Errorf("cannot record the current checkpoint: %w", err)
	}
	return nil
}

// clearHead removes the HEAD marker. A missing marker is not an
// error.
func (store *Store) clearHead() error {
	err := os.Remove(filepath.Join(store.Dir, headFile))
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("cannot clear the current checkpoint: %w", err)
	}
	return nil
}

// Resolve turns user input into a stored checkpoint identifier.
// Accepts both short and padded forms. Returns an error listing
// available checkpoints if not found.
func (store *Store) Resolve(wanted string) (string, error) {
	identifiers, err := store.identifiers()
	if err != nil {
		return "", err
	}
	if len(identifiers) == 0 {
		return "", fmt.Errorf(
			"no checkpoints for this project yet; `prison checkpoint` " +
				"takes one")
	}
	candidates := []string{strings.TrimSpace(wanted)}
	if number, err := strconv.Atoi(candidates[0]); err == nil {
		candidates = append(candidates, formatCheckpointID(number))
	}
	for _, candidate := range candidates {
		if slices.Contains(identifiers, candidate) {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("no checkpoint %s; this project has %s",
		wanted, strings.Join(identifiers, ", "))
}

// Manifest returns the file tree of a checkpoint. Takes a resolved
// identifier.
func (store *Store) Manifest(identifier string) (Manifest, error) {
	data, err := os.ReadFile(
		store.snapshotPath(identifier, manifestSuffix))
	if err != nil {
		return nil, fmt.Errorf("cannot read checkpoint %s: %w",
			identifier, err)
	}
	return parseManifest(data), nil
}

// Metadata returns the metadata of a checkpoint. Takes a resolved
// identifier.
func (store *Store) Metadata(identifier string) (Metadata, error) {
	data, err := os.ReadFile(
		store.snapshotPath(identifier, metadataSuffix))
	if err != nil {
		return Metadata{}, fmt.Errorf(
			"cannot read the details of checkpoint %s: %w", identifier, err)
	}
	var document metadataDocument
	if err := json.Unmarshal(data, &document); err != nil {
		return Metadata{}, fmt.Errorf(
			"the details of checkpoint %s are not readable JSON: %w",
			identifier, err)
	}
	return document.metadata(), nil
}

// Take creates a new checkpoint from the working tree. Takes an
// optional label. Returns the new checkpoint. Contents are
// deduplicated by digest and HEAD is updated.
func (store *Store) Take(label string) (Checkpoint, error) {
	entries, _, err := store.Scan()
	if err != nil {
		return Checkpoint{}, err
	}
	for _, entry := range entries {
		if err := store.storeEntryContent(entry); err != nil {
			return Checkpoint{}, err
		}
	}

	identifiers, err := store.identifiers()
	if err != nil {
		return Checkpoint{}, err
	}
	highest := 0
	if len(identifiers) > 0 {
		highest, _ = strconv.Atoi(identifiers[len(identifiers)-1])
	}
	identifier := formatCheckpointID(highest + 1)

	metadata := Metadata{
		Time:    time.Now(),
		Label:   label,
		Entries: len(entries),
	}
	document, err := json.MarshalIndent(metadata.document(), "", "  ")
	if err != nil {
		return Checkpoint{}, fmt.Errorf(
			"cannot write the details of checkpoint %s: %w", identifier, err)
	}
	if err := writeFileAtomically(
		store.snapshotPath(identifier, manifestSuffix),
		formatManifest(entries), 0o644); err != nil {
		return Checkpoint{}, fmt.Errorf("cannot write checkpoint %s: %w",
			identifier, err)
	}
	if err := writeFileAtomically(
		store.snapshotPath(identifier, metadataSuffix),
		append(document, '\n'), 0o644); err != nil {
		return Checkpoint{}, fmt.Errorf(
			"cannot write the details of checkpoint %s: %w", identifier, err)
	}
	if err := store.SetHead(identifier); err != nil {
		return Checkpoint{}, err
	}
	return Checkpoint{ID: identifier, Metadata: metadata}, nil
}

// storeEntryContent stores one entry's contents if not already
// present. Directories have no contents. Links store their target.
func (store *Store) storeEntryContent(entry Entry) error {
	if entry.Kind == KindDirectory {
		return nil
	}
	destination := store.BlobPath(entry.Digest)
	if _, err := os.Lstat(destination); err == nil {
		return nil
	}
	absolute := filepath.Join(store.Project, entry.Path)
	if entry.Kind == KindLink {
		target, err := os.Readlink(absolute)
		if err != nil {
			return fmt.Errorf("cannot read the link %s: %w", entry.Path, err)
		}
		return store.writeObject(destination, strings.NewReader(target))
	}
	source, err := os.Open(absolute)
	if err != nil {
		return fmt.Errorf("cannot read %s: %w", entry.Path, err)
	}
	defer source.Close()
	return store.writeObject(destination, source)
}

// writeObject copies content to an object path atomically via a
// temporary file.
func (store *Store) writeObject(destination string, content io.Reader) error {
	shard := filepath.Dir(destination)
	if err := os.MkdirAll(shard, 0o755); err != nil {
		return fmt.Errorf("cannot create the object store: %w", err)
	}
	temporary, err := os.CreateTemp(shard, "partial-*")
	if err != nil {
		return fmt.Errorf("cannot write into the object store: %w", err)
	}
	defer os.Remove(temporary.Name())
	if _, err := io.Copy(temporary, content); err != nil {
		temporary.Close()
		return fmt.Errorf("cannot write into the object store: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("cannot write into the object store: %w", err)
	}
	if err := os.Chmod(temporary.Name(), 0o644); err != nil {
		return fmt.Errorf("cannot write into the object store: %w", err)
	}
	if err := os.Rename(temporary.Name(), destination); err != nil {
		return fmt.Errorf("cannot write into the object store: %w", err)
	}
	return nil
}

// Remove deletes a checkpoint's manifest and metadata. Clears HEAD
// if it pointed to this checkpoint. Shared objects are left for
// Prune.
func (store *Store) Remove(wanted string) error {
	identifier, err := store.Resolve(wanted)
	if err != nil {
		return err
	}
	for _, suffix := range []string{manifestSuffix, metadataSuffix} {
		err := os.Remove(store.snapshotPath(identifier, suffix))
		if err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("cannot remove checkpoint %s: %w",
				identifier, err)
		}
	}
	if head, ok := store.Head(); ok && head == identifier {
		return store.clearHead()
	}
	return nil
}

// Prune deletes objects no checkpoint references. Returns the count
// of removed objects and the bytes freed. Also removes stale
// temporary files left in the object directory.
func (store *Store) Prune() (int, int64, error) {
	identifiers, err := store.identifiers()
	if err != nil {
		return 0, 0, err
	}
	referenced := map[string]bool{}
	for _, identifier := range identifiers {
		manifest, err := store.Manifest(identifier)
		if err != nil {
			return 0, 0, err
		}
		for _, entry := range manifest {
			if entry.Digest != "" {
				referenced[entry.Digest] = true
			}
		}
	}

	root := filepath.Join(store.Dir, objectsDirectory)
	shards, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, 0, nil
		}
		return 0, 0, fmt.Errorf("cannot read the object store: %w", err)
	}
	removed := 0
	var freed int64
	for _, shard := range shards {
		if !shard.IsDir() {
			continue
		}
		objects, err := os.ReadDir(filepath.Join(root, shard.Name()))
		if err != nil {
			return 0, 0, fmt.Errorf("cannot read the object store: %w", err)
		}
		for _, object := range objects {
			if referenced[shard.Name()+object.Name()] {
				continue
			}
			path := filepath.Join(root, shard.Name(), object.Name())
			information, err := object.Info()
			if err != nil {
				continue
			}
			if err := os.Remove(path); err != nil {
				continue
			}
			removed++
			freed += information.Size()
		}
	}
	return removed, freed, nil
}

// writeFileAtomically writes data to a path via a temporary file.
// Takes the destination path, the content, and the file mode.
func writeFileAtomically(path string, data []byte, mode os.FileMode) error {
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, "partial-*")
	if err != nil {
		return err
	}
	defer os.Remove(temporary.Name())
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Chmod(temporary.Name(), mode); err != nil {
		return err
	}
	return os.Rename(temporary.Name(), path)
}
