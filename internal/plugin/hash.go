package plugin

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
)

// IgnoredTreeNames also stay out of the build context, so that the approval
// covers all of it.
var IgnoredTreeNames = map[string]bool{
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
		if IgnoredTreeNames[entry.Name()] {
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
		writeFramed(digest, []byte(name))
		if info.Mode().Perm()&0o100 != 0 {
			writeFramed(digest, []byte{1})
		} else {
			writeFramed(digest, []byte{0})
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			target, err := fs.ReadLink(fsys, name)
			if err != nil {
				return fmt.Errorf("cannot read the symlink %s: %w", name, err)
			}
			writeFramed(digest, []byte{'l'})
			writeFramed(digest, []byte(target))
			return nil
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		contents, err := hashFile(fsys, name)
		if err != nil {
			return err
		}
		writeFramed(digest, []byte{'f'})
		writeFramed(digest, contents)
		return nil
	})
	if walkError != nil {
		return "", walkError
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func writeFramed(digest io.Writer, part []byte) {
	binary.Write(digest, binary.BigEndian, uint64(len(part)))
	digest.Write(part)
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
		if IgnoredTreeNames[entry.Name()] {
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
