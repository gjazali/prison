package checkpoint

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

// TestIsText checks text detection: plain text passes, NUL bytes
// and invalid UTF-8 fail, and a cut multi-byte character still
// counts as text.
func TestIsText(t *testing.T) {
	longText := bytes.Repeat([]byte("hello world\n"), 2000)
	cases := []struct {
		name string
		data []byte
		want bool
	}{
		{name: "empty", data: nil, want: true},
		{name: "ascii", data: []byte("one\ntwo\n"), want: true},
		{name: "utf8", data: []byte("café åäö\n"), want: true},
		{name: "long text", data: longText, want: true},
		{name: "nul early", data: []byte("bin\x00ary")},
		{name: "invalid utf8", data: []byte("good\xff\xfebad")},
		{name: "cut two byte", data: []byte("caf\xc3"), want: true},
		{name: "cut three byte", data: []byte("snow\xe2\x98"), want: true},
		{name: "lone continuation", data: []byte("bad\x98\x98")},
		{
			name: "nul past the sniff is not looked for",
			data: append(bytes.Repeat([]byte("a"), sniffBytes), 0),
			want: true,
		},
	}
	for _, testCase := range cases {
		if got := IsText(testCase.data); got != testCase.want {
			t.Errorf("%s: IsText = %v, want %v",
				testCase.name, got, testCase.want)
		}
	}
}

// TestCompare checks added, removed, and changed entries, including
// permission-only changes.
func TestCompare(t *testing.T) {
	previous := Manifest{
		{Kind: KindDirectory, Path: "src"},
		{Kind: KindFile, Mode: 0o644, Digest: "aa", Path: "src/kept.go"},
		{Kind: KindFile, Mode: 0o644, Digest: "bb", Path: "src/edited.go"},
		{Kind: KindFile, Mode: 0o644, Digest: "cc", Path: "src/chmod.go"},
		{Kind: KindFile, Mode: 0o644, Digest: "dd", Path: "src/gone.go"},
		{Kind: KindFile, Mode: 0o644, Digest: "ee", Path: "link"},
	}
	current := Manifest{
		{Kind: KindDirectory, Path: "src"},
		{Kind: KindFile, Mode: 0o644, Digest: "aa", Path: "src/kept.go"},
		{Kind: KindFile, Mode: 0o644, Digest: "b2", Path: "src/edited.go"},
		{Kind: KindFile, Mode: 0o755, Digest: "cc", Path: "src/chmod.go"},
		{Kind: KindFile, Mode: 0o644, Digest: "ff", Path: "src/new.go"},
		{Kind: KindLink, Digest: "ee", Path: "link"},
	}
	changes := Compare(previous, current)
	if changes.Empty() {
		t.Fatal("Compare reported no changes")
	}
	if changes.Count() != 5 {
		t.Errorf("Compare counted %d changes, want 5", changes.Count())
	}
	if len(changes.Added) != 1 || changes.Added[0].Path != "src/new.go" {
		t.Errorf("added = %+v, want only src/new.go", changes.Added)
	}
	if len(changes.Removed) != 1 || changes.Removed[0].Path != "src/gone.go" {
		t.Errorf("removed = %+v, want only src/gone.go", changes.Removed)
	}
	wantChanged := []string{"link", "src/chmod.go", "src/edited.go"}
	if len(changes.Changed) != len(wantChanged) {
		t.Fatalf("changed = %+v, want %v", changes.Changed, wantChanged)
	}
	for index, path := range wantChanged {
		if changes.Changed[index].After.Path != path {
			t.Errorf("changed[%d] = %q, want %q",
				index, changes.Changed[index].After.Path, path)
		}
	}
	if !Compare(previous, previous).Empty() {
		t.Error("a manifest compared to itself reported changes")
	}
}

// TestManifestRoundTrip checks that formatting and parsing a
// manifest round-trips for all entry kinds.
func TestManifestRoundTrip(t *testing.T) {
	manifest := Manifest{
		{Kind: KindDirectory, Path: "a dir"},
		{Kind: KindFile, Mode: 0o755, Digest: "aa", Path: "a dir/run.sh"},
		{Kind: KindLink, Digest: "bb", Path: "a dir/point"},
	}
	rendered := string(formatManifest(manifest))
	want := "dir\t0\t\ta dir\n" +
		"file\t493\taa\ta dir/run.sh\n" +
		"link\t0\tbb\ta dir/point\n"
	if rendered != want {
		t.Errorf("formatManifest = %q, want %q", rendered, want)
	}
	parsed := parseManifest([]byte(rendered))
	if len(parsed) != len(manifest) {
		t.Fatalf("parseManifest gave %d entries, want %d",
			len(parsed), len(manifest))
	}
	for index, entry := range parsed {
		if entry != manifest[index] {
			t.Errorf("entry %d = %+v, want %+v", index, entry, manifest[index])
		}
	}
}

// TestParseManifestDropsBadLines checks that malformed lines are
// skipped without losing valid entries.
func TestParseManifestDropsBadLines(t *testing.T) {
	data := "file\t420\taa\tkept.txt\nfile\tnotanumber\tbb\tbad.txt\ndir\t0\n"
	parsed := parseManifest([]byte(data))
	if len(parsed) != 1 || parsed[0].Path != "kept.txt" {
		t.Errorf("parseManifest = %+v, want only kept.txt", parsed)
	}
	if parsed[0].Mode != os.FileMode(0o644) {
		t.Errorf("mode = %v, want 0644", parsed[0].Mode)
	}
}

// TestManifestIndex checks that Index maps each path to its entry.
func TestManifestIndex(t *testing.T) {
	manifest := Manifest{
		{Kind: KindFile, Path: "one"},
		{Kind: KindFile, Path: "two"},
	}
	index := manifest.Index()
	if len(index) != 2 || index["two"].Path != "two" {
		t.Errorf("Index = %+v, want both paths", index)
	}
}

// TestMetadataDocument checks JSON encoding and parsing of metadata,
// including both RFC 3339 and offset time formats.
func TestMetadataDocument(t *testing.T) {
	moment := time.Date(2026, 9, 2, 14, 31, 0, 0, time.FixedZone("", 7200))
	encoded, err := json.Marshal(Metadata{
		Time: moment, Label: "before restore", Entries: 12,
	}.document())
	if err != nil {
		t.Fatalf("marshalling metadata: %v", err)
	}
	if !strings.Contains(string(encoded), `"time":"2026-09-02T14:31:00+02:00"`) {
		t.Errorf("metadata JSON = %s, want an RFC 3339 time", encoded)
	}

	for _, stored := range []string{
		`{"time": "2026-09-02T14:31:00+02:00", "label": "l", "entries": 3}`,
		`{"time": "2026-09-02T14:31:00+0200", "label": "l", "entries": 3}`,
	} {
		var document metadataDocument
		if err := json.Unmarshal([]byte(stored), &document); err != nil {
			t.Fatalf("unmarshalling %s: %v", stored, err)
		}
		metadata := document.metadata()
		if !metadata.Time.Equal(moment) {
			t.Errorf("%s parsed to %v, want %v", stored, metadata.Time, moment)
		}
		if metadata.Label != "l" || metadata.Entries != 3 {
			t.Errorf("%s parsed to %+v", stored, metadata)
		}
	}

	var missing metadataDocument
	if err := json.Unmarshal([]byte(`{"entries": 1}`), &missing); err != nil {
		t.Fatalf("unmarshalling metadata without a time: %v", err)
	}
	if !missing.metadata().Time.IsZero() {
		t.Error("metadata without a time parsed to a non-zero time")
	}
}

// TestFormatCheckpointID checks four-digit zero padding and that
// numbers above 9999 grow without wrapping.
func TestFormatCheckpointID(t *testing.T) {
	cases := map[int]string{1: "0001", 42: "0042", 9999: "9999",
		10000: "10000", 123456: "123456"}
	for number, want := range cases {
		if got := formatCheckpointID(number); got != want {
			t.Errorf("formatCheckpointID(%d) = %q, want %q", number, got, want)
		}
	}
}
