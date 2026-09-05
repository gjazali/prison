package guest

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

// probeReport is the JSON output of `prison-guest probe`. Reports
// which commands are on PATH and whether a terminal description is
// installed.
type probeReport struct {
	Commands map[string]bool `json:"commands"`
	Terminfo bool            `json:"terminfo"`
}

// runProbe implements `prison-guest probe`. Takes arguments, stdout,
// and stderr. Prints a JSON report to stdout. Returns 0 on success
// or 2 on bad flags.
func runProbe(arguments []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("probe", flag.ContinueOnError)
	flags.SetOutput(stderr)
	commandList := flags.String("command", "",
		"comma-separated commands to look up on PATH")
	terminal := flags.String("term", "",
		"terminal name whose terminfo entry to check")
	if err := flags.Parse(arguments); err != nil {
		return 2
	}
	report := probe(splitCommaList(*commandList), *terminal, exec.LookPath,
		terminfoPresent)
	encoded, err := json.Marshal(report)
	if err != nil {
		fmt.Fprintf(stderr, "prison-guest: %v\n", err)
		return 1
	}
	fmt.Fprintln(stdout, string(encoded))
	return 0
}

// probe builds a probeReport for the given commands and terminal
// name. Uses lookPath to find commands and hasTerminfo to check the
// terminal.
func probe(commands []string, terminal string,
	lookPath func(string) (string, error),
	hasTerminfo func(string) bool) probeReport {
	report := probeReport{Commands: map[string]bool{}}
	for _, command := range commands {
		_, err := lookPath(command)
		report.Commands[command] = err == nil
	}
	if terminal != "" {
		report.Terminfo = hasTerminfo(terminal)
	}
	return report
}

// terminfoPresent returns true if the terminal description for name
// is installed.
func terminfoPresent(name string) bool {
	return exec.Command("infocmp", name).Run() == nil
}

// splitCommaList splits text on commas, trims spaces, and drops
// empty items.
func splitCommaList(text string) []string {
	var items []string
	for _, item := range strings.Split(text, ",") {
		if item = strings.TrimSpace(item); item != "" {
			items = append(items, item)
		}
	}
	return items
}
