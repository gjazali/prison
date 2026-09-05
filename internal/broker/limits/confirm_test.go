package limits

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

// newTestConfirmer returns a confirmer that fakes macOS and uses the
// given runner instead of showing a dialog.
func newTestConfirmer(run commandRunner) *DialogConfirmer {
	return &DialogConfirmer{operatingSystem: "darwin", runCommand: run}
}

// TestConfirmScript checks the generated AppleScript, including
// quote and backslash escaping.
func TestConfirmScript(t *testing.T) {
	var command []string
	confirmer := newTestConfirmer(
		func(ctx context.Context, name string,
			arguments ...string) ([]byte, error) {
			command = append([]string{name}, arguments...)
			return []byte("Allow\n"), nil
		})
	if !confirmer.Confirm("alpha", "deploy", "Running \\ curl \"once\"") {
		t.Fatal("an Allow answer was read as a refusal")
	}
	if len(command) != 3 || command[0] != "osascript" || command[1] != "-e" {
		t.Fatalf("ran %q, want osascript -e with one script", command)
	}
	want := "button returned of (display dialog " +
		"\"Project alpha wants to use the \\\"deploy\\\" secret.\n\n" +
		"Running \\\\ curl \\\"once\\\"\" " +
		"with title \"prison\" " +
		"buttons {\"Deny\", \"Allow\"} " +
		"default button \"Deny\" " +
		"with icon caution " +
		"giving up after 60)\n"
	if command[2] != want {
		t.Errorf("script is\n%q\nwant\n%q", command[2], want)
	}
}

// TestConfirmAnswers checks that only "Allow" approves. Failed or
// timed-out dialogs refuse.
func TestConfirmAnswers(t *testing.T) {
	cases := []struct {
		name   string
		output string
		err    error
		want   bool
	}{
		{name: "allow", output: "Allow\n", want: true},
		{name: "deny", output: "Deny\n"},
		{name: "dismissed", output: ""},
		{name: "gave up", output: "false\n"},
		{name: "failed", output: "Allow\n", err: errors.New("exit status 1")},
		{name: "timed out", err: context.DeadlineExceeded},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			confirmer := newTestConfirmer(
				func(ctx context.Context, name string,
					arguments ...string) ([]byte, error) {
					return []byte(testCase.output), testCase.err
				})
			got := confirmer.Confirm("alpha", "deploy", "detail")
			if got != testCase.want {
				t.Errorf("Confirm returned %v, want %v", got, testCase.want)
			}
		})
	}
}

// TestConfirmRefusesOffMacOS checks that non-macOS hosts refuse
// without running any command.
func TestConfirmRefusesOffMacOS(t *testing.T) {
	ran := false
	confirmer := newTestConfirmer(
		func(ctx context.Context, name string,
			arguments ...string) ([]byte, error) {
			ran = true
			return []byte("Allow\n"), nil
		})
	confirmer.operatingSystem = "linux"
	if confirmer.Confirm("alpha", "deploy", "detail") {
		t.Error("a host without a dialog approved the use")
	}
	if ran {
		t.Error("a command ran on a host that cannot show a dialog")
	}
}

// TestConfirmShowsOneDialogAtATime checks that concurrent requests
// serialize so only one dialog is open at a time.
func TestConfirmShowsOneDialogAtATime(t *testing.T) {
	var inFlight, peak int64
	confirmer := newTestConfirmer(
		func(ctx context.Context, name string,
			arguments ...string) ([]byte, error) {
			current := atomic.AddInt64(&inFlight, 1)
			for {
				highest := atomic.LoadInt64(&peak)
				swapped := atomic.CompareAndSwapInt64(&peak, highest, current)
				if current <= highest || swapped {
					break
				}
			}
			atomic.AddInt64(&inFlight, -1)
			return []byte("Allow\n"), nil
		})
	var waiting sync.WaitGroup
	for request := 0; request < 8; request++ {
		waiting.Add(1)
		go func() {
			defer waiting.Done()
			confirmer.Confirm("alpha", "deploy", "detail")
		}()
	}
	waiting.Wait()
	if peak != 1 {
		t.Errorf("%d dialogs overlapped, want one at a time", peak)
	}
}

// TestNewDialogConfirmer checks that the real constructor sets both
// the runner and the operating system.
func TestNewDialogConfirmer(t *testing.T) {
	confirmer := NewDialogConfirmer()
	if confirmer.runCommand == nil || confirmer.operatingSystem == "" {
		t.Fatal("NewDialogConfirmer left the runner or the host unset")
	}
}
