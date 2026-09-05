package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

// tableSchema maps table names to their accepted keys. A nil key
// list means any key is allowed in that table.
type tableSchema map[string][]string

// unusable wraps a config file problem into a user-facing error.
// The message names the path, the problem, and how to fix it.
func unusable(path string, problem error) error {
	return fmt.Errorf(
		"%s is unusable: %w; fix it, or delete it to fall back to defaults",
		path, problem)
}

// decodeFile decodes the TOML file at path into target. It takes
// a path, a target struct, and a schema. It returns whether the
// file exists and an error. Unknown tables or keys are refused.
func decodeFile(path string, target any, schema tableSchema) (bool, error) {
	text, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, unusable(path, fmt.Errorf("it cannot be read: %w", err))
	}
	metadata, err := toml.Decode(string(text), target)
	if err != nil {
		return true, unusable(path, err)
	}
	if err := refuseUnknown(filepath.Base(path), schema, metadata); err != nil {
		return true, unusable(path, err)
	}
	return true, nil
}

// refuseUnknown reports the first unknown table or key from the
// decode. It names what the table accepts so typos get helpful
// messages. Returns nil if everything is known.
func refuseUnknown(
	fileName string, schema tableSchema, metadata toml.MetaData) error {
	for _, key := range metadata.Undecoded() {
		accepted, known := schema[key[0]]
		if !known {
			if metadata.Type(key...) != "Hash" {
				return fmt.Errorf(
					"unknown key %q outside any table; %s has only the tables %s",
					key[0], fileName, listNames(tableNames(schema)))
			}
			return fmt.Errorf("unknown table [%s]; %s has only %s",
				key[0], fileName, listNames(tableNames(schema)))
		}
		if accepted == nil || len(key) < 2 {
			continue
		}
		return fmt.Errorf("unknown key %q in [%s]; it has only %s",
			key[1], key[0], listNames(accepted))
	}
	return nil
}

// tableNames returns a schema's table names, sorted.
func tableNames(schema tableSchema) []string {
	names := make([]string, 0, len(schema))
	for name := range schema {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// listNames joins names into a sorted, comma-separated string.
func listNames(names []string) string {
	sorted := append([]string(nil), names...)
	sort.Strings(sorted)
	return strings.Join(sorted, ", ")
}

// sortedKeys returns a map's keys in alphabetical order.
func sortedKeys[Value any](table map[string]Value) []string {
	names := make([]string, 0, len(table))
	for name := range table {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
