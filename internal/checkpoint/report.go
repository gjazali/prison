package checkpoint

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"prison/internal/checkpoint/textdiff"
)

// reportLabelWidth is the column width for report verbs. Keeps paths aligned.
const reportLabelWidth = 15

// Line caps for the human report.
const (
	maximumDiffLinesPerFile = 60
	maximumDiffLinesTotal   = 400
)

// Context lines around each change in each report format.
const (
	reportContextLines  = 2
	unifiedContextLines = 3
)

// Git mode strings for each entry kind.
const (
	gitModeFile       = "100644"
	gitModeExecutable = "100755"
	gitModeLink       = "120000"
)

// noNewlineMarker is the line git writes when a side has no final
// newline.
const noNewlineMarker = `\ No newline at end of file`

// ReportOptions controls the human report. Full prints every diff body
// without line caps.
type ReportOptions struct {
	Full bool
}

// DiffTarget returns the resolved checkpoint identifier for a diff.
// Takes the wanted identifier in short or padded form. Falls back to
// HEAD, then to the newest checkpoint. Returns an error when there are
// no checkpoints or the identifier does not match one.
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
				return "", fmt.Errorf("no checkpoints for this project yet; " +
					"`prison checkpoint` takes one")
			}
			wanted = identifiers[len(identifiers)-1]
		}
	}
	return store.Resolve(wanted)
}

// Diff compares the working tree against a checkpoint. Takes a
// checkpoint identifier. Returns the changes, the stored manifest, the
// current manifest, and any skipped paths. Does not write.
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

// lineWriter prints lines to a destination. Keeps the first error so
// callers can write a whole report without checking each call.
type lineWriter struct {
	destination io.Writer
	err         error
}

// line prints one formatted line. After the first error, further calls
// are dropped.
func (writer *lineWriter) line(format string, arguments ...any) {
	if writer.err != nil {
		return
	}
	_, writer.err = fmt.Fprintf(writer.destination, format+"\n", arguments...)
}

// reportRow formats one "verb path note" line. Pads the verb to
// reportLabelWidth.
func reportRow(label, path, note string) string {
	return fmt.Sprintf("  %-*s%s%s", reportLabelWidth, label, path, note)
}

// WriteReport prints the human-readable diff report. Takes a
// destination writer, the store, a resolved identifier, the changes,
// and options. Returns the first write error.
func WriteReport(destination io.Writer, store *Store, identifier string,
	changes Changes, options ReportOptions) error {
	out := &lineWriter{destination: destination}
	out.line("%d path(s) changed since checkpoint %s.",
		changes.Count(), identifier)
	out.line("The project is mounted writable, so these are on your disk now.")
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
		out.line("%d more changed path(s) are listed above without their "+
			"contents,", withheld)
		out.line("since the diff had already run to %d lines. "+
			"`prison checkpoint diff --full`", maximumDiffLinesTotal)
		out.line("prints all of it, and `[checkpoint] pager` in " +
			"~/.prison/config.toml pages it.")
		out.line("")
	}
	out.line("`prison checkpoint` records this as a new checkpoint.")
	return out.err
}

// describeAdded returns the annotation for an added path. Returns a
// slash for directories, or a marker like "(symlink)", "(binary)", or
// "(executable)" for other kinds.
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

// describeRemoved returns the annotation for a removed path. Returns
// a slash for directories, or a marker for symlinks and binary files.
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

// describeModeChange returns a note when the executable bit changed
// between two modes. Returns empty if it did not.
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

// sniffIsText reports whether a file looks like text. Reads only the
// first few bytes. Returns false for unreadable paths.
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

// textLines splits data into lines and reports whether it is text.
// Returns nil and false for binary data.
func textLines(data []byte) ([]string, bool) {
	if !IsText(data) {
		return nil, false
	}
	lines, _ := textdiff.Split(string(data))
	return lines, true
}

// projectText returns a project file's contents as lines. Takes a
// project-relative path. Returns false if unreadable or binary.
func (store *Store) projectText(relativePath string) ([]string, bool) {
	data, err := os.ReadFile(filepath.Join(store.Project, relativePath))
	if err != nil {
		return nil, false
	}
	return textLines(data)
}

// storedText returns a stored object's contents as lines. Takes a
// digest. Returns false if missing or binary.
func (store *Store) storedText(digest string) ([]string, bool) {
	data, err := store.Blob(digest)
	if err != nil {
		return nil, false
	}
	return textLines(data)
}

// unifiedStatus is the shape of a unified diff file header.
type unifiedStatus int

// The three header shapes: added, removed, or changed.
const (
	unifiedAdded unifiedStatus = iota
	unifiedRemoved
	unifiedChanged
)

// unifiedSide is one side of a file in the unified stream. Lines keep
// their terminators. An absent side has no lines, counts as text, and
// has an empty digest.
type unifiedSide struct {
	lines  []string
	text   bool
	digest string
	mode   string
}

// absentSide returns the side for a file that does not exist.
func absentSide() unifiedSide {
	return unifiedSide{text: true}
}

// splitKeepingNewlines breaks text into lines that keep their
// terminators. Empty text yields no lines.
func splitKeepingNewlines(text string) []string {
	lines := strings.SplitAfter(text, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// contentSide builds a unified side from raw bytes, a digest, and a
// git mode. Binary content has no lines.
func contentSide(data []byte, digest, mode string) unifiedSide {
	side := unifiedSide{digest: digest, mode: mode}
	if IsText(data) {
		side.text = true
		side.lines = splitKeepingNewlines(string(data))
	}
	return side
}

// gitMode returns the git mode-line value for an entry.
func gitMode(entry Entry) string {
	if entry.Kind == KindLink {
		return gitModeLink
	}
	if entry.Mode&0o111 != 0 {
		return gitModeExecutable
	}
	return gitModeFile
}

// abbreviateDigest shortens a digest for an index line. An empty
// digest becomes the all-zero form.
func abbreviateDigest(digest string) string {
	if digest == "" {
		return "0000000"
	}
	if len(digest) > 7 {
		return digest[:7]
	}
	return digest
}

// projectSide returns the unified side for an entry as it is on disk.
// An unreadable path is treated as binary.
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

// storedSide returns the unified side for an entry from the object
// store. A missing object is treated as binary.
func (store *Store) storedSide(entry Entry) unifiedSide {
	data, err := store.Blob(entry.Digest)
	if err != nil {
		return unifiedSide{digest: entry.Digest, mode: gitMode(entry)}
	}
	return contentSide(data, entry.Digest, gitMode(entry))
}

// WriteUnified prints changes since a checkpoint as a git-style
// unified diff. Takes the diff destination, a notes writer, the store,
// the resolved identifier, and the changes. Directory events and the
// summary go to notes. Returns the first write error.
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

// unifiedFile prints one path's git-style diff with header. Takes the
// path, header shape, and both sides.
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

// unifiedLines renders the diff hunks between two sides. Returns nil
// when the sides are equal.
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

// hunkRange formats one side of an "@@" header. Takes a zero-based
// start and count. Returns the one-based range string.
func hunkRange(start, count int) string {
	switch count {
	case 0:
		return fmt.Sprintf("%d,0", start)
	case 1:
		return fmt.Sprintf("%d", start+1)
	}
	return fmt.Sprintf("%d,%d", start+1, count)
}
