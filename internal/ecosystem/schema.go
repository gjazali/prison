// Package ecosystem loads declarative ecosystem definitions, detects matching
// ecosystems in a project, and proposes edits to `prison.toml`.
package ecosystem

import (
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/BurntSushi/toml"

	"prison/internal/config"
	"prison/internal/policy"
)

type Ecosystem struct {
	Name        string          `toml:"name"`
	Description string          `toml:"description"`
	Group       string          `toml:"group"`
	Priority    int             `toml:"priority"`
	Detect      DetectTable     `toml:"detect"`
	Box         BoxTable        `toml:"box"`
	Setup       SetupTable      `toml:"setup"`
	Checkpoint  CheckpointTable `toml:"checkpoint"`
	Egress      EgressTable     `toml:"egress"`
}

type DetectTable struct {
	Files    []string      `toml:"files"`
	AllFiles []string      `toml:"all_files"`
	Match    []FileMatcher `toml:"match"`
}

type FileMatcher struct {
	File    string `toml:"file"`
	Pattern string `toml:"pattern"`
}

type BoxTable struct {
	Shadow []string `toml:"shadow"`
}

type SetupTable struct {
	Commands []string `toml:"commands"`
}

type CheckpointTable struct {
	Ignore []string `toml:"ignore"`
}

type EgressTable struct {
	Hosts []string `toml:"hosts"`
	Docs  []string `toml:"docs"`
}

var acceptedKeys = map[string][]string{
	"":             {"name", "description", "group", "priority", "detect", "box", "setup", "checkpoint", "egress"},
	"detect":       {"files", "all_files", "match"},
	"detect.match": {"file", "pattern"},
	"box":          {"shadow"},
	"setup":        {"commands"},
	"checkpoint":   {"ignore"},
	"egress":       {"hosts", "docs"},
}

func Parse(data []byte) (*Ecosystem, error) {
	eco := &Ecosystem{}
	metadata, err := toml.Decode(string(data), eco)
	if err != nil {
		return nil, fmt.Errorf("not valid TOML: %w", err)
	}
	if err := refuseUnknownKeys(metadata.Undecoded()); err != nil {
		return nil, err
	}
	if eco.Description == "" {
		eco.Description = eco.Name
	}
	if err := eco.Validate(); err != nil {
		return nil, err
	}
	return eco, nil
}

func (e *Ecosystem) Validate() error {
	if err := config.ValidateName("ecosystem", e.Name); err != nil {
		return err
	}
	if err := requireLine("description", e.Description); err != nil {
		return err
	}
	if e.Group != "" {
		if err := config.ValidateName("ecosystem group", e.Group); err != nil {
			return err
		}
	}
	if len(e.Detect.Files) == 0 && len(e.Detect.AllFiles) == 0 &&
		len(e.Detect.Match) == 0 {
		return fmt.Errorf("[detect] must declare at least one detection rule")
	}
	for _, pattern := range e.Detect.Files {
		if err := validateDetectPattern("detect.files", pattern); err != nil {
			return err
		}
	}
	for _, pattern := range e.Detect.AllFiles {
		if err := validateDetectPattern("detect.all_files", pattern); err != nil {
			return err
		}
	}
	for index, matcher := range e.Detect.Match {
		cleaned, err := validateRelativePath("detect.match.file", matcher.File)
		if err != nil {
			return err
		}
		e.Detect.Match[index].File = cleaned
		if err := requireLine("detect.match.pattern", matcher.Pattern); err != nil {
			return err
		}
		if _, err := regexp.Compile(matcher.Pattern); err != nil {
			return fmt.Errorf("detect.match.pattern %q is not a regular expression: %w",
				matcher.Pattern, err)
		}
	}
	for index, shadow := range e.Box.Shadow {
		cleaned, err := validateRelativePath("box.shadow", shadow)
		if err != nil {
			return err
		}
		if cleaned == ".git" || strings.HasPrefix(cleaned, ".git/") {
			return fmt.Errorf("box.shadow cannot contain .git")
		}
		e.Box.Shadow[index] = cleaned
	}
	for _, command := range e.Setup.Commands {
		if err := requireLine("setup.commands", command); err != nil {
			return err
		}
	}
	for _, pattern := range e.Checkpoint.Ignore {
		if _, err := validateRelativePath("checkpoint.ignore", pattern); err != nil {
			return err
		}
	}
	for _, host := range e.Egress.Hosts {
		if err := requireLine("egress.hosts", host); err != nil {
			return err
		}
		if _, err := policy.ParsePattern(host); err != nil {
			return fmt.Errorf("egress.hosts: %w", err)
		}
	}
	for _, host := range e.Egress.Docs {
		if err := requireLine("egress.docs", host); err != nil {
			return err
		}
		if _, err := policy.ParsePattern(host); err != nil {
			return fmt.Errorf("egress.docs: %w", err)
		}
	}
	return nil
}

func validateDetectPattern(key, pattern string) error {
	if err := requireLine(key, pattern); err != nil {
		return err
	}
	if strings.HasPrefix(pattern, "/") || strings.Contains(pattern, "..") {
		return fmt.Errorf("%s %q must be a relative path inside the project", key, pattern)
	}
	if _, err := filepath.Match(pattern, "probe"); err != nil {
		return fmt.Errorf("%s %q is not a valid pattern: %w", key, pattern, err)
	}
	return nil
}

func validateRelativePath(key, value string) (string, error) {
	if err := requireLine(key, value); err != nil {
		return "", err
	}
	if strings.HasPrefix(value, "/") {
		return "", fmt.Errorf("%s %q must be a relative path", key, value)
	}
	cleaned := strings.TrimPrefix(filepath.ToSlash(filepath.Clean(value)), "./")
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") ||
		strings.Contains(cleaned, "/../") || strings.HasSuffix(cleaned, "/..") {
		return "", fmt.Errorf("%s %q must stay inside the project", key, value)
	}
	return cleaned, nil
}

func requireLine(key, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s must be a non-empty string", key)
	}
	if strings.ContainsFunc(value, unicode.IsControl) {
		return fmt.Errorf("%s must be a single line without control characters", key)
	}
	return nil
}

func refuseUnknownKeys(undecoded []toml.Key) error {
	if len(undecoded) == 0 {
		return nil
	}
	dotted := make([]string, 0, len(undecoded))
	for _, key := range undecoded {
		dotted = append(dotted, key.String())
	}
	sort.Strings(dotted)
	first := dotted[0]
	parts := strings.Split(first, ".")
	for depth := len(parts) - 1; depth >= 0; depth-- {
		prefix := strings.Join(parts[:depth], ".")
		accepted, known := acceptedKeys[prefix]
		if !known {
			continue
		}
		listed := strings.Join(accepted, ", ")
		if depth < len(parts)-1 {
			return fmt.Errorf("unknown table [%s]. Use one of %s",
				strings.Join(parts[:depth+1], "."), listed)
		}
		if prefix == "" {
			return fmt.Errorf("unknown key %q. Use one of %s", parts[depth], listed)
		}
		return fmt.Errorf("unknown key %q in [%s]. Use one of %s",
			parts[depth], prefix, listed)
	}
	return fmt.Errorf("unknown key %q", first)
}
