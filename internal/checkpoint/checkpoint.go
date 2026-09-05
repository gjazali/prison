// Package checkpoint saves and restores snapshots of a project's
// working tree.
package checkpoint

import (
	"bytes"
	"cmp"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// sniffBytes is how many bytes to read when checking if a file is text.
const sniffBytes = 8192

// Kind is the type of filesystem entry: file, link, or directory.
type Kind string

// The three entry kinds a manifest can hold.
const (
	KindFile      Kind = "file"
	KindLink      Kind = "link"
	KindDirectory Kind = "dir"
)

// Entry is one recorded path in a checkpoint.
type Entry struct {
	Kind   Kind
	Mode   os.FileMode
	Digest string
	Path   string
}

// Manifest is a sorted list of all entries in one checkpoint.
type Manifest []Entry

// Index returns a map from path to entry. The map is a copy, so
// it is safe to change.
func (manifest Manifest) Index() map[string]Entry {
	byPath := make(map[string]Entry, len(manifest))
	for _, entry := range manifest {
		byPath[entry.Path] = entry
	}
	return byPath
}

// sortByPath sorts entries by path.
func (manifest Manifest) sortByPath() {
	slices.SortFunc(manifest, func(left, right Entry) int {
		return cmp.Compare(left.Path, right.Path)
	})
}

// Metadata holds the time, label, and entry count of a checkpoint.
type Metadata struct {
	Time    time.Time
	Label   string
	Entries int
}

// metadataDocument is the JSON shape stored on disk.
type metadataDocument struct {
	Time    string `json:"time"`
	Label   string `json:"label"`
	Entries int    `json:"entries"`
}

// legacyMetadataTimeLayout is the time format the old Python tool used.
const legacyMetadataTimeLayout = "2006-01-02T15:04:05-0700"

// document converts the metadata to its on-disk JSON form. A zero
// time becomes an empty string.
func (metadata Metadata) document() metadataDocument {
	document := metadataDocument{
		Label:   metadata.Label,
		Entries: metadata.Entries,
	}
	if !metadata.Time.IsZero() {
		document.Time = metadata.Time.Format(time.RFC3339)
	}
	return document
}

// metadata parses a stored metadata document back into a Metadata
// value. An unreadable time becomes zero instead of an error.
func (document metadataDocument) metadata() Metadata {
	parsed := Metadata{Label: document.Label, Entries: document.Entries}
	for _, layout := range []string{time.RFC3339, legacyMetadataTimeLayout} {
		if moment, err := time.Parse(layout, document.Time); err == nil {
			parsed.Time = moment
			break
		}
	}
	return parsed
}

// Checkpoint is a snapshot's identifier and metadata together.
type Checkpoint struct {
	ID string
	Metadata
}

// ChangedEntry holds the before and after versions of one changed
// path.
type ChangedEntry struct {
	Before Entry
	After  Entry
}

// Changes holds the added, removed, and changed paths between two
// manifests.
type Changes struct {
	Added   []Entry
	Removed []Entry
	Changed []ChangedEntry
}

// Empty returns true if there are no differences.
func (changes Changes) Empty() bool {
	return changes.Count() == 0
}

// Count returns the total number of changed paths.
func (changes Changes) Count() int {
	return len(changes.Added) + len(changes.Removed) + len(changes.Changed)
}

// Compare finds all differences between two manifests. Takes the
// previous and current manifests. Returns a Changes value listing
// added, removed, and changed paths.
func Compare(previous, current Manifest) Changes {
	before := previous.Index()
	after := current.Index()
	var changes Changes
	for _, entry := range current {
		earlier, existed := before[entry.Path]
		switch {
		case !existed:
			changes.Added = append(changes.Added, entry)
		case earlier.Kind != entry.Kind ||
			earlier.Mode.Perm() != entry.Mode.Perm() ||
			earlier.Digest != entry.Digest:
			changes.Changed = append(changes.Changed,
				ChangedEntry{Before: earlier, After: entry})
		}
	}
	for _, entry := range previous {
		if _, survives := after[entry.Path]; !survives {
			changes.Removed = append(changes.Removed, entry)
		}
	}
	Manifest(changes.Added).sortByPath()
	Manifest(changes.Removed).sortByPath()
	slices.SortFunc(changes.Changed, func(left, right ChangedEntry) int {
		return cmp.Compare(left.After.Path, right.After.Path)
	})
	return changes
}

// IsText returns true if data looks like text. Takes a byte slice.
// A NUL in the first 8 KiB or invalid UTF-8 means binary.
func IsText(data []byte) bool {
	probe := data
	if len(probe) > sniffBytes {
		probe = probe[:sniffBytes]
	}
	if bytes.IndexByte(probe, 0) >= 0 {
		return false
	}
	for offset := 0; offset < len(data); {
		character, width := utf8.DecodeRune(data[offset:])
		if character == utf8.RuneError && width <= 1 {
			return isTruncatedRune(data[offset:])
		}
		offset += width
	}
	return true
}

// isTruncatedRune returns true if tail is an incomplete but valid
// start of a multi-byte UTF-8 character.
func isTruncatedRune(tail []byte) bool {
	if len(tail) == 0 || len(tail) > 3 {
		return false
	}
	var wanted int
	switch leader := tail[0]; {
	case leader&0xE0 == 0xC0:
		wanted = 2
	case leader&0xF0 == 0xE0:
		wanted = 3
	case leader&0xF8 == 0xF0:
		wanted = 4
	default:
		return false
	}
	if wanted <= len(tail) {
		return false
	}
	for _, following := range tail[1:] {
		if following&0xC0 != 0x80 {
			return false
		}
	}
	return true
}

// formatManifest converts a manifest to its tab-separated on-disk
// format. Takes a manifest and returns the formatted bytes.
func formatManifest(manifest Manifest) []byte {
	var builder strings.Builder
	for _, entry := range manifest {
		mode := 0
		if entry.Kind == KindFile {
			mode = int(entry.Mode.Perm())
		}
		fmt.Fprintf(&builder, "%s\t%d\t%s\t%s\n",
			entry.Kind, mode, entry.Digest, entry.Path)
	}
	return []byte(builder.String())
}

// parseManifest reads the tab-separated on-disk format. Takes raw
// bytes and returns a manifest. Bad lines are skipped.
func parseManifest(data []byte) Manifest {
	manifest := Manifest{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if line == "" {
			continue
		}
		fields := strings.SplitN(line, "\t", 4)
		if len(fields) != 4 {
			continue
		}
		mode, err := strconv.Atoi(fields[1])
		if err != nil {
			continue
		}
		manifest = append(manifest, Entry{
			Kind:   Kind(fields[0]),
			Mode:   os.FileMode(mode).Perm(),
			Digest: fields[2],
			Path:   fields[3],
		})
	}
	return manifest
}
