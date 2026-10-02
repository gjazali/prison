package checkpoint

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const maximumTotalBytes = 2 * 1024 * 1024 * 1024

const maximumEntryCount = 100000

type Skipped struct {
	Path   string
	Reason string
}

type scanState struct {
	store      *Store
	entries    Manifest
	skipped    []Skipped
	totalBytes int64
}

func (store *Store) Scan() (Manifest, []Skipped, error) {
	state := &scanState{store: store, entries: Manifest{}}
	if err := state.walk("", store.Project); err != nil {
		return nil, nil, err
	}
	state.entries.sortByPath()
	return state.entries, state.skipped, nil
}

func (state *scanState) walk(
	relativeDirectory, absoluteDirectory string) error {
	children, err := os.ReadDir(absoluteDirectory)
	if err != nil {
		if relativeDirectory == "" {
			return fmt.Errorf("cannot read the project directory %s: %w",
				absoluteDirectory, err)
		}
		state.skip(relativeDirectory, fmt.Sprintf("cannot be read: %v", err))
		return nil
	}
	for _, child := range children {
		relativePath := child.Name()
		if relativeDirectory != "" {
			relativePath = relativeDirectory + "/" + child.Name()
		}
		absolutePath := filepath.Join(absoluteDirectory, child.Name())
		isDirectory := child.IsDir()
		if state.store.Ignore.Matches(relativePath, isDirectory) {
			continue
		}
		if err := state.record(
			child, relativePath, absolutePath, isDirectory); err != nil {
			return err
		}
	}
	return nil
}

func (state *scanState) record(
	child os.DirEntry, relativePath, absolutePath string,
	isDirectory bool) error {
	if child.Type()&os.ModeSymlink != 0 {
		target, err := os.Readlink(absolutePath)
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
		return state.walk(relativePath, absolutePath)
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
	digest, err := hashFile(absolutePath)
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

func hashFile(absolutePath string) (string, error) {
	handle, err := os.Open(absolutePath)
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
