package plugin

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Placeholders holds the substitution values for `[host]` operations.
type Placeholders struct {
	PlaceholderTail string
	Inmate          string
	ProjectID       string
}

// replacer returns a string replacer for the placeholder variables.
func (p Placeholders) replacer() *strings.Replacer {
	return strings.NewReplacer(
		"${placeholder_tail}", p.PlaceholderTail,
		"${inmate}", p.Inmate,
		"${project_id}", p.ProjectID,
	)
}

// RunHostOperations runs an inmate's `[host]` copy and JSON
// operations. It reads from `homeDir` and writes inside
// `persistedHome`. It returns the names it wrote, notes for skipped
// items, and an error if a write fails or a path escapes the
// persisted home.
func RunHostOperations(
	inmate *Inmate, homeDir string, persistedHome string,
	placeholders Placeholders,
) (carried []string, notes []string, err error) {
	host := inmate.Host
	root, err := resolveUnderHome(homeDir, host.Root)
	if err != nil {
		return nil, nil, err
	}
	if err := os.MkdirAll(persistedHome, 0o700); err != nil {
		return nil, nil, fmt.Errorf("cannot create %s: %w", persistedHome, err)
	}
	for _, entry := range host.Copy {
		copied, note, err := copyHostEntry(root, persistedHome, entry)
		if err != nil {
			return carried, notes, err
		}
		if note != "" {
			notes = append(notes, note)
		}
		if copied {
			carried = append(carried, entry)
		}
	}
	for _, operation := range host.JSON {
		written, operationNotes, err := runJSONOperation(
			root, persistedHome, operation, placeholders)
		notes = append(notes, operationNotes...)
		if err != nil {
			return carried, notes, err
		}
		if written {
			carried = append(carried, operation.To)
		}
	}
	return carried, notes, nil
}

// copyHostEntry copies one `[host] copy` entry into the persisted
// home. It merges directories and overwrites files. It returns
// whether anything was copied and a note when the entry was skipped.
func copyHostEntry(root, persistedHome, entry string) (bool, string, error) {
	wantsDirectory := strings.HasSuffix(entry, "/")
	relative := strings.TrimSuffix(entry, "/")
	source, err := resolveInside(root, relative)
	if err != nil {
		return false, "", err
	}
	destination, err := resolveInside(persistedHome, relative)
	if err != nil {
		return false, "", err
	}
	info, err := os.Stat(source)
	if errors.Is(err, fs.ErrNotExist) {
		return false, fmt.Sprintf("%s is not on the host, so it was not carried", entry), nil
	}
	if err != nil {
		return false, "", fmt.Errorf("cannot read %s: %w", source, err)
	}
	if info.IsDir() != wantsDirectory {
		return false, fmt.Sprintf("%s does not name what is on the host, so it was not carried", entry), nil
	}
	if wantsDirectory {
		if err := mergeDirectory(source, destination); err != nil {
			return false, "", err
		}
		return true, "", nil
	}
	if err := copyFile(source, destination, info.Mode().Perm()); err != nil {
		return false, "", err
	}
	return true, "", nil
}

// mergeDirectory copies `source` into `destination`, creating missing
// directories and overwriting files. It deletes nothing and skips
// `.DS_Store`. It returns an error if any file cannot be copied.
func mergeDirectory(source, destination string) error {
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Name() == ".DS_Store" {
			return nil
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		return copyFile(path, target, info.Mode().Perm())
	})
}

// runJSONOperation applies one `[[host.json]]` entry. It returns
// whether a file was written, notes about skipped or replaced keys,
// and an error.
func runJSONOperation(
	root, persistedHome string, operation HostJSONOperation,
	placeholders Placeholders,
) (bool, []string, error) {
	var notes []string
	destination, err := resolveInside(persistedHome, operation.To)
	if err != nil {
		return false, notes, err
	}
	source := destination
	if operation.From != "" {
		source, err = resolveInside(root, operation.From)
		if err != nil {
			return false, notes, err
		}
	}
	document, found, readNote := readJSONObject(source)
	if readNote != "" {
		notes = append(notes, readNote)
	}
	if operation.From != "" && !found {
		return false, notes, nil
	}
	if len(operation.Keep) > 0 {
		var dropped []string
		document, dropped = keepOnly(document, operation.Keep)
		if len(dropped) > 0 {
			notes = append(notes, fmt.Sprintf("%s: dropped %s",
				operation.To, strings.Join(dropped, ", ")))
		}
	}
	replacer := placeholders.replacer()
	for _, dotted := range sortedKeys(operation.Set) {
		notes = append(notes, setDotted(
			document, dotted, replacer.Replace(operation.Set[dotted]), operation.To)...)
	}
	for _, dotted := range sortedKeys(operation.Append) {
		notes = append(notes, appendDotted(
			document, dotted, replacer.Replace(operation.Append[dotted]), operation.To)...)
	}
	if err := writeJSONObject(destination, document); err != nil {
		return false, notes, err
	}
	return true, notes, nil
}

// keepOnly returns the document with only the named top-level keys,
// and the sorted names of the keys it dropped.
func keepOnly(document map[string]any, keep []string) (map[string]any, []string) {
	allowed := make(map[string]bool, len(keep))
	for _, key := range keep {
		allowed[key] = true
	}
	kept := make(map[string]any, len(keep))
	var dropped []string
	for key, value := range document {
		if allowed[key] {
			kept[key] = value
			continue
		}
		dropped = append(dropped, key)
	}
	sort.Strings(dropped)
	return kept, dropped
}

// setDotted writes a value at a dotted path, creating the objects
// along the way. It returns a note for every value it had to replace
// because it was not an object.
func setDotted(document map[string]any, dotted, value, name string) []string {
	segments := strings.Split(dotted, ".")
	container, notes := containerFor(document, segments[:len(segments)-1], dotted, name)
	container[segments[len(segments)-1]] = value
	return notes
}

// appendDotted adds a value to the array at a dotted path, creating
// the array when it is absent and leaving it alone when the value is
// already there. It returns a note for every value it had to replace.
func appendDotted(document map[string]any, dotted, value, name string) []string {
	segments := strings.Split(dotted, ".")
	container, notes := containerFor(document, segments[:len(segments)-1], dotted, name)
	last := segments[len(segments)-1]
	existing, isArray := container[last].([]any)
	if _, present := container[last]; present && !isArray {
		notes = append(notes, fmt.Sprintf("%s: %s was not a list, so it was replaced", name, dotted))
	}
	for _, member := range existing {
		if text, ok := member.(string); ok && text == value {
			return notes
		}
	}
	container[last] = append(existing, value)
	return notes
}

// containerFor walks a dotted path to the object holding its last
// segment, creating an object wherever one is missing and replacing
// anything that is not an object. It returns that object and a note
// for every replacement.
func containerFor(
	document map[string]any, segments []string, dotted, name string,
) (map[string]any, []string) {
	var notes []string
	container := document
	for _, segment := range segments {
		next, isObject := container[segment].(map[string]any)
		if !isObject {
			if _, present := container[segment]; present {
				notes = append(notes, fmt.Sprintf("%s: %s was not an object, so it was replaced", name, dotted))
			}
			next = map[string]any{}
			container[segment] = next
		}
		container = next
	}
	return container, notes
}

// readJSONObject reads a JSON object from a file. It reports whether
// the file held one, returning an empty object and a note when the
// file is missing, unreadable, or not an object.
func readJSONObject(path string) (map[string]any, bool, string) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]any{}, false, ""
	}
	if err != nil {
		return map[string]any{}, false, fmt.Sprintf("%s cannot be read, so it was ignored", path)
	}
	document := map[string]any{}
	if err := json.Unmarshal(data, &document); err != nil {
		return map[string]any{}, false, fmt.Sprintf("%s is not a JSON object, so it was ignored", path)
	}
	return document, true, ""
}

// writeJSONObject writes a JSON object indented by two spaces with a
// trailing newline, creating the directories above it.
func writeJSONObject(path string, document map[string]any) error {
	data, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return fmt.Errorf("cannot write %s: %w", path, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("cannot create %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("cannot write %s: %w", path, err)
	}
	return nil
}

// HostEnvironment evaluates an inmate's `[[host.environment]]` entries
// against the host's files and returns the `NAME=value` lines the box
// is given. A file that is missing, a path that is absent, and a value
// that does not match the pattern in full are all skipped silently; an
// error means a path escaped the home directory or a pattern would not
// compile.
func HostEnvironment(inmate *Inmate, homeDir string) ([]string, error) {
	var assignments []string
	for _, source := range inmate.Host.Environment {
		path, err := resolveUnderHome(homeDir, source.From)
		if err != nil {
			return nil, err
		}
		pattern, err := regexp.Compile(source.Pattern)
		if err != nil {
			return nil, fmt.Errorf("host.environment.pattern %q is not a regular expression: %w",
				source.Pattern, err)
		}
		document, found, _ := readJSONObject(path)
		if !found {
			continue
		}
		value, ok := lookupDotted(document, source.Path)
		if !ok {
			continue
		}
		if !matchesInFull(pattern, value) {
			continue
		}
		assignments = append(assignments, source.Name+"="+value)
	}
	return assignments, nil
}

// lookupDotted returns the string at a dotted path in a JSON object.
// It reports false when the path is absent or holds anything but a
// string.
func lookupDotted(document map[string]any, dotted string) (string, bool) {
	segments := strings.Split(dotted, ".")
	var current any = document
	for _, segment := range segments {
		object, isObject := current.(map[string]any)
		if !isObject {
			return "", false
		}
		value, present := object[segment]
		if !present {
			return "", false
		}
		current = value
	}
	text, isText := current.(string)
	return text, isText
}

// matchesInFull reports whether a pattern matches the whole value, so
// an unanchored pattern cannot let a longer value through.
func matchesInFull(pattern *regexp.Regexp, value string) bool {
	found := pattern.FindStringIndex(value)
	return found != nil && found[0] == 0 && found[1] == len(value)
}

// resolveUnderHome turns a manifest path written relative to the home
// directory, with or without a leading `~`, into an absolute path. It
// returns an error when the result would leave the home directory.
func resolveUnderHome(homeDir, value string) (string, error) {
	if value == "" {
		return homeDir, nil
	}
	return resolveInside(homeDir, homeRelativePath(value))
}

// resolveInside joins a relative path onto a base directory and
// returns an error unless the result stays inside it.
func resolveInside(base, relative string) (string, error) {
	target := filepath.Join(base, filepath.FromSlash(relative))
	within, err := filepath.Rel(base, target)
	if err != nil {
		return "", fmt.Errorf("%q is not a path inside %s", relative, base)
	}
	if within == ".." || strings.HasPrefix(within, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%q would leave %s, which a host operation may not do", relative, base)
	}
	return target, nil
}
