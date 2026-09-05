package guest

import (
	"fmt"
	"os/exec"
	"strings"
	"sync"
)

// commandRunner runs helper programs with a lock that prevents the
// process reaper from collecting their exit status.
type commandRunner struct {
	reapLock sync.RWMutex
}

// run executes name with arguments and returns trimmed combined
// output. Non-zero exits return an error with the command and output.
func (r *commandRunner) run(name string, arguments ...string) (string, error) {
	r.reapLock.RLock()
	defer r.reapLock.RUnlock()
	output, err := exec.Command(name, arguments...).CombinedOutput()
	text := strings.TrimSpace(string(output))
	if err != nil {
		return text, fmt.Errorf("%s %s: %w: %s", name,
			strings.Join(arguments, " "), err, text)
	}
	return text, nil
}

// exclusively runs the given function while no other commands are in
// flight.
func (r *commandRunner) exclusively(reap func()) {
	r.reapLock.Lock()
	defer r.reapLock.Unlock()
	reap()
}
