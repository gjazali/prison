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
	PlaceholderTail  string
	Inmate           string
	ProjectID        string
	ActiveCredential string
	InBoxLogin       bool
}

func (p Placeholders) replacer() *strings.Replacer {
	return strings.NewReplacer(
		"${placeholder_tail}", p.PlaceholderTail,
		"${inmate}", p.Inmate,
		"${project_id}", p.ProjectID,
	)
}

type HostSyncResult struct {
	Carried []string
	Removed []string
	Notes   []string
}

func RunHostOperations(
	inmate *Inmate, homeDir string, persistedHome string,
	placeholders Placeholders,
) (carried []string, notes []string, err error) {
	result, err := runHostOperations(inmate, homeDir, persistedHome, placeholders, false)
	return result.Carried, result.Notes, err
}

// MirrorHostOperations runs the inmate's [host] operations and mirrors
// [host].copy entries and [[host.json]] sources by removing entries in
// persistedHome that no longer exist on the host.
func MirrorHostOperations(
	inmate *Inmate, homeDir string, persistedHome string,
	placeholders Placeholders,
) (HostSyncResult, error) {
	return runHostOperations(inmate, homeDir, persistedHome, placeholders, true)
}

func runHostOperations(
	inmate *Inmate, homeDir string, persistedHome string,
	placeholders Placeholders, mirror bool,
) (HostSyncResult, error) {
	var result HostSyncResult
	host := inmate.Host
	root, err := resolveUnderHome(homeDir, host.Root)
	if err != nil {
		return result, err
	}
	if err := os.MkdirAll(persistedHome, 0o700); err != nil {
		return result, fmt.Errorf("cannot create %s: %w", persistedHome, err)
	}
	// The box can write the persisted home.
	destination, err := os.OpenRoot(persistedHome)
	if err != nil {
		return result, fmt.Errorf("cannot open %s: %w", persistedHome, err)
	}
	defer destination.Close()
	for _, entry := range host.Copy {
		copied, removed, note, err := copyHostEntry(root, destination, entry, mirror)
		if err != nil {
			return result, err
		}
		if note != "" {
			result.Notes = append(result.Notes, note)
		}
		if copied {
			result.Carried = append(result.Carried, entry)
		}
		for _, item := range removed {
			if !contains(result.Removed, item) {
				result.Removed = append(result.Removed, item)
			}
		}
	}
	for _, operation := range host.JSON {
		if !matchesAuthMode(operation.WhenAuth, placeholders.InBoxLogin) {
			continue
		}
		if len(operation.WhenCredential) > 0 &&
			(placeholders.InBoxLogin || !contains(operation.WhenCredential, placeholders.ActiveCredential)) {
			continue
		}
		written, removed, operationNotes, err := runJSONOperation(
			root, destination, operation, placeholders, mirror)
		result.Notes = append(result.Notes, operationNotes...)
		if err != nil {
			return result, err
		}
		if written {
			if !contains(result.Carried, operation.To) {
				result.Carried = append(result.Carried, operation.To)
			}
			result.Removed = removeString(result.Removed, operation.To)
		}
		if removed && !contains(result.Removed, operation.To) {
			result.Removed = append(result.Removed, operation.To)
		}
	}
	return result, nil
}

func matchesAuthMode(whenAuth string, inBox bool) bool {
	switch whenAuth {
	case "":
		return true
	case AuthModeBroker:
		return !inBox
	case AuthModeInBox:
		return inBox
	default:
		return false
	}
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func removeString(values []string, unwanted string) []string {
	out := values[:0]
	for _, value := range values {
		if value != unwanted {
			out = append(out, value)
		}
	}
	return out
}

func copyHostEntry(
	root string, destination *os.Root, entry string, mirror bool,
) (bool, []string, string, error) {
	wantsDirectory := strings.HasSuffix(entry, "/")
	relative := strings.TrimSuffix(entry, "/")
	source, err := resolveInside(root, relative)
	if err != nil {
		return false, nil, "", err
	}
	name := filepath.FromSlash(relative)
	info, err := os.Stat(source)
	if errors.Is(err, fs.ErrNotExist) {
		if mirror {
			removed, removeErr := removeInsideIfPresent(destination, name)
			if removeErr != nil {
				return false, nil, "", removeErr
			}
			if removed {
				return false, []string{entry}, "", nil
			}
		}
		return false, nil, fmt.Sprintf(
			"skipped %s because it is not on the host", entry), nil
	}
	if err != nil {
		return false, nil, "", fmt.Errorf("cannot read %s: %w", source, err)
	}
	if info.IsDir() != wantsDirectory {
		return false, nil, fmt.Sprintf(
			"skipped %s because its type does not match the host", entry), nil
	}
	if wantsDirectory {
		if mirror {
			pruned, err := mirrorDirectory(source, destination, name)
			if err != nil {
				return false, nil, "", err
			}
			return true, pruned, "", nil
		}
		if err := mergeDirectory(source, destination, name); err != nil {
			return false, nil, "", err
		}
		return true, nil, "", nil
	}
	if mirror {
		if err := removeIfDirectory(destination, name); err != nil {
			return false, nil, "", err
		}
	}
	err = copyHostFile(source, destination, name, info.Mode().Perm())
	if err != nil {
		return false, nil, "", err
	}
	return true, nil, "", nil
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

func mirrorDirectory(source string, destination *os.Root, name string) ([]string, error) {
	cleanRoot := filepath.Clean(name)
	wantedDirs := map[string]bool{cleanRoot: true}
	wantedFiles := map[string]bool{}
	err := filepath.WalkDir(source, func(path string, entry fs.DirEntry, err error) error {
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
		target := filepath.Clean(filepath.Join(name, relative))
		if entry.IsDir() {
			if err := removeIfNonDirectory(destination, target); err != nil {
				return err
			}
			if err := destination.MkdirAll(target, 0o700); err != nil {
				return fmt.Errorf("cannot create %s: %w", target, err)
			}
			wantedDirs[target] = true
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		if err := removeIfDirectory(destination, target); err != nil {
			return err
		}
		if err := copyHostFile(path, destination, target, info.Mode().Perm()); err != nil {
			return err
		}
		wantedFiles[target] = true
		return nil
	})
	if err != nil {
		return nil, err
	}
	return pruneDirectory(destination, cleanRoot, wantedDirs, wantedFiles)
}

func pruneDirectory(
	destination *os.Root, dir string,
	wantedDirs, wantedFiles map[string]bool,
) ([]string, error) {
	handle, err := destination.Open(dir)
	if err != nil {
		return nil, fmt.Errorf("cannot read %s: %w", dir, err)
	}
	entries, err := handle.ReadDir(-1)
	handle.Close()
	if err != nil {
		return nil, fmt.Errorf("cannot read %s: %w", dir, err)
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() < entries[j].Name()
	})
	var pruned []string
	for _, entry := range entries {
		child := filepath.Clean(filepath.Join(dir, entry.Name()))
		info, err := destination.Lstat(child)
		if err != nil {
			return pruned, fmt.Errorf("cannot inspect %s: %w", child, err)
		}
		if wantedDirs[child] && info.IsDir() {
			nested, err := pruneDirectory(destination, child, wantedDirs, wantedFiles)
			pruned = append(pruned, nested...)
			if err != nil {
				return pruned, err
			}
			continue
		}
		if wantedFiles[child] && info.Mode().IsRegular() {
			continue
		}
		removed, err := removeInsideIfPresent(destination, child)
		if err != nil {
			return pruned, err
		}
		if removed {
			label := filepath.ToSlash(child)
			if info.IsDir() {
				label += "/"
			}
			pruned = append(pruned, label)
		}
	}
	return pruned, nil
}

func removeIfDirectory(destination *os.Root, name string) error {
	info, err := destination.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("cannot inspect %s: %w", name, err)
	}
	if info.IsDir() {
		if err := destination.RemoveAll(name); err != nil {
			return fmt.Errorf("cannot remove %s: %w", name, err)
		}
	}
	return nil
}

func removeIfNonDirectory(destination *os.Root, name string) error {
	info, err := destination.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("cannot inspect %s: %w", name, err)
	}
	if info.IsDir() {
		return nil
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		if _, statErr := destination.Stat(name); statErr != nil && !errors.Is(statErr, fs.ErrNotExist) {
			return fmt.Errorf("cannot replace %s: %w", name, statErr)
		}
	}
	if err := destination.Remove(name); err != nil {
		return fmt.Errorf("cannot remove %s: %w", name, err)
	}
	return nil
}

func removeInsideIfPresent(destination *os.Root, name string) (bool, error) {
	info, err := destination.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("cannot inspect %s: %w", name, err)
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		if _, statErr := destination.Stat(name); statErr != nil && !errors.Is(statErr, fs.ErrNotExist) {
			return false, fmt.Errorf("cannot remove %s: %w", name, statErr)
		}
		if err := destination.Remove(name); err != nil {
			return false, fmt.Errorf("cannot remove %s: %w", name, err)
		}
		return true, nil
	}
	if info.IsDir() {
		if err := destination.RemoveAll(name); err != nil {
			return false, fmt.Errorf("cannot remove %s: %w", name, err)
		}
		return true, nil
	}
	if err := destination.Remove(name); err != nil {
		return false, fmt.Errorf("cannot remove %s: %w", name, err)
	}
	return true, nil
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
	placeholders Placeholders, mirror bool,
) (bool, bool, []string, error) {
	var notes []string
	name := filepath.FromSlash(operation.To)
	read, source := destination.ReadFile, name
	if operation.From != "" {
		var err error
		source, err = resolveInside(root, operation.From)
		if err != nil {
			return false, false, notes, err
		}
		read = os.ReadFile
	}
	document, found, notExist, readNote := readJSONObject(read, source)
	if readNote != "" {
		notes = append(notes, readNote)
	}
	if operation.From != "" && !found {
		if mirror && notExist {
			removed, err := removeInsideIfPresent(destination, name)
			if err != nil {
				return false, false, notes, err
			}
			return false, removed, notes, nil
		}
		return false, false, notes, nil
	}
	if !found && len(operation.Set) == 0 && len(operation.Append) == 0 &&
		len(operation.Keep) == 0 {
		return false, false, notes, nil
	}
	if len(operation.Keep) > 0 {
		var dropped []string
		document, dropped = keepOnly(document, operation.Keep)
		if len(dropped) > 0 {
			notes = append(notes, fmt.Sprintf("%s: dropped %s",
				operation.To, strings.Join(dropped, ", ")))
		}
	}
	for _, dotted := range operation.Remove {
		removeDotted(document, dotted)
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
		return false, false, notes, err
	}
	return true, false, notes, nil
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

func removeDotted(document map[string]any, dotted string) {
	segments := strings.Split(dotted, ".")
	container := document
	for _, segment := range segments[:len(segments)-1] {
		next, isObject := container[segment].(map[string]any)
		if !isObject {
			return
		}
		container = next
	}
	delete(container, segments[len(segments)-1])
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
) (map[string]any, bool, bool, string) {
	data, err := read(path)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]any{}, false, true, ""
	}
	if err != nil {
		return map[string]any{}, false, false,
			fmt.Sprintf("ignored %s because it cannot be read", path)
	}
	document := map[string]any{}
	if err := json.Unmarshal(data, &document); err != nil {
		return map[string]any{}, false, false,
			fmt.Sprintf("ignored %s because it is not a JSON object", path)
	}
	return document, true, false, ""
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
		document, found, _, _ := readJSONObject(os.ReadFile, path)
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
