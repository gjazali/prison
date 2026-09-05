package applecontainer

import (
	"context"
	"fmt"
	"io"
	"strings"
)

// Doctor writes diagnostic information about the backend to w.
func (driver *Driver) Doctor(ctx context.Context, w io.Writer) {
	path, err := driver.lookPath(containerBinary)
	if err != nil {
		fmt.Fprintf(w, "  container  not found on PATH\n")
		return
	}
	fmt.Fprintf(w, "  container  %s\n", path)
	driver.dumpSection(ctx, w, "version",
		containerBinary, "--version")
	driver.dumpSection(ctx, w, "system status",
		containerBinary, "system", "status")
	driver.dumpSection(ctx, w, "container ls --all",
		containerBinary, "ls", "--all")
	driver.dumpSection(ctx, w, "networks",
		containerBinary, "network", "ls")
	driver.dumpSection(ctx, w, "dns domains",
		containerBinary, "system", "dns", "list")
}

// dumpSection runs a command and writes its output under a heading.
// Prints the failure message if the command fails.
func (driver *Driver) dumpSection(
	ctx context.Context, w io.Writer,
	heading string, name string, arguments ...string,
) {
	fmt.Fprintf(w, "\n  %s\n", heading)
	stdout, stderr, status, err := driver.runCapturing(
		ctx, name, arguments...)
	if err != nil {
		fmt.Fprintf(w, "    could not run: %v\n", err)
		return
	}
	text := strings.TrimRight(string(stdout), "\n")
	if status != 0 || text == "" {
		text = strings.TrimRight(string(stderr), "\n")
	}
	if text == "" {
		fmt.Fprintf(w, "    (nothing)\n")
		return
	}
	for _, line := range strings.Split(text, "\n") {
		fmt.Fprintf(w, "    %s\n", line)
	}
}
