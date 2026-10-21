package checkpoint

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"
	"syscall"
)

const maximumTotalBytes = 2 * 1024 * 1024 * 1024

const maximumEntryCount = 100000

type Skipped struct {
	Path   string
	Reason string
}

type scanState struct {
	store      *Store
	project    *os.Root
	entries    Manifest
	skipped    []Skipped
	totalBytes int64
}

func (store *Store) Scan() (Manifest, []Skipped, error) {
	project, err := store.openProject()
	if err != nil {
		return nil, nil, err
	}
	defer project.Close()
	state := &scanState{store: store, project: project, entries: Manifest{}}
	if err := state.walk(""); err != nil {
		return nil, nil, err
	}
	state.entries.sortByPath()
	return state.entries, state.skipped, nil
}

func (state *scanState) walk(relativeDirectory string) error {
	children, err := fs.ReadDir(state.project.FS(),
		cmp.Or(relativeDirectory, "."))
	if err != nil {
		if relativeDirectory == "" {
			return fmt.Errorf("cannot read the project directory %s: %w",
				state.store.Project, err)
		}
		state.skip(relativeDirectory, fmt.Sprintf("cannot be read: %v", err))
		return nil
	}
	for _, child := range children {
		relativePath := child.Name()
		if relativeDirectory != "" {
			relativePath = relativeDirectory + "/" + child.Name()
		}
		isDirectory := child.IsDir()
		if state.store.Ignore.Matches(relativePath, isDirectory) {
			continue
		}
		// A line break in a name will add a false line to the manifest.
		if strings.ContainsAny(child.Name(), "\n\r") {
			state.skip(relativePath, "the name has a line break")
			continue
		}
		if err := state.record(
			child, relativePath, isDirectory); err != nil {
			return err
		}
	}
	return nil
}

func (state *scanState) record(
	child fs.DirEntry, relativePath string, isDirectory bool) error {
	if child.Type()&os.ModeSymlink != 0 {
		target, err := state.project.Readlink(relativePath)
		if err != nil {
			state.skip(relativePath, fmt.Sprintf("cannot be read: %v", err))
			return nil
		}
		digest := sha256.Sum256([]byte(target))
		return state.append(Entry{
			Kind:   KindLink,
			Digest: hex.EncodeToString(digest[:]),
			Path:   relativePath,
		})
	}

	if isDirectory {
		if err := state.append(Entry{
			Kind: KindDirectory,
			Path: relativePath,
		}); err != nil {
			return err
		}
		return state.walk(relativePath)
	}

	information, err := child.Info()
	if err != nil {
		state.skip(relativePath, fmt.Sprintf("cannot be read: %v", err))
		return nil
	}
	if !information.Mode().IsRegular() {
		state.skip(relativePath, "not a regular file")
		return nil
	}
	state.totalBytes += information.Size()
	if state.totalBytes > maximumTotalBytes {
		return fmt.Errorf("this tree is over %dGB. "+
			"Add paths to `[checkpoint] ignore` in prison.toml",
			maximumTotalBytes/(1024*1024*1024))
	}
	digest, err := hashFile(state.project, relativePath)
	if err != nil {
		state.skip(relativePath, fmt.Sprintf("cannot be read: %v", err))
		return nil
	}
	return state.append(Entry{
		Kind:   KindFile,
		Mode:   information.Mode().Perm(),
		Digest: digest,
		Path:   relativePath,
	})
}

func (state *scanState) append(entry Entry) error {
	state.entries = append(state.entries, entry)
	if len(state.entries) > maximumEntryCount {
		return fmt.Errorf("this tree has over %d entries. "+
			"Add paths to `[checkpoint] ignore` in prison.toml",
			maximumEntryCount)
	}
	return nil
}

func (state *scanState) skip(relativePath, reason string) {
	state.skipped = append(state.skipped,
		Skipped{Path: relativePath, Reason: reason})
}

// openRegularFile does not block because the agent can replace a file with
// a FIFO.
func openRegularFile(project *os.Root, relativePath string) (*os.File, error) {
	handle, err := project.OpenFile(
		relativePath, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	information, err := handle.Stat()
	if err != nil {
		handle.Close()
		return nil, err
	}
	if !information.Mode().IsRegular() {
		handle.Close()
		return nil, errors.New("not a regular file")
	}
	return handle, nil
}

func hashFile(project *os.Root, relativePath string) (string, error) {
	handle, err := openRegularFile(project, relativePath)
	if err != nil {
		return "", err
	}
	defer handle.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, handle); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}
