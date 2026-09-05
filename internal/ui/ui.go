// Package ui provides output helpers for commands: tables, prompts,
// progress lines, warnings, and exit errors.
package ui

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"golang.org/x/term"
)

// ExitError is an error with an exit status code. An empty Message
// means the reason was already printed.
type ExitError struct {
	Status  int
	Message string
}

// Error returns the message string.
func (e *ExitError) Error() string { return e.Message }

// Exit returns an ExitError. Takes a status code and a format string
// with arguments.
func Exit(status int, format string, arguments ...any) error {
	return &ExitError{Status: status, Message: fmt.Sprintf(format, arguments...)}
}

// Warn prints a warning line to stderr. Takes a format string and
// arguments.
func Warn(format string, arguments ...any) {
	fmt.Fprintf(os.Stderr, "warn: "+format+"\n", arguments...)
}

// Progress prints a `==> ` progress line to stderr. Takes a format
// string and arguments.
func Progress(format string, arguments ...any) {
	fmt.Fprintf(os.Stderr, "==> "+format+"\n", arguments...)
}

// Summary prints an aligned `==> key  value` line to stdout. Takes a
// key, a format string, and arguments.
func Summary(key string, format string, arguments ...any) {
	fmt.Fprintf(os.Stdout, "==> %-9s %s\n", key, fmt.Sprintf(format, arguments...))
}

// StdinIsTerminal reports whether stdin is a terminal. Returns true
// or false.
func StdinIsTerminal() bool {
	return term.IsTerminal(int(os.Stdin.Fd()))
}

// StdoutIsTerminal reports whether stdout is a terminal. Returns true
// or false.
func StdoutIsTerminal() bool {
	return term.IsTerminal(int(os.Stdout.Fd()))
}

// Confirm asks a yes/no question on stderr. Takes the question text.
// Returns true for "y" or "yes", false otherwise. Returns false
// without asking if stdin is not a terminal.
func Confirm(question string) bool {
	if !StdinIsTerminal() {
		return false
	}
	fmt.Fprintf(os.Stderr, "%s [y/N] ", question)
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil && line == "" {
		return false
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes"
}

// ReadSecret prompts on stderr and reads input with echo off. Takes
// a prompt string. Returns the input or an error. Fails if stdin is
// not a terminal.
func ReadSecret(prompt string) (string, error) {
	if !StdinIsTerminal() {
		return "", fmt.Errorf("%s needs a terminal to read from", strings.TrimSuffix(prompt, ": "))
	}
	fmt.Fprint(os.Stderr, prompt)
	value, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	return string(value), nil
}

// TerminalSize returns the rows and columns of stdout's terminal.
// Falls back to 24x80 if stdout is not a terminal.
func TerminalSize() (rows, columns int) {
	columns, rows, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil || rows <= 0 || columns <= 0 {
		return 24, 80
	}
	return rows, columns
}

// Table writes aligned columns to w. Takes a writer and rows of
// strings. The first row is the header. Returns an error on write
// failure.
func Table(w io.Writer, rows [][]string) error {
	writer := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, row := range rows {
		if _, err := fmt.Fprintln(writer, strings.Join(row, "\t")); err != nil {
			return err
		}
	}
	return writer.Flush()
}

// Indent adds a prefix to every line. Takes the text and the prefix.
// Returns the indented text.
func Indent(text, prefix string) string {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	for i, line := range lines {
		lines[i] = prefix + line
	}
	return strings.Join(lines, "\n")
}
