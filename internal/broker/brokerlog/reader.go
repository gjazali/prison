package brokerlog

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"
)

// maxLineBytes is the longest line the reader accepts. Longer lines
// are skipped.
const maxLineBytes = 4 << 20

// maxRotatedFiles caps how many rotated files one read will scan.
const maxRotatedFiles = 64

// Filter selects which entries Read returns. Empty strings match all
// values. A zero Since matches any time. A Limit of zero or less
// returns everything.
type Filter struct {
	Kind    string
	Project string
	Secret  string
	Inmate  string
	Since   time.Time
	Limit   int
}

// matches reports whether entry passes every set filter field.
func (filter Filter) matches(entry Entry) bool {
	if filter.Kind != "" && entry.Kind != filter.Kind {
		return false
	}
	if filter.Project != "" && entry.Project != filter.Project {
		return false
	}
	if filter.Secret != "" && entry.Secret != filter.Secret {
		return false
	}
	if filter.Inmate != "" && entry.Inmate != filter.Inmate {
		return false
	}
	if !filter.Since.IsZero() && entry.Time.Before(filter.Since) {
		return false
	}
	return true
}

// Read returns matching entries from path and its rotated files,
// oldest first, keeping only the last Limit entries. Takes a path and
// a filter. Returns the entries and an error if a file exists but
// cannot be read. A missing log returns empty with no error. Invalid
// lines are skipped.
func Read(path string, filter Filter) ([]Entry, error) {
	var matched []Entry
	for _, name := range readOrder(path) {
		found, err := readFile(name, filter)
		if err != nil {
			return nil, err
		}
		matched = append(matched, found...)
	}
	if filter.Limit > 0 && len(matched) > filter.Limit {
		matched = matched[len(matched)-filter.Limit:]
	}
	return matched, nil
}

// readOrder lists existing log files oldest first, live file last.
// Takes a log path. Returns the ordered file paths.
func readOrder(path string) []string {
	var rotated []string
	for number := 1; number <= maxRotatedFiles; number++ {
		name := rotatedPath(path, number)
		if _, err := os.Stat(name); err != nil {
			break
		}
		rotated = append(rotated, name)
	}
	order := make([]string, 0, len(rotated)+1)
	for index := len(rotated) - 1; index >= 0; index-- {
		order = append(order, rotated[index])
	}
	return append(order, path)
}

// readFile returns matching entries from one file in written order.
// Takes a file path and a filter. Returns entries and an error. A
// missing file returns empty with no error. Invalid or oversized lines
// are skipped.
func readFile(path string, filter Filter) ([]Entry, error) {
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("cannot read the broker log %s: %w", path, err)
	}
	defer file.Close()
	var matched []Entry
	lines := bufio.NewReader(file)
	for {
		line, readErr := lines.ReadBytes('\n')
		if len(line) > 0 && len(line) <= maxLineBytes {
			var entry Entry
			if json.Unmarshal(line, &entry) == nil && filter.matches(entry) {
				matched = append(matched, entry)
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return matched, nil
			}
			return nil, fmt.Errorf("cannot read the broker log %s: %w", path, readErr)
		}
	}
}
