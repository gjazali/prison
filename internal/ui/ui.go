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

// ExitError carries an exit status. An empty `Message` means that the caller
// printed the reason.
type ExitError struct {
	Status  int
	Message string
}

func (e *ExitError) Error() string { return e.Message }

func Exit(status int, format string, arguments ...any) error {
	return &ExitError{Status: status, Message: fmt.Sprintf(format, arguments...)}
}

func Warn(format string, arguments ...any) {
	fmt.Fprintf(os.Stderr, "warn: "+format+"\n", arguments...)
}

func Progress(format string, arguments ...any) {
	fmt.Fprintf(os.Stderr, "==> "+format+"\n", arguments...)
}

func Summary(key string, format string, arguments ...any) {
	fmt.Fprintf(os.Stdout, "==> %-9s %s\n", key, fmt.Sprintf(format, arguments...))
}

func StdinIsTerminal() bool {
	return term.IsTerminal(int(os.Stdin.Fd()))
}

func StdoutIsTerminal() bool {
	return term.IsTerminal(int(os.Stdout.Fd()))
}

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

func ReadSecret(prompt string) (string, error) {
	if !StdinIsTerminal() {
		return "", fmt.Errorf("%s needs a terminal", strings.TrimSuffix(prompt, ": "))
	}
	fmt.Fprint(os.Stderr, prompt)
	value, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	return string(value), nil
}

func TerminalSize() (rows, columns int) {
	columns, rows, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil || rows <= 0 || columns <= 0 {
		return 24, 80
	}
	return rows, columns
}

func Table(w io.Writer, rows [][]string) error {
	writer := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, row := range rows {
		if _, err := fmt.Fprintln(writer, strings.Join(row, "\t")); err != nil {
			return err
		}
	}
	return writer.Flush()
}

func Indent(text, prefix string) string {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	for i, line := range lines {
		lines[i] = prefix + line
	}
	return strings.Join(lines, "\n")
}
