package config

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"
)

// tableSchema maps table names to accepted keys. A nil key list allows
// any key in that table.
type tableSchema map[string][]string

func unusable(path string, problem error) error {
	return fmt.Errorf(
		"%s is not valid: %w. Fix it or delete it to use the defaults",
		path, problem)
}

func decodeFile(path string, target any, schema tableSchema) (bool, error) {
	text, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, unusable(path, fmt.Errorf("cannot read it: %w", err))
	}
	return true, decodeText(path, text, target, schema)
}

func decodeText(
	path string, text []byte, target any, schema tableSchema) error {
	metadata, err := toml.Decode(string(text), target)
	if err != nil {
		return unusable(path, err)
	}
	if err := refuseUnknown(filepath.Base(path), schema, metadata); err != nil {
		return unusable(path, err)
	}
	return nil
}

func refuseUnknown(
	fileName string, schema tableSchema, metadata toml.MetaData) error {
	for _, key := range metadata.Undecoded() {
		accepted, known := schema[key[0]]
		if !known {
			tables := listNames(slices.Collect(maps.Keys(schema)))
			if metadata.Type(key...) != "Hash" {
				return fmt.Errorf("unknown key %q outside a table. %s accepts "+
					"the tables %s", key[0], fileName, tables)
			}
			return fmt.Errorf("unknown table [%s]. %s accepts %s",
				key[0], fileName, tables)
		}
		if accepted == nil || len(key) < 2 {
			continue
		}
		return fmt.Errorf("unknown key %q in [%s]. It accepts %s",
			key[1], key[0], listNames(accepted))
	}
	return nil
}

func listNames(names []string) string {
	return strings.Join(slices.Sorted(slices.Values(names)), ", ")
}
