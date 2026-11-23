package applecontainer

import (
	"context"
	"fmt"
	"io"
	"strings"
)

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
	driver.reportAnchor(ctx, w)
}

func (driver *Driver) reportAnchor(ctx context.Context, w io.Writer) {
	fmt.Fprintf(w, "\n  anchor\n")
	info, err := driver.Anchor().Status(ctx)
	if err != nil {
		fmt.Fprintf(w, "    cannot inspect %s: %v\n", AnchorName, err)
		return
	}
	switch {
	case !info.Exists:
		fmt.Fprintf(w, "    absent (%s; run `prison up` or `prison host anchor`)\n",
			AnchorName)
	case !info.Running:
		fmt.Fprintf(w, "    stopped (%s; run `prison up` or `prison host anchor`)\n",
			AnchorName)
	case info.Network != AnchorNetwork:
		fmt.Fprintf(w, "    running on %q instead of %q (%s; run `prison host anchor`)\n",
			info.Network, AnchorNetwork, AnchorName)
	case info.Address != "":
		fmt.Fprintf(w, "    running (%s on %s, %s)\n",
			AnchorName, info.Network, info.Address)
	default:
		fmt.Fprintf(w, "    running (%s on %s)\n", AnchorName, info.Network)
	}
}

func (driver *Driver) dumpSection(
	ctx context.Context, w io.Writer,
	heading string, name string, arguments ...string,
) {
	fmt.Fprintf(w, "\n  %s\n", heading)
	stdout, stderr, status, err := driver.runCapturing(
		ctx, name, arguments...)
	if err != nil {
		fmt.Fprintf(w, "    cannot run: %v\n", err)
		return
	}
	text := strings.TrimRight(string(stdout), "\n")
	if status != 0 || text == "" {
		text = strings.TrimRight(string(stderr), "\n")
	}
	if text == "" {
		fmt.Fprintf(w, "    (no output)\n")
		return
	}
	for _, line := range strings.Split(text, "\n") {
		fmt.Fprintf(w, "    %s\n", line)
	}
}
