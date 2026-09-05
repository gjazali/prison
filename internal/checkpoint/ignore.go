package checkpoint

import (
	"path"
	"strings"
)

// DefaultIgnores lists the patterns every project excludes from
// checkpoints by default.
var DefaultIgnores = []string{
	".git/",
	"node_modules/",
	".venv/",
	"venv/",
	"__pycache__/",
	".mypy_cache/",
	".pytest_cache/",
	".ruff_cache/",
	"target/",
	"dist/",
	"build/",
	".next/",
	".turbo/",
	".DS_Store",
	"*.pyc",
	"*.prison-partial",
	"*.partial",
}

// ignorePattern is one parsed exclusion rule.
type ignorePattern struct {
	text          string
	rooted        bool
	directoryOnly bool
}

// Ignore decides which paths a scan excludes from checkpoints.
type Ignore struct {
	patterns []ignorePattern
}

// NewIgnore builds an Ignore from a list of glob patterns. Takes
// a string slice of patterns and returns an *Ignore. A trailing
// slash means directories only. A slash inside the pattern anchors
// it to the project root.
func NewIgnore(patterns []string) *Ignore {
	ignore := &Ignore{}
	for _, pattern := range patterns {
		directoryOnly := strings.HasSuffix(pattern, "/")
		cleaned := strings.TrimRight(pattern, "/")
		if cleaned == "" {
			continue
		}
		ignore.patterns = append(ignore.patterns, ignorePattern{
			text:          cleaned,
			rooted:        strings.Contains(cleaned, "/"),
			directoryOnly: directoryOnly,
		})
	}
	return ignore
}

// Matches returns true if a path should be excluded. Takes the
// project-relative path and whether it is a directory. A nil Ignore
// excludes nothing.
func (ignore *Ignore) Matches(relativePath string, isDirectory bool) bool {
	if ignore == nil || relativePath == "" {
		return false
	}
	segments := strings.Split(relativePath, "/")
	for _, pattern := range ignore.patterns {
		if pattern.directoryOnly && !isDirectory {
			continue
		}
		if pattern.rooted {
			if matchesPattern(pattern.text, relativePath) {
				return true
			}
			for depth := 1; depth < len(segments); depth++ {
				if matchesPattern(pattern.text,
					strings.Join(segments[:depth], "/")) {
					return true
				}
			}
			continue
		}
		for _, segment := range segments {
			if matchesPattern(pattern.text, segment) {
				return true
			}
		}
	}
	return false
}

// matchesPattern tests one glob against a subject. A bad pattern
// returns false.
func matchesPattern(pattern, subject string) bool {
	matched, err := path.Match(pattern, subject)
	return err == nil && matched
}
