package session

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type setupRule struct {
	Marker  string
	Tool    string
	Command string
}

// setupRuleGroups has one group per ecosystem, in priority order. Only the
// first match in a group runs.
var setupRuleGroups = [][]setupRule{
	{
		{"package-lock.json", "npm", "npm ci"},
		{"pnpm-lock.yaml", "pnpm", "pnpm install --frozen-lockfile"},
		{"yarn.lock", "yarn", "yarn install"},
		{"package.json", "npm", "npm install"},
	},
	{
		{"uv.lock", "uv", "uv sync"},
		{"poetry.lock", "poetry", "poetry install"},
		{"requirements.txt", "pip3", "pip3 install -r requirements.txt"},
	},
	{{"go.mod", "go", "go mod download"}},
	{{"Cargo.toml", "cargo", "cargo fetch"}},
	{{"Gemfile.lock", "bundle", "bundle install"}},
}

// DetectSetupCommands returns the install commands and the warnings about
// missing tools.
func (s *Session) DetectSetupCommands(ctx context.Context) ([]string, []string, error) {
	type candidate struct {
		tool    string
		command string
	}
	var candidates []candidate
	toolSet := map[string]bool{}
	for _, group := range setupRuleGroups {
		for _, rule := range group {
			if !fileExists(filepath.Join(s.Directory, rule.Marker)) {
				continue
			}
			candidates = append(candidates, candidate{rule.Tool, rule.Command})
			toolSet[rule.Tool] = true
			break
		}
	}
	if len(candidates) == 0 {
		return nil, nil, nil
	}
	tools := make([]string, 0, len(toolSet))
	for tool := range toolSet {
		tools = append(tools, tool)
	}
	probe, err := s.Probe(ctx, tools, "")
	if err != nil {
		return nil, nil, err
	}
	var commands []string
	var missing []string
	for _, item := range candidates {
		if probe.Has(item.tool) {
			commands = append(commands, item.command)
			continue
		}
		missing = append(missing, fmt.Sprintf(
			"the box cannot run `%s`. "+
				"Set [setup] commands in prison.toml", item.command))
	}
	return commands, missing, nil
}

func (s *Session) SetupCommands(ctx context.Context) ([]string, []string, error) {
	if s.Config != nil && len(s.Config.Setup.Commands) > 0 {
		return append([]string(nil), s.Config.Setup.Commands...), nil, nil
	}
	return s.DetectSetupCommands(ctx)
}

func SetupSignature(commands []string) string {
	if len(commands) == 0 {
		return ""
	}
	digest := sha256.Sum256([]byte(strings.Join(commands, "\n")))
	return hex.EncodeToString(digest[:])[:16]
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func directoryExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
