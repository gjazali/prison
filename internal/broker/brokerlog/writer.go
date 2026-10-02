package brokerlog

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"
)

// Writer is safe for concurrent use.
type Writer struct {
	mutex    sync.Mutex
	path     string
	maxBytes int64
	keep     int
	file     *os.File
	size     int64
}

func OpenWriter(path string, maxBytes int64, keep int) (*Writer, error) {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	if keep <= 0 {
		keep = DefaultKeep
	}
	writer := &Writer{path: path, maxBytes: maxBytes, keep: keep}
	if err := writer.open(); err != nil {
		return nil, err
	}
	return writer, nil
}

// open requires the mutex, unless the writer is not yet shared.
func (writer *Writer) open() error {
	file, err := os.OpenFile(writer.path,
		os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("cannot open the broker log %s: %w", writer.path, err)
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return fmt.Errorf("cannot measure the broker log %s: %w", writer.path, err)
	}
	writer.file = file
	writer.size = info.Size()
	return nil
}

func (writer *Writer) Write(entry Entry) error {
	if entry.Time.IsZero() {
		entry.Time = time.Now()
	}
	encoded, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("cannot encode a broker log entry: %w", err)
	}
	line := append(encoded, '\n')
	writer.mutex.Lock()
	defer writer.mutex.Unlock()
	if writer.file == nil {
		return fmt.Errorf("the broker log %s is closed", writer.path)
	}
	if writer.size > 0 && writer.size+int64(len(line)) > writer.maxBytes {
		if err := writer.rotate(); err != nil {
			return err
		}
	}
	written, err := writer.file.Write(line)
	writer.size += int64(written)
	if err != nil {
		return fmt.Errorf("cannot write to the broker log %s: %w", writer.path, err)
	}
	return nil
}

// rotate requires the mutex.
func (writer *Writer) rotate() error {
	if err := writer.file.Close(); err != nil {
		return fmt.Errorf("cannot close the broker log %s: %w", writer.path, err)
	}
	writer.file = nil
	oldest := rotatedPath(writer.path, writer.keep)
	if err := os.Remove(oldest); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("cannot remove the oldest broker log %s: %w", oldest, err)
	}
	for number := writer.keep - 1; number >= 1; number-- {
		from := rotatedPath(writer.path, number)
		to := rotatedPath(writer.path, number+1)
		if err := os.Rename(from, to); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("cannot rotate the broker log %s: %w", from, err)
		}
	}
	if err := os.Rename(writer.path, rotatedPath(writer.path, 1)); err != nil {
		return fmt.Errorf("cannot rotate the broker log %s: %w", writer.path, err)
	}
	return writer.open()
}

func (writer *Writer) Close() error {
	writer.mutex.Lock()
	defer writer.mutex.Unlock()
	if writer.file == nil {
		return nil
	}
	file := writer.file
	writer.file = nil
	if err := file.Close(); err != nil {
		return fmt.Errorf("cannot close the broker log %s: %w", writer.path, err)
	}
	return nil
}

func rotatedPath(path string, number int) string {
	return fmt.Sprintf("%s.%d", path, number)
}
