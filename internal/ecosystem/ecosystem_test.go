package ecosystem

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"prison/internal/config"
)

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func makeTestCatalogDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"ecosystems/npm.toml": `
name = "npm"
description = "Node (npm)"
group = "javascript-pkg"
priority = 10

[detect]
files = ["package.json"]

[box]
shadow = ["node_modules"]

[setup]
commands = ["npm install"]

[egress]
hosts = ["registry.npmjs.org", "*.npmjs.org"]
`,
		"ecosystems/pnpm.toml": `
name = "pnpm"
description = "Node (pnpm)"
group = "javascript-pkg"
priority = 30

[detect]
files = ["pnpm-lock.yaml"]

[box]
shadow = ["node_modules"]

[setup]
commands = ["pnpm install --frozen-lockfile"]

[checkpoint]
ignore = ["node_modules/", ".pnpm-store/"]

[egress]
hosts = ["registry.npmjs.org", "*.npmjs.org"]
docs = ["pnpm.io", "nodejs.org"]
`,
		"ecosystems/nextjs.toml": `
name = "nextjs"
description = "Next.js"

[detect]
files = ["next.config.*"]

[[detect.match]]
file = "package.json"
pattern = '"next"\s*:'

[checkpoint]
ignore = [".next/"]

[egress]
docs = ["nextjs.org"]
`,
		"ecosystems/uv.toml": `
name = "uv"
description = "Python (uv)"
group = "python-pkg"
priority = 30

[detect]
files = ["uv.lock"]

[box]
shadow = [".venv"]

[setup]
commands = ["uv sync"]

[checkpoint]
ignore = [".venv/", "__pycache__/", "*.pyc"]

[egress]
hosts = ["pypi.org", "files.pythonhosted.org"]
`,
		"ecosystems/pytest.toml": `
name = "pytest"
description = "pytest"

[detect]
files = ["pytest.ini"]

[[detect.match]]
file = "pyproject.toml"
pattern = '(?m)(^\[tool\.pytest|\bpytest\b)'

[checkpoint]
ignore = [".pytest_cache/"]
`,
	}
	for relative, content := range files {
		writeTestFile(t, filepath.Join(dir, filepath.FromSlash(relative)), content)
	}
	return dir
}

func TestParseValidation(t *testing.T) {
	bad := []string{
		`name = "BadName"
[detect]
files = ["go.mod"]`,
		`name = "empty-detect"`,
		`name = "unknown-key"
extra = true
[detect]
files = ["go.mod"]`,
		`name = "bad-shadow"
[detect]
files = ["go.mod"]
[box]
shadow = [".git"]`,
		`name = "bad-host"
[detect]
files = ["go.mod"]
[egress]
hosts = ["https://example.com"]`,
		`name = "bad-regex"
[[detect.match]]
file = "package.json"
pattern = "[unclosed"`,
	}
	for index, sample := range bad {
		if _, err := Parse([]byte(sample)); err == nil {
			t.Errorf("bad case %d: Parse = nil, want error", index)
		}
	}
}

func TestDetectResolvesGroupsAndFrameworks(t *testing.T) {
	catalog, err := LoadCatalog(makeTestCatalogDir(t))
	if err != nil {
		t.Fatalf("LoadCatalog: %v", err)
	}

	project := t.TempDir()
	writeTestFile(t, filepath.Join(project, "package.json"),
		`{"dependencies":{"next":"15.0.0"}}`)
	writeTestFile(t, filepath.Join(project, "pnpm-lock.yaml"), "lockfileVersion: 9\n")
	writeTestFile(t, filepath.Join(project, "uv.lock"), "version = 1\n")
	writeTestFile(t, filepath.Join(project, "pyproject.toml"), `
[project]
name = "demo"
dependencies = ["pytest>=8.0"]
`)

	detected, err := Detect(project, catalog)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	var names []string
	for _, eco := range detected.Matched {
		names = append(names, eco.Name)
	}
	wantNames := []string{"pnpm", "uv", "nextjs", "pytest"}
	if !slices.Equal(names, wantNames) {
		t.Fatalf("matched = %v, want %v", names, wantNames)
	}
	if !slices.Equal(detected.Shadow, []string{"node_modules", ".venv"}) {
		t.Errorf("Shadow = %v", detected.Shadow)
	}
	if !slices.Equal(detected.SetupCommands, []string{"pnpm install --frozen-lockfile", "uv sync"}) {
		t.Errorf("SetupCommands = %v", detected.SetupCommands)
	}
	// Shadowed directories are omitted from checkpoint.ignore to avoid duplication.
	if slices.Contains(detected.CheckpointIgnore, "node_modules/") ||
		slices.Contains(detected.CheckpointIgnore, ".venv/") {
		t.Errorf("CheckpointIgnore kept shadowed dirs: %v", detected.CheckpointIgnore)
	}
	for _, want := range []string{".pnpm-store/", "__pycache__/", "*.pyc", ".next/", ".pytest_cache/"} {
		if !slices.Contains(detected.CheckpointIgnore, want) {
			t.Errorf("CheckpointIgnore = %v, missing %q", detected.CheckpointIgnore, want)
		}
	}
	for _, want := range []string{"registry.npmjs.org", "*.npmjs.org", "pypi.org", "files.pythonhosted.org"} {
		if !slices.Contains(detected.EgressHosts, want) {
			t.Errorf("EgressHosts = %v, missing %q", detected.EgressHosts, want)
		}
	}
	for _, want := range []string{"pnpm.io", "nodejs.org", "nextjs.org"} {
		if !slices.Contains(detected.EgressDocs, want) {
			t.Errorf("EgressDocs = %v, missing %q", detected.EgressDocs, want)
		}
	}
}

func TestProposeProjectConfigNewAndExistingPreservesComments(t *testing.T) {
	detection := &Detection{
		Shadow:           []string{"node_modules"},
		SetupCommands:    []string{"pnpm install --frozen-lockfile"},
		CheckpointIgnore: []string{".pnpm-store/", ".next/"},
		EgressHosts:      []string{"registry.npmjs.org", "*.npmjs.org"},
		EgressDocs:       []string{"pnpm.io", "nextjs.org"},
	}

	created, changed, err := ProposeProjectConfig(nil, detection, ProposeOptions{})
	if err != nil || !changed {
		t.Fatalf("ProposeProjectConfig(nil) = %v, %v", changed, err)
	}
	parsedNew, err := config.LoadProject("prison.toml", created)
	if err != nil {
		t.Fatalf("LoadProject(created): %v\n%s", err, created)
	}
	wantHosts := []string{"registry.npmjs.org", "*.npmjs.org", "pnpm.io", "nextjs.org"}
	if !slices.Equal(parsedNew.Box.Shadow, detection.Shadow) ||
		!slices.Equal(parsedNew.Setup.Commands, detection.SetupCommands) ||
		!slices.Equal(parsedNew.Checkpoint.Ignore, detection.CheckpointIgnore) ||
		!slices.Equal(parsedNew.Egress.Hosts, wantHosts) {
		t.Fatalf("parsedNew = %+v", parsedNew)
	}

	withoutDocs, changed, err := ProposeProjectConfig(nil, detection, ProposeOptions{SkipDocs: true})
	if err != nil || !changed {
		t.Fatalf("ProposeProjectConfig(SkipDocs) = %v, %v", changed, err)
	}
	parsedWithoutDocs, err := config.LoadProject("prison.toml", withoutDocs)
	if err != nil {
		t.Fatalf("LoadProject(withoutDocs): %v", err)
	}
	if !slices.Equal(parsedWithoutDocs.Egress.Hosts, detection.EgressHosts) {
		t.Fatalf("SkipDocs Egress.Hosts = %v, want %v", parsedWithoutDocs.Egress.Hosts, detection.EgressHosts)
	}

	existing := `# Project config comment
[prison]
inmates = ["claude-code"]

[box]
cpus = 6 # keep this comment
shadow = ["custom-out"]

[egress]
# Keep custom API host
hosts = [
  "api.example.com", # custom API
  "registry.npmjs.org"
]

[environment]
APP_ENV = "dev"
`
	updated, changed, err := ProposeProjectConfig([]byte(existing), detection, ProposeOptions{})
	if err != nil || !changed {
		t.Fatalf("ProposeProjectConfig(existing) = %v, %v", changed, err)
	}
	updatedText := string(updated)
	for _, comment := range []string{
		"# Project config comment",
		"# keep this comment",
		"# Keep custom API host",
		"# custom API",
	} {
		if !strings.Contains(updatedText, comment) {
			t.Errorf("lost comment %q in:\n%s", comment, updatedText)
		}
	}
	parsedUpdated, err := config.LoadProject("prison.toml", updated)
	if err != nil {
		t.Fatalf("LoadProject(updated): %v\n%s", err, updatedText)
	}
	if !slices.Equal(parsedUpdated.Box.Shadow, []string{"custom-out", "node_modules"}) {
		t.Errorf("Box.Shadow = %v", parsedUpdated.Box.Shadow)
	}
	if !slices.Equal(parsedUpdated.Setup.Commands, []string{"pnpm install --frozen-lockfile"}) {
		t.Errorf("Setup.Commands = %v", parsedUpdated.Setup.Commands)
	}
	if !slices.Equal(parsedUpdated.Checkpoint.Ignore, []string{".pnpm-store/", ".next/"}) {
		t.Errorf("Checkpoint.Ignore = %v", parsedUpdated.Checkpoint.Ignore)
	}
	if !slices.Equal(parsedUpdated.Egress.Hosts, []string{"api.example.com", "registry.npmjs.org", "*.npmjs.org", "pnpm.io", "nextjs.org"}) {
		t.Errorf("Egress.Hosts = %v", parsedUpdated.Egress.Hosts)
	}

	// Applying the same detection again makes no further edits.
	again, changed, err := ProposeProjectConfig(updated, detection, ProposeOptions{})
	if err != nil || changed || string(again) != updatedText {
		t.Fatalf("second apply: changed=%v err=%v", changed, err)
	}
}

func TestUpdateCatalogFromLocalDirectory(t *testing.T) {
	source := makeTestCatalogDir(t)
	dest := filepath.Join(t.TempDir(), "ecosystems")

	catalog, origin, err := UpdateCatalog(context.Background(), source, "", dest)
	if err != nil {
		t.Fatalf("UpdateCatalog: %v", err)
	}
	if len(catalog.Ecosystems) != 5 {
		t.Fatalf("len(Ecosystems) = %d, want 5", len(catalog.Ecosystems))
	}
	if origin.Source == "" {
		t.Fatal("origin.Source is empty")
	}

	// Updating with empty source reuses the recorded origin.
	reloaded, _, err := UpdateCatalog(context.Background(), "", "", dest)
	if err != nil || len(reloaded.Ecosystems) != 5 {
		t.Fatalf("UpdateCatalog(reuse) = %v, %v", reloaded, err)
	}
}
