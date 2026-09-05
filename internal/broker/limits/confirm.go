package limits

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
)

const (
	// dialogTimeout is the AppleScript dialog timeout.
	dialogTimeout = 60 * time.Second
	// dialogKillAfter is the subprocess kill deadline, slightly past
	// the dialog timeout.
	dialogKillAfter = 70 * time.Second
	// allowButton is the approval button label.
	allowButton = "Allow"
	// denyButton is the refusal button and the default choice.
	denyButton = "Deny"
)

// dialogScriptTemplate is the AppleScript template for the prompt
// dialog.
const dialogScriptTemplate = "button returned of (display dialog %s " +
	"with title \"prison\" " +
	"buttons {%q, %q} " +
	"default button %q " +
	"with icon caution " +
	"giving up after %d)\n"

// commandRunner runs a command and returns its stdout. Abstracted for
// testing.
type commandRunner func(
	ctx context.Context, name string, arguments ...string) ([]byte, error)

// DialogConfirmer shows a macOS confirmation dialog via osascript.
// Only one dialog is shown at a time. Unanswered dialogs time out
// and count as refusals.
type DialogConfirmer struct {
	promptLock      sync.Mutex
	operatingSystem string
	runCommand      commandRunner
}

// NewDialogConfirmer returns a confirmer for the current platform.
// On non-macOS systems, it always refuses.
func NewDialogConfirmer() *DialogConfirmer {
	return &DialogConfirmer{
		operatingSystem: runtime.GOOS,
		runCommand:      runCapturingOutput,
	}
}

// Confirm shows a dialog asking to approve use of a secret. Takes
// the project name, secret name, and a detail string. Returns true
// only if Allow is chosen.
func (confirmer *DialogConfirmer) Confirm(project, secret, detail string) bool {
	if confirmer.operatingSystem != "darwin" {
		return false
	}
	script := dialogScript(describeUse(project, secret, detail))
	confirmer.promptLock.Lock()
	defer confirmer.promptLock.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), dialogKillAfter)
	defer cancel()
	output, err := confirmer.runCommand(ctx, "osascript", "-e", script)
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(output)) == allowButton
}

// runCapturingOutput runs a command and returns its stdout. Returns
// an error on non-zero exit, missing binary, or killed process.
func runCapturingOutput(
	ctx context.Context, name string, arguments ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, arguments...).Output()
}

// describeUse builds the dialog text for a secret use request.
func describeUse(project, secret, detail string) string {
	return fmt.Sprintf("Project %s wants to use the %q secret.\n\n%s",
		project, secret, detail)
}

// dialogScript wraps a question in the AppleScript display template.
func dialogScript(question string) string {
	return fmt.Sprintf(dialogScriptTemplate, quoteForAppleScript(question),
		denyButton, allowButton, denyButton, int(dialogTimeout/time.Second))
}

// quoteForAppleScript escapes text as an AppleScript string literal.
func quoteForAppleScript(text string) string {
	escaped := strings.ReplaceAll(text, "\\", "\\\\")
	escaped = strings.ReplaceAll(escaped, "\"", "\\\"")
	return "\"" + escaped + "\""
}
