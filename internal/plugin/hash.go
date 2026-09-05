package plugin

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
)

// ignoredTreeNames are file and directory names excluded from a
// plugin's hash. Editing them does not change the plugin's identity.
var ignoredTreeNames = map[string]bool{
	".git":         true,
	".DS_Store":    true,
	"__pycache__":  true,
	"node_modules": true,
}

// TreeHash returns a hex SHA-256 that identifies a plugin's contents.
// It takes a filesystem and returns the hash string and an error. It
// walks files in sorted order, hashing each path, execute bit, and
// content. Symlinks hash their target instead.
func TreeHash(fsys fs.FS) (string, error) {
	digest := sha256.New()
	walkError := fs.WalkDir(fsys, ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if name == "." {
			return nil
		}
		if ignoredTreeNames[entry.Name()] {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("cannot read %s: %w", name, err)
		}
		digest.Write([]byte(name))
		if info.Mode().Perm()&0o100 != 0 {
			digest.Write([]byte{1})
		} else {
			digest.Write([]byte{0})
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			target, err := fs.ReadLink(fsys, name)
			if err != nil {
				return fmt.Errorf("cannot read the symlink %s: %w", name, err)
			}
			digest.Write([]byte(target))
			return nil
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		contents, err := hashFile(fsys, name)
		if err != nil {
			return err
		}
		digest.Write(contents)
		return nil
	})
	if walkError != nil {
		return "", walkError
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

// hashFile returns the raw SHA-256 bytes of one file's contents. It
// streams the data so large files are not held in memory.
func hashFile(fsys fs.FS, name string) ([]byte, error) {
	file, err := fsys.Open(name)
	if err != nil {
		return nil, fmt.Errorf("cannot read %s: %w", name, err)
	}
	defer file.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return nil, fmt.Errorf("cannot read %s: %w", name, err)
	}
	return digest.Sum(nil), nil
}

// TreeFiles returns the paths a tree hash covers, in sorted order.
// It takes a filesystem and returns the path list and an error.
func TreeFiles(fsys fs.FS) ([]string, error) {
	var names []string
	err := fs.WalkDir(fsys, ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if name == "." {
			return nil
		}
		if ignoredTreeNames[entry.Name()] {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		names = append(names, name)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return names, nil
}
