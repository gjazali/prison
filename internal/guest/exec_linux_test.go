//go:build linux

package guest

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"os"
	"os/signal"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"prison/internal/machine"
)

type execResult struct {
	stdout, stderr string
	status         int
	failure        string
}

// runTestExec reaps children like PID 1 so that the owned process gets its
// exit code.
func runTestExec(t *testing.T, request machine.ExecRequest,
	input func(host *os.File)) execResult {
	t.Helper()
	runner := &commandRunner{}
	children := make(chan os.Signal, 16)
	signal.Notify(children, unix.SIGCHLD)
	defer signal.Stop(children)
	stopReaping := make(chan struct{})
	defer close(stopReaping)
	go func() {
		for {
			select {
			case <-children:
				reapChildren(runner, 0)
			case <-stopReaping:
				return
			}
		}
	}()
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	host := os.NewFile(uintptr(pair[0]), "host")
	guest := os.NewFile(uintptr(pair[1]), "guest")
	defer host.Close()
	go handleAgentConnection(guest, runner, log.New(io.Discard, "", 0),
		func() {})
	line, _ := json.Marshal(machine.Request{
		Operation: machine.OperationExec, Exec: &request})
	if _, err := host.Write(append(line, '\n')); err != nil {
		t.Fatal(err)
	}
	if input != nil {
		input(host)
	}
	var result execResult
	var stdout, stderr bytes.Buffer
	for {
		kind, payload, err := machine.ReadFrame(host)
		if err != nil {
			t.Fatalf("ReadFrame: %v", err)
		}
		switch kind {
		case machine.FrameStdout:
			stdout.Write(payload)
		case machine.FrameStderr:
			stderr.Write(payload)
		case machine.FrameFailure:
			result.failure = string(payload)
			return result
		case machine.FrameExit:
			result.status, _ = machine.ParseExit(payload)
			result.stdout, result.stderr = stdout.String(), stderr.String()
			return result
		}
	}
}

func TestExecStreamsAndStatus(t *testing.T) {
	result := runTestExec(t, machine.ExecRequest{
		Command:     []string{"sh", "-c", "echo out; echo err >&2; exit 3"},
		Environment: []string{"PATH=/usr/bin:/bin"},
	}, nil)
	if result.stdout != "out\n" || result.stderr != "err\n" ||
		result.status != 3 {
		t.Errorf("exec = %+v, want out, err, and status 3", result)
	}
}

func TestExecForwardsStdin(t *testing.T) {
	result := runTestExec(t, machine.ExecRequest{
		Command:     []string{"cat"},
		Environment: []string{"PATH=/usr/bin:/bin"},
	}, func(host *os.File) {
		machine.WriteFrame(host, machine.FrameStdin, []byte("hello"))
		machine.WriteFrame(host, machine.FrameStdinClose, nil)
	})
	if result.stdout != "hello" || result.status != 0 {
		t.Errorf("exec = %+v, want hello and status 0", result)
	}
}

func TestExecWithTerminal(t *testing.T) {
	result := runTestExec(t, machine.ExecRequest{
		Command:     []string{"sh", "-c", "stty size; test -t 0"},
		Environment: []string{"PATH=/usr/bin:/bin"},
		TTY:         true,
		Rows:        33,
		Columns:     99,
	}, nil)
	if !strings.Contains(result.stdout, "33 99") || result.status != 0 {
		t.Errorf("exec = %+v, want size 33 99 on a terminal", result)
	}
}

func TestExecReportsMissingCommands(t *testing.T) {
	result := runTestExec(t, machine.ExecRequest{
		Command:     []string{"no-such-command"},
		Environment: []string{"PATH=/usr/bin:/bin"},
	}, nil)
	if !strings.Contains(result.failure, "command not found") {
		t.Errorf("exec = %+v, want command not found", result)
	}
}

func TestExecEnvironment(t *testing.T) {
	environment := execEnvironment(
		[]string{"PATH=/bin", "PRISON_TOKEN=secret", "LANG=C"},
		machine.ExecRequest{
			UID:         0,
			Environment: []string{"LANG=C.UTF-8", "HOME=/custom"},
		})
	joined := strings.Join(environment, " ")
	if strings.Contains(joined, "PRISON_TOKEN") {
		t.Errorf("environment = %q, want no token", joined)
	}
	for _, want := range []string{"PATH=/bin", "LANG=C.UTF-8",
		"HOME=/custom"} {
		if !strings.Contains(joined, want) {
			t.Errorf("environment = %q, want %q", joined, want)
		}
	}
	if strings.Count(joined, "LANG=") != 1 {
		t.Errorf("environment = %q, want one LANG", joined)
	}
}

func TestRegisterClaimsAnEarlyExit(t *testing.T) {
	runner := &commandRunner{}
	runner.deliverExit(4242, 7)
	if code := <-runner.register(4242); code != 7 {
		t.Errorf("register(4242) = %d, want 7", code)
	}
	later := runner.register(4343)
	runner.deliverExit(4343, 3)
	if code := <-later; code != 3 {
		t.Errorf("register(4343) = %d, want 3", code)
	}
	runner.ownedLock.Lock()
	left := len(runner.owned) + len(runner.unclaimed)
	runner.ownedLock.Unlock()
	if left != 0 {
		t.Errorf("entries left = %d, want 0", left)
	}
}

func TestExecKeepsBackgroundProcesses(t *testing.T) {
	marker := t.TempDir() + "/alive"
	result := runTestExec(t, machine.ExecRequest{
		Command: []string{"sh", "-c",
			"(sleep 1; touch " + marker + ") >/dev/null 2>&1 &"},
		Environment: []string{"PATH=/usr/bin:/bin"},
	}, nil)
	if result.status != 0 {
		t.Fatalf("exec = %+v, want status 0", result)
	}
	for range 30 {
		if _, err := os.Stat(marker); err == nil {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Error("background process = dead, want alive")
}
