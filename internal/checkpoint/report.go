package checkpoint

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"prison/internal/checkpoint/textdiff"
)

const reportLabelWidth = 15

const (
	maximumDiffLinesPerFile = 60
	maximumDiffLinesTotal   = 400
)

const (
	reportContextLines  = 2
	unifiedContextLines = 3
)

const (
	gitModeFile       = "100644"
	gitModeExecutable = "100755"
	gitModeLink       = "120000"
)

const noNewlineMarker = `\ No newline at end of file`

type ReportOptions struct {
	Full bool
}

func (store *Store) DiffTarget(wanted string) (string, error) {
	if wanted == "" {
		head, ok := store.Head()
		if ok {
			wanted = head
		} else {
			identifiers, err := store.identifiers()
			if err != nil {
				return "", err
			}
			if len(identifiers) == 0 {
				return "", fmt.Errorf("no checkpoints for this project. " +
					"Run `prison checkpoint` to take one")
			}
			wanted = identifiers[len(identifiers)-1]
		}
	}
	return store.Resolve(wanted)
}

func (store *Store) Diff(
	wanted string) (Changes, Manifest, Manifest, []Skipped, error) {
	identifier, err := store.DiffTarget(wanted)
	if err != nil {
		return Changes{}, nil, nil, nil, err
	}
	previous, err := store.Manifest(identifier)
	if err != nil {
		return Changes{}, nil, nil, nil, err
	}
	current, skipped, err := store.Scan()
	if err != nil {
		return Changes{}, nil, nil, nil, err
	}
	return Compare(previous, current), previous, current, skipped, nil
}

type lineWriter struct {
	destination io.Writer
	err         error
}

func (writer *lineWriter) line(format string, arguments ...any) {
	if writer.err != nil {
		return
	}
	_, writer.err = fmt.Fprintf(writer.destination, format+"\n", arguments...)
}

func reportRow(label, path, note string) string {
	return fmt.Sprintf("  %-*s%s%s", reportLabelWidth, label, path, note)
}

func WriteReport(destination io.Writer, store *Store, identifier string,
	changes Changes, options ReportOptions) error {
	out := &lineWriter{destination: destination}
	out.line("%d path(s) changed since checkpoint %s.",
		changes.Count(), identifier)
	out.line("")

	for _, entry := range changes.Added {
		out.line("%s", reportRow("added", entry.Path, store.describeAdded(entry)))
	}
	for _, entry := range changes.Removed {
		out.line("%s",
			reportRow("removed", entry.Path, store.describeRemoved(entry)))
	}

	printed, withheld := 0, 0
	for _, change := range changes.Changed {
		before, after := change.Before, change.After
		if before.Kind != after.Kind {
			out.line("%s", reportRow("changed", after.Path,
				fmt.Sprintf(" (%s became %s)", before.Kind, after.Kind)))
			continue
		}
		out.line("%s", reportRow("changed", after.Path,
			describeModeChange(before.Mode, after.Mode)))
		switch after.Kind {
		case KindDirectory:
			continue
		case KindLink:
			previousTarget, previousErr := store.Blob(before.Digest)
			currentTarget, currentErr := os.Readlink(
				filepath.Join(store.Project, after.Path))
			if previousErr != nil || currentErr != nil {
				out.line("    (symlink, target unreadable)")
				continue
			}
			out.line("    (symlink: %s -> %s)", previousTarget, currentTarget)
			continue
		}

		previousLines, previousIsText := store.storedText(before.Digest)
		currentLines, currentIsText := store.projectText(after.Path)
		if !previousIsText || !currentIsText {
			out.line("    (binary file, contents differ)")
			continue
		}
		if !options.Full && printed >= maximumDiffLinesTotal {
			withheld++
			continue
		}
		lines := textdiff.Unified(previousLines, currentLines,
			"a/"+after.Path, "b/"+after.Path, reportContextLines)
		if len(lines) > 2 {
			lines = lines[2:]
		} else {
			lines = nil
		}
		if !options.Full && len(lines) > maximumDiffLinesPerFile {
			remaining := len(lines) - maximumDiffLinesPerFile
			lines = append(lines[:maximumDiffLinesPerFile:maximumDiffLinesPerFile],
				fmt.Sprintf("... %d more lines", remaining))
		}
		for _, line := range lines {
			out.line("    %s", line)
		}
		printed += len(lines)
	}

	out.line("")
	if withheld > 0 {
		out.line("%d more changed path(s) not shown because the diff "+
			"reached %d lines.", withheld, maximumDiffLinesTotal)
		out.line("Run `prison checkpoint diff --full` to see all changes.")
		out.line("")
	}
	out.line("Run `prison checkpoint` to save these changes.")
	return out.err
}

func (store *Store) describeAdded(entry Entry) string {
	switch entry.Kind {
	case KindDirectory:
		return "/"
	case KindLink:
		return " (symlink)"
	}
	if !sniffIsText(filepath.Join(store.Project, entry.Path)) {
		return " (binary)"
	}
	if entry.Mode&0o111 != 0 {
		return " (executable)"
	}
	return ""
}

func (store *Store) describeRemoved(entry Entry) string {
	switch entry.Kind {
	case KindDirectory:
		return "/"
	case KindLink:
		return " (symlink)"
	}
	if !sniffIsText(store.BlobPath(entry.Digest)) {
		return " (binary)"
	}
	return ""
}

func describeModeChange(previous, current os.FileMode) string {
	previousExecutable := previous&0o111 != 0
	currentExecutable := current&0o111 != 0
	switch {
	case currentExecutable && !previousExecutable:
		return " (became executable)"
	case previousExecutable && !currentExecutable:
		return " (no longer executable)"
	}
	return ""
}

func sniffIsText(absolute string) bool {
	handle, err := os.Open(absolute)
	if err != nil {
		return false
	}
	defer handle.Close()
	probe := make([]byte, sniffBytes)
	count, err := io.ReadFull(handle, probe)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return false
	}
	return IsText(probe[:count])
}

func textLines(data []byte) ([]string, bool) {
	if !IsText(data) {
		return nil, false
	}
	lines, _ := textdiff.Split(string(data))
	return lines, true
}

func (store *Store) projectText(relativePath string) ([]string, bool) {
	data, err := os.ReadFile(filepath.Join(store.Project, relativePath))
	if err != nil {
		return nil, false
	}
	return textLines(data)
}

func (store *Store) storedText(digest string) ([]string, bool) {
	data, err := store.Blob(digest)
	if err != nil {
		return nil, false
	}
	return textLines(data)
}

type unifiedStatus int

const (
	unifiedAdded unifiedStatus = iota
	unifiedRemoved
	unifiedChanged
)

// unifiedSide lines keep their terminators, so the diff can mark a missing
// final newline.
type unifiedSide struct {
	lines  []string
	text   bool
	digest string
	mode   string
}

func absentSide() unifiedSide {
	return unifiedSide{text: true}
}

func splitKeepingNewlines(text string) []string {
	lines := strings.SplitAfter(text, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func contentSide(data []byte, digest, mode string) unifiedSide {
	side := unifiedSide{digest: digest, mode: mode}
	if IsText(data) {
		side.text = true
		side.lines = splitKeepingNewlines(string(data))
	}
	return side
}

func gitMode(entry Entry) string {
	if entry.Kind == KindLink {
		return gitModeLink
	}
	if entry.Mode&0o111 != 0 {
		return gitModeExecutable
	}
	return gitModeFile
}

func abbreviateDigest(digest string) string {
	if digest == "" {
		return "0000000"
	}
	if len(digest) > 7 {
		return digest[:7]
	}
	return digest
}

func (store *Store) projectSide(entry Entry) unifiedSide {
	absolute := filepath.Join(store.Project, entry.Path)
	var data []byte
	var err error
	if entry.Kind == KindLink {
		var target string
		target, err = os.Readlink(absolute)
		data = []byte(target)
	} else {
		data, err = os.ReadFile(absolute)
	}
	if err != nil {
		return unifiedSide{digest: entry.Digest, mode: gitMode(entry)}
	}
	return contentSide(data, entry.Digest, gitMode(entry))
}

func (store *Store) storedSide(entry Entry) unifiedSide {
	data, err := store.Blob(entry.Digest)
	if err != nil {
		return unifiedSide{digest: entry.Digest, mode: gitMode(entry)}
	}
	return contentSide(data, entry.Digest, gitMode(entry))
}

// WriteUnified writes only file diffs to destination, so the stream stays
// a valid patch. Directory events and the summary go to notes.
func WriteUnified(destination, notes io.Writer, store *Store,
	identifier string, changes Changes) error {
	out := &lineWriter{destination: destination}
	aside := &lineWriter{destination: notes}
	aside.line("%d path(s) changed since checkpoint %s",
		changes.Count(), identifier)

	for _, entry := range changes.Added {
		if entry.Kind == KindDirectory {
			aside.line("directory added: %s", entry.Path)
			continue
		}
		out.unifiedFile(entry.Path, unifiedAdded,
			absentSide(), store.projectSide(entry))
	}
	for _, entry := range changes.Removed {
		if entry.Kind == KindDirectory {
			aside.line("directory removed: %s", entry.Path)
			continue
		}
		out.unifiedFile(entry.Path, unifiedRemoved,
			store.storedSide(entry), absentSide())
	}
	for _, change := range changes.Changed {
		if change.Before.Kind == KindDirectory ||
			change.After.Kind == KindDirectory {
			aside.line("directory changed: %s", change.After.Path)
			continue
		}
		out.unifiedFile(change.After.Path, unifiedChanged,
			store.storedSide(change.Before), store.projectSide(change.After))
	}
	if out.err != nil {
		return out.err
	}
	return aside.err
}

func (writer *lineWriter) unifiedFile(path string, status unifiedStatus,
	before, after unifiedSide) {
	fromName, toName := "a/"+path, "b/"+path
	writer.line("diff --git a/%s b/%s", path, path)
	indexMode := ""
	switch {
	case status == unifiedAdded:
		fromName = "/dev/null"
		writer.line("new file mode %s", after.mode)
	case status == unifiedRemoved:
		toName = "/dev/null"
		writer.line("deleted file mode %s", before.mode)
	case before.mode != after.mode:
		writer.line("old mode %s", before.mode)
		writer.line("new mode %s", after.mode)
	default:
		indexMode = " " + after.mode
	}
	writer.line("index %s..%s%s", abbreviateDigest(before.digest),
		abbreviateDigest(after.digest), indexMode)
	if !before.text || !after.text {
		writer.line("Binary files %s and %s differ", fromName, toName)
		return
	}
	for _, line := range unifiedLines(before.lines, after.lines,
		fromName, toName) {
		writer.line("%s", line)
	}
}

func unifiedLines(before, after []string, fromName, toName string) []string {
	hunks := textdiff.Hunks(textdiff.Diff(before, after), unifiedContextLines)
	if len(hunks) == 0 {
		return nil
	}
	lines := []string{"--- " + fromName, "+++ " + toName}
	for _, hunk := range hunks {
		lines = append(lines, fmt.Sprintf("@@ -%s +%s @@",
			hunkRange(hunk.AStart, hunk.ACount),
			hunkRange(hunk.BStart, hunk.BCount)))
		for _, edit := range hunk.Lines {
			prefix := " "
			switch edit.Op {
			case textdiff.OpDelete:
				prefix = "-"
			case textdiff.OpInsert:
				prefix = "+"
			}
			if strings.HasSuffix(edit.Line, "\n") {
				lines = append(lines, prefix+strings.TrimSuffix(edit.Line, "\n"))
				continue
			}
			lines = append(lines, prefix+edit.Line, noNewlineMarker)
		}
	}
	return lines
}

// hunkRange follows git. An empty range starts at the line before it, so
// its start stays zero-based.
func hunkRange(start, count int) string {
	switch count {
	case 0:
		return fmt.Sprintf("%d,0", start)
	case 1:
		return fmt.Sprintf("%d", start+1)
	}
	return fmt.Sprintf("%d,%d", start+1, count)
}
