package plugin

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
)

var ignoredTreeNames = map[string]bool{
	".git":         true,
	".DS_Store":    true,
	"__pycache__":  true,
	"node_modules": true,
}

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
