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

type Placeholders struct {
	PlaceholderTail string
	Inmate          string
	ProjectID       string
}

func (p Placeholders) replacer() *strings.Replacer {
	return strings.NewReplacer(
		"${placeholder_tail}", p.PlaceholderTail,
		"${inmate}", p.Inmate,
		"${project_id}", p.ProjectID,
	)
}

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
	// The box can write the persisted home.
	destination, err := os.OpenRoot(persistedHome)
	if err != nil {
		return nil, nil, fmt.Errorf("cannot open %s: %w", persistedHome, err)
	}
	defer destination.Close()
	for _, entry := range host.Copy {
		copied, note, err := copyHostEntry(root, destination, entry)
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
			root, destination, operation, placeholders)
		notes = append(notes, operationNotes...)
		if err != nil {
			return carried, notes, err
		}
		if written && !contains(carried, operation.To) {
			carried = append(carried, operation.To)
		}
	}
	return carried, notes, nil
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func copyHostEntry(
	root string, destination *os.Root, entry string,
) (bool, string, error) {
	wantsDirectory := strings.HasSuffix(entry, "/")
	relative := strings.TrimSuffix(entry, "/")
	source, err := resolveInside(root, relative)
	if err != nil {
		return false, "", err
	}
	name := filepath.FromSlash(relative)
	info, err := os.Stat(source)
	if errors.Is(err, fs.ErrNotExist) {
		return false, fmt.Sprintf(
			"skipped %s because it is not on the host", entry), nil
	}
	if err != nil {
		return false, "", fmt.Errorf("cannot read %s: %w", source, err)
	}
	if info.IsDir() != wantsDirectory {
		return false, fmt.Sprintf(
			"skipped %s because its type does not match the host", entry), nil
	}
	if wantsDirectory {
		if err := mergeDirectory(source, destination, name); err != nil {
			return false, "", err
		}
		return true, "", nil
	}
	err = copyHostFile(source, destination, name, info.Mode().Perm())
	if err != nil {
		return false, "", err
	}
	return true, "", nil
}

func mergeDirectory(source string, destination *os.Root, name string) error {
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
		target := filepath.Join(name, relative)
		if entry.IsDir() {
			if err := destination.MkdirAll(target, 0o700); err != nil {
				return fmt.Errorf("cannot create %s: %w", target, err)
			}
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		return copyHostFile(path, destination, target, info.Mode().Perm())
	})
}

func copyHostFile(
	source string, destination *os.Root, name string, mode fs.FileMode,
) error {
	data, err := os.ReadFile(source)
	if err != nil {
		return fmt.Errorf("cannot read %s: %w", source, err)
	}
	return writeInside(destination, name, data, mode)
}

func runJSONOperation(
	root string, destination *os.Root, operation HostJSONOperation,
	placeholders Placeholders,
) (bool, []string, error) {
	var notes []string
	name := filepath.FromSlash(operation.To)
	read, source := destination.ReadFile, name
	if operation.From != "" {
		var err error
		source, err = resolveInside(root, operation.From)
		if err != nil {
			return false, notes, err
		}
		read = os.ReadFile
	}
	document, found, readNote := readJSONObject(read, source)
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
	if err := writeJSONObject(destination, name, document); err != nil {
		return false, notes, err
	}
	return true, notes, nil
}

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

func setDotted(document map[string]any, dotted, value, name string) []string {
	segments := strings.Split(dotted, ".")
	container, notes := containerFor(document, segments[:len(segments)-1], dotted, name)
	container[segments[len(segments)-1]] = value
	return notes
}

func appendDotted(document map[string]any, dotted, value, name string) []string {
	segments := strings.Split(dotted, ".")
	container, notes := containerFor(document, segments[:len(segments)-1], dotted, name)
	last := segments[len(segments)-1]
	existing, isArray := container[last].([]any)
	if _, present := container[last]; present && !isArray {
		notes = append(notes, fmt.Sprintf(
			"%s: replaced %s because it was not a list", name, dotted))
	}
	for _, member := range existing {
		if text, ok := member.(string); ok && text == value {
			return notes
		}
	}
	container[last] = append(existing, value)
	return notes
}

func containerFor(
	document map[string]any, segments []string, dotted, name string,
) (map[string]any, []string) {
	var notes []string
	container := document
	for _, segment := range segments {
		next, isObject := container[segment].(map[string]any)
		if !isObject {
			if _, present := container[segment]; present {
				notes = append(notes, fmt.Sprintf(
					"%s: replaced %s because it was not an object", name, dotted))
			}
			next = map[string]any{}
			container[segment] = next
		}
		container = next
	}
	return container, notes
}

func readJSONObject(
	read func(string) ([]byte, error), path string,
) (map[string]any, bool, string) {
	data, err := read(path)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]any{}, false, ""
	}
	if err != nil {
		return map[string]any{}, false,
			fmt.Sprintf("ignored %s because it cannot be read", path)
	}
	document := map[string]any{}
	if err := json.Unmarshal(data, &document); err != nil {
		return map[string]any{}, false,
			fmt.Sprintf("ignored %s because it is not a JSON object", path)
	}
	return document, true, ""
}

func writeJSONObject(
	destination *os.Root, name string, document map[string]any,
) error {
	data, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return fmt.Errorf("cannot write %s: %w", name, err)
	}
	return writeInside(destination, name, append(data, '\n'), 0o600)
}

func writeInside(
	destination *os.Root, name string, data []byte, mode fs.FileMode,
) error {
	if err := destination.MkdirAll(filepath.Dir(name), 0o700); err != nil {
		return fmt.Errorf("cannot create %s: %w", filepath.Dir(name), err)
	}
	if err := destination.WriteFile(name, data, mode); err != nil {
		return fmt.Errorf("cannot write %s: %w", name, err)
	}
	return nil
}

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
		document, found, _ := readJSONObject(os.ReadFile, path)
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

// matchesInFull rejects a value that an unanchored pattern matches only in
// part.
func matchesInFull(pattern *regexp.Regexp, value string) bool {
	found := pattern.FindStringIndex(value)
	return found != nil && found[0] == 0 && found[1] == len(value)
}

func resolveUnderHome(homeDir, value string) (string, error) {
	if value == "" {
		return homeDir, nil
	}
	return resolveInside(homeDir, homeRelativePath(value))
}

func resolveInside(base, relative string) (string, error) {
	target := filepath.Join(base, filepath.FromSlash(relative))
	within, err := filepath.Rel(base, target)
	if err != nil {
		return "", fmt.Errorf("%q is not a path inside %s", relative, base)
	}
	if within == ".." || strings.HasPrefix(within, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%q is outside %s", relative, base)
	}
	return target, nil
}
