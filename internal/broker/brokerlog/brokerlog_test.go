package brokerlog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// baseTime is the fixed timestamp tests count from.
var baseTime = time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)

// writeEntries opens a writer at path and writes all given entries.
// Fails the test on any error.
func writeEntries(t *testing.T, path string, maxBytes int64, keep int,
	entries ...Entry) {
	t.Helper()
	writer, err := OpenWriter(path, maxBytes, keep)
	if err != nil {
		t.Fatalf("OpenWriter: %v", err)
	}
	for _, entry := range entries {
		if err := writer.Write(entry); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// TestWriteLine checks that a written line is valid JSON, omits
// empty optional fields, and fills in a zero timestamp.
func TestWriteLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "broker.log")
	before := time.Now()
	writeEntries(t, path, 0, 0, Entry{
		Kind:    KindEgress,
		Project: "alpha",
		Host:    "example.com",
		Port:    443,
		Outcome: "allowed",
		Bytes:   1024,
	})
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	lines := strings.Split(strings.TrimSuffix(string(content), "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("wrote %d lines, want one", len(lines))
	}
	var fields map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &fields); err != nil {
		t.Fatalf("the line is not a JSON object: %v", err)
	}
	absentFields := []string{"secret", "inmate", "status", "input_tokens",
		"error"}
	for _, absent := range absentFields {
		if _, present := fields[absent]; present {
			t.Errorf("%q was written although it is empty", absent)
		}
	}
	var entry Entry
	if err := json.Unmarshal([]byte(lines[0]), &entry); err != nil {
		t.Fatalf("the line is not an entry: %v", err)
	}
	if entry.Time.Before(before) {
		t.Errorf("the timestamp %v predates the write", entry.Time)
	}
	if entry.Bytes != 1024 || entry.Port != 443 {
		t.Errorf("the numeric fields did not round trip: %+v", entry)
	}
}

// TestWriteKeepsTokenZero checks that a zero token count round-trips
// as zero, not nil.
func TestWriteKeepsTokenZero(t *testing.T) {
	path := filepath.Join(t.TempDir(), "broker.log")
	zero := 0
	writeEntries(t, path, 0, 0, Entry{
		Time: baseTime, Kind: KindRoute, InputTokens: &zero,
	})
	entries, err := Read(path, Filter{})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(entries) != 1 || entries[0].InputTokens == nil {
		t.Fatalf("the token count did not round trip: %+v", entries)
	}
	if *entries[0].InputTokens != 0 || entries[0].OutputTokens != nil {
		t.Errorf("token counts are %+v, want only a reported zero", entries[0])
	}
}

// TestRotation checks that full log files rotate through numbered
// names and the oldest is dropped.
func TestRotation(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "broker.log")
	writer, err := OpenWriter(path, 200, 2)
	if err != nil {
		t.Fatalf("OpenWriter: %v", err)
	}
	defer writer.Close()
	for count := 0; count < 12; count++ {
		entry := Entry{Time: baseTime.Add(time.Duration(count) * time.Second),
			Kind: KindEgress, Project: "alpha", Host: "example.com"}
		if err := writer.Write(entry); err != nil {
			t.Fatalf("Write %d: %v", count, err)
		}
	}
	for _, name := range []string{"broker.log", "broker.log.1", "broker.log.2"} {
		info, err := os.Stat(filepath.Join(directory, name))
		if err != nil {
			t.Fatalf("Stat %s: %v", name, err)
		}
		if info.Size() > 200 {
			t.Errorf("%s is %d bytes, over the limit", name, info.Size())
		}
	}
	_, err = os.Stat(filepath.Join(directory, "broker.log.3"))
	if !os.IsNotExist(err) {
		t.Errorf("broker.log.3 exists, want the oldest file dropped")
	}
}

// TestReadAcrossRotatedFiles checks that reading spans all rotated
// files in chronological order with no gaps or duplicates.
func TestReadAcrossRotatedFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "broker.log")
	writer, err := OpenWriter(path, 200, 8)
	if err != nil {
		t.Fatalf("OpenWriter: %v", err)
	}
	defer writer.Close()
	const total = 9
	for count := 0; count < total; count++ {
		entry := Entry{Time: baseTime.Add(time.Duration(count) * time.Second),
			Kind: KindEgress, Project: "alpha", Host: "example.com"}
		if err := writer.Write(entry); err != nil {
			t.Fatalf("Write %d: %v", count, err)
		}
	}
	if _, err := os.Stat(path + ".2"); err != nil {
		t.Fatalf("the log did not rotate more than once: %v", err)
	}
	entries, err := Read(path, Filter{})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(entries) != total {
		t.Fatalf("read %d entries, want %d", len(entries), total)
	}
	for index, entry := range entries {
		want := baseTime.Add(time.Duration(index) * time.Second)
		if !entry.Time.Equal(want) {
			t.Fatalf("entry %d is at %v, want %v", index, entry.Time, want)
		}
	}
}

// TestReadFilters checks filtering by kind, project, secret, inmate,
// since time, and limit.
func TestReadFilters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "broker.log")
	writeEntries(t, path, 120, 6,
		Entry{Time: baseTime, Kind: KindEgress, Project: "alpha",
			Host: "a.example"},
		Entry{Time: baseTime.Add(time.Second), Kind: KindSign,
			Project: "alpha", Secret: "deploy"},
		Entry{Time: baseTime.Add(2 * time.Second), Kind: KindSign,
			Project: "beta", Secret: "deploy"},
		Entry{Time: baseTime.Add(3 * time.Second), Kind: KindInmate,
			Project: "alpha", Inmate: "claude"},
		Entry{Time: baseTime.Add(4 * time.Second), Kind: KindRoute,
			Project: "alpha", Secret: "anthropic"},
	)
	cases := []struct {
		name   string
		filter Filter
		want   int
	}{
		{name: "everything", want: 5},
		{name: "by kind", filter: Filter{Kind: KindSign}, want: 2},
		{name: "by project", filter: Filter{Project: "alpha"}, want: 4},
		{name: "by secret", filter: Filter{Secret: "deploy"}, want: 2},
		{name: "by inmate", filter: Filter{Inmate: "claude"}, want: 1},
		{name: "kind and project", want: 1,
			filter: Filter{Kind: KindSign, Project: "beta"}},
		{name: "since", want: 2,
			filter: Filter{Since: baseTime.Add(3 * time.Second)}},
		{name: "no match", filter: Filter{Project: "gamma"}, want: 0},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			entries, err := Read(path, testCase.filter)
			if err != nil {
				t.Fatalf("Read: %v", err)
			}
			if len(entries) != testCase.want {
				t.Fatalf("read %d entries, want %d", len(entries), testCase.want)
			}
		})
	}
	entries, err := Read(path, Filter{Limit: 2})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("the limit returned %d entries, want two", len(entries))
	}
	if !entries[0].Time.Equal(baseTime.Add(3 * time.Second)) {
		t.Fatalf("the limit did not take the last two entries: %+v", entries)
	}
}

// TestReadSkipsMalformedLines checks that bad lines are skipped
// without losing valid entries.
func TestReadSkipsMalformedLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "broker.log")
	writeEntries(t, path, 0, 0,
		Entry{Time: baseTime, Kind: KindEgress, Project: "alpha"})
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	damaged := "{\"kind\": \"egr\n not json at all\n"
	if _, err := file.WriteString(damaged); err != nil {
		t.Fatalf("WriteString: %v", err)
	}
	file.Close()
	writeEntries(t, path, 0, 0,
		Entry{Time: baseTime.Add(time.Second), Kind: KindSign, Project: "beta"})
	entries, err := Read(path, Filter{})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("read %d entries, want the two valid ones", len(entries))
	}
}

// TestReadMissingLog checks that a missing log file returns an empty
// slice, not an error.
func TestReadMissingLog(t *testing.T) {
	entries, err := Read(filepath.Join(t.TempDir(), "broker.log"), Filter{})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("read %d entries from a missing log", len(entries))
	}
}

// TestWriteAfterClose checks that writing to a closed writer returns
// an error.
func TestWriteAfterClose(t *testing.T) {
	path := filepath.Join(t.TempDir(), "broker.log")
	writer, err := OpenWriter(path, 0, 0)
	if err != nil {
		t.Fatalf("OpenWriter: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("the second Close failed: %v", err)
	}
	if err := writer.Write(Entry{Kind: KindEgress}); err == nil {
		t.Fatal("writing to a closed log succeeded")
	}
}

// TestOpenWriterDefaults checks that zero values use package defaults
// and that an invalid path returns an error.
func TestOpenWriterDefaults(t *testing.T) {
	directory := t.TempDir()
	writer, err := OpenWriter(filepath.Join(directory, "broker.log"), 0, 0)
	if err != nil {
		t.Fatalf("OpenWriter: %v", err)
	}
	defer writer.Close()
	if writer.maxBytes != DefaultMaxBytes || writer.keep != DefaultKeep {
		t.Errorf("defaults are %d and %d, want %d and %d",
			writer.maxBytes, writer.keep, DefaultMaxBytes, DefaultKeep)
	}
	missing := filepath.Join(directory, "missing", "broker.log")
	if _, err := OpenWriter(missing, 0, 0); err == nil {
		t.Error("opening a log under a missing directory succeeded")
	}
}
