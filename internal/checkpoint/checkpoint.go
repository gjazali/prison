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

const sniffBytes = 8192

type Kind string

const (
	KindFile      Kind = "file"
	KindLink      Kind = "link"
	KindDirectory Kind = "dir"
)

type Entry struct {
	Kind   Kind
	Mode   os.FileMode
	Digest string
	Path   string
}

// Manifest lists the entries of one checkpoint, sorted by path.
type Manifest []Entry

func (manifest Manifest) Index() map[string]Entry {
	byPath := make(map[string]Entry, len(manifest))
	for _, entry := range manifest {
		byPath[entry.Path] = entry
	}
	return byPath
}

func (manifest Manifest) sortByPath() {
	slices.SortFunc(manifest, func(left, right Entry) int {
		return cmp.Compare(left.Path, right.Path)
	})
}

type Metadata struct {
	Time    time.Time
	Label   string
	Entries int
}

type metadataDocument struct {
	Time    string `json:"time"`
	Label   string `json:"label"`
	Entries int    `json:"entries"`
}

// legacyMetadataTimeLayout is the time format of the old Python tool.
const legacyMetadataTimeLayout = "2006-01-02T15:04:05-0700"

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

type Checkpoint struct {
	ID string
	Metadata
}

type ChangedEntry struct {
	Before Entry
	After  Entry
}

type Changes struct {
	Added   []Entry
	Removed []Entry
	Changed []ChangedEntry
}

func (changes Changes) Empty() bool {
	return changes.Count() == 0
}

func (changes Changes) Count() int {
	return len(changes.Added) + len(changes.Removed) + len(changes.Changed)
}

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

// IsText accepts a truncated character at the end because callers can
// pass only the first bytes of a file.
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
