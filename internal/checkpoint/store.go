package checkpoint

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

const (
	objectsDirectory   = "objects"
	snapshotsDirectory = "snapshots"
	headFile           = "HEAD"
	manifestSuffix     = ".manifest"
	metadataSuffix     = ".meta"
)

type Store struct {
	Dir     string
	Project string
	Ignore  *Ignore
}

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

func (store *Store) Lock() (func(), error) {
	path := filepath.Join(store.Dir, "lock")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("cannot open %s: %w", path, err)
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX); err != nil {
		file.Close()
		return nil, fmt.Errorf("cannot lock %s: %w", path, err)
	}
	return func() { file.Close() }, nil
}

func (store *Store) openProject() (*os.Root, error) {
	project, err := os.OpenRoot(store.Project)
	if err != nil {
		return nil, fmt.Errorf("cannot open the project directory %s: %w",
			store.Project, err)
	}
	return project, nil
}

func (store *Store) readProjectFile(relativePath string) ([]byte, error) {
	project, err := store.openProject()
	if err != nil {
		return nil, err
	}
	defer project.Close()
	return project.ReadFile(filepath.FromSlash(relativePath))
}

func formatCheckpointID(number int) string {
	return fmt.Sprintf("%04d", number)
}

func (store *Store) snapshotPath(identifier, suffix string) string {
	return filepath.Join(
		store.Dir, snapshotsDirectory, identifier+suffix)
}

func (store *Store) BlobPath(digest string) string {
	if len(digest) < 3 {
		return filepath.Join(store.Dir, objectsDirectory, digest)
	}
	return filepath.Join(
		store.Dir, objectsDirectory, digest[:2], digest[2:])
}

func (store *Store) Blob(digest string) ([]byte, error) {
	data, err := os.ReadFile(store.BlobPath(digest))
	if err != nil {
		return nil, fmt.Errorf("cannot read stored object %s: %w",
			digest, err)
	}
	return data, nil
}

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

func (store *Store) Head() (string, bool) {
	data, err := os.ReadFile(filepath.Join(store.Dir, headFile))
	if err != nil {
		return "", false
	}
	identifier := strings.TrimSpace(string(data))
	return identifier, identifier != ""
}

func (store *Store) SetHead(identifier string) error {
	path := filepath.Join(store.Dir, headFile)
	if err := os.WriteFile(path, []byte(identifier+"\n"), 0o644); err != nil {
		return fmt.Errorf("cannot record the current checkpoint: %w", err)
	}
	return nil
}

func (store *Store) clearHead() error {
	err := os.Remove(filepath.Join(store.Dir, headFile))
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("cannot clear the current checkpoint: %w", err)
	}
	return nil
}

func (store *Store) Resolve(wanted string) (string, error) {
	identifiers, err := store.identifiers()
	if err != nil {
		return "", err
	}
	if len(identifiers) == 0 {
		return "", fmt.Errorf("no checkpoints for this project. " +
			"Run `prison checkpoint` to take one")
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
	return "", fmt.Errorf("no checkpoint %s. This project has %s",
		wanted, strings.Join(identifiers, ", "))
}

func (store *Store) Manifest(identifier string) (Manifest, error) {
	data, err := os.ReadFile(
		store.snapshotPath(identifier, manifestSuffix))
	if err != nil {
		return nil, fmt.Errorf("cannot read checkpoint %s: %w",
			identifier, err)
	}
	manifest, err := parseManifest(data)
	if err != nil {
		return nil, fmt.Errorf("cannot read checkpoint %s: %w",
			identifier, err)
	}
	return manifest, nil
}

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
			"the details of checkpoint %s are not valid JSON: %w",
			identifier, err)
	}
	return document.metadata(), nil
}

func (store *Store) Take(label string) (Checkpoint, error) {
	entries, skipped, err := store.Scan()
	if err != nil {
		return Checkpoint{}, err
	}
	project, err := store.openProject()
	if err != nil {
		return Checkpoint{}, err
	}
	defer project.Close()
	for index := range entries {
		if err := store.storeEntryContent(
			project, &entries[index]); err != nil {
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
	return Checkpoint{
		ID: identifier, Metadata: metadata, Skipped: skipped}, nil
}

// storeEntryContent hashes the bytes it copies because the file can change
// after the scan.
func (store *Store) storeEntryContent(project *os.Root, entry *Entry) error {
	var content io.Reader
	switch entry.Kind {
	case KindDirectory:
		return nil
	case KindLink:
		target, err := project.Readlink(entry.Path)
		if err != nil {
			return fmt.Errorf("cannot read the link %s: %w", entry.Path, err)
		}
		content = strings.NewReader(target)
	default:
		source, err := openRegularFile(project, entry.Path)
		if err != nil {
			return fmt.Errorf("cannot read %s: %w", entry.Path, err)
		}
		defer source.Close()
		content = source
	}
	digest, err := store.writeObject(content)
	if err != nil {
		return err
	}
	entry.Digest = digest
	return nil
}

func (store *Store) writeObject(content io.Reader) (string, error) {
	temporary, err := os.CreateTemp(
		filepath.Join(store.Dir, objectsDirectory), "partial-*")
	if err != nil {
		return "", fmt.Errorf("cannot write to the object store: %w", err)
	}
	defer os.Remove(temporary.Name())
	hash := sha256.New()
	_, err = io.Copy(io.MultiWriter(temporary, hash), content)
	if err == nil {
		err = temporary.Sync()
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return "", fmt.Errorf("cannot write to the object store: %w", err)
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	destination := store.BlobPath(digest)
	if _, err := os.Lstat(destination); err == nil {
		return digest, nil
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return "", fmt.Errorf("cannot create the object store: %w", err)
	}
	if err := os.Chmod(temporary.Name(), 0o644); err != nil {
		return "", fmt.Errorf("cannot write to the object store: %w", err)
	}
	if err := os.Rename(temporary.Name(), destination); err != nil {
		return "", fmt.Errorf("cannot write to the object store: %w", err)
	}
	return digest, nil
}

// Remove leaves the objects of the checkpoint for `Prune`.
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

// Prune returns the count of removed objects and the bytes freed. It also
// removes leftover `partial-*` files because no manifest references them.
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
			os.Remove(filepath.Join(root, shard.Name()))
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

func writeFileAtomically(path string, data []byte, mode os.FileMode) error {
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, "partial-*")
	if err != nil {
		return err
	}
	defer os.Remove(temporary.Name())
	_, err = temporary.Write(data)
	if err == nil {
		err = temporary.Sync()
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err := os.Chmod(temporary.Name(), mode); err != nil {
		return err
	}
	return os.Rename(temporary.Name(), path)
}
