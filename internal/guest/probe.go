package guest

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

type probeReport struct {
	Commands map[string]bool `json:"commands"`
	Terminfo bool            `json:"terminfo"`
}

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

func terminfoPresent(name string) bool {
	return exec.Command("infocmp", name).Run() == nil
}

func splitCommaList(text string) []string {
	var items []string
	for _, item := range strings.Split(text, ",") {
		if item = strings.TrimSpace(item); item != "" {
			items = append(items, item)
		}
	}
	return items
}
