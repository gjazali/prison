package plugin

import (
	"strings"
	"testing"
)

// TestDescribePrintsTheManifest checks the output for a bundled
// inmate and for one with an invalid manifest.
func TestDescribePrintsTheManifest(t *testing.T) {
	registry, installDir, _ := testRegistry(t)
	installInmate(t, installDir, "broken", "[inmate]\nname = \"broken\"\n")

	claude, err := registry.Get("claude")
	if err != nil {
		t.Fatal(err)
	}
	shown := &strings.Builder{}
	Describe(claude, shown)
	for _, want := range []string{
		"name        claude",
		"bundled with prison",
		"approved    yes",
		"claude --dangerously-skip-permissions",
		"api.anthropic.com/v1/",
		"ANTHROPIC_BASE_URL",
		"ANTHROPIC_API_KEY in x-api-key",
		"CLAUDE_CODE_OAUTH_TOKEN in Authorization behind \"Bearer \"",
		"/home/dev/.claude",
		"CLAUDE_CONFIG_DIR=/home/dev/.claude",
		"copies CLAUDE.md, agents/, skills/",
		"CLAUDE_CODE_ACCOUNT_UUID from oauthAccount.accountUuid",
	} {
		if !strings.Contains(shown.String(), want) {
			t.Errorf("the description is missing %q:\n%s", want, shown)
		}
	}

	broken, err := registry.Get("broken")
	if err != nil {
		t.Fatal(err)
	}
	shown.Reset()
	Describe(broken, shown)
	if !strings.Contains(shown.String(), "problem") ||
		!strings.Contains(shown.String(), "command.run") {
		t.Errorf("a broken inmate is described as %q", shown)
	}
}
