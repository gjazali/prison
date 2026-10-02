package guest

import (
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// commandRunner runs helper programs under a read lock so that the reaper
// cannot collect their exit status.
type commandRunner struct {
	reapLock sync.RWMutex

	ownedLock sync.Mutex
	owned     map[int]chan int
	unclaimed map[int]unclaimedExit
}

type unclaimedExit struct {
	code   int
	reaped time.Time
}

// unclaimedLifetime limits how long an unclaimed exit code is kept,
// because orphans that PID 1 adopts never get an owner.
const unclaimedLifetime = time.Minute

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

// startOwned returns a channel for the exit code. Callers must not call
// `Wait` because the reaper collects the status. `Start` does not hold the
// reaper lock because a slow working directory can block it for a long time.
func (r *commandRunner) startOwned(process *exec.Cmd) (<-chan int, error) {
	if err := process.Start(); err != nil {
		return nil, err
	}
	return r.register(process.Process.Pid), nil
}

func (r *commandRunner) register(pid int) <-chan int {
	exited := make(chan int, 1)
	r.ownedLock.Lock()
	defer r.ownedLock.Unlock()
	if early, found := r.unclaimed[pid]; found {
		delete(r.unclaimed, pid)
		exited <- early.code
		return exited
	}
	if r.owned == nil {
		r.owned = map[int]chan int{}
	}
	r.owned[pid] = exited
	return exited
}

// deliverExit keeps the code of an unknown child for a time because its
// owner can register after the reaper collects it.
func (r *commandRunner) deliverExit(pid, code int) {
	r.ownedLock.Lock()
	defer r.ownedLock.Unlock()
	if exited, found := r.owned[pid]; found {
		exited <- code
		delete(r.owned, pid)
		return
	}
	now := time.Now()
	if r.unclaimed == nil {
		r.unclaimed = map[int]unclaimedExit{}
	}
	for stale, entry := range r.unclaimed {
		if now.Sub(entry.reaped) > unclaimedLifetime {
			delete(r.unclaimed, stale)
		}
	}
	r.unclaimed[pid] = unclaimedExit{code: code, reaped: now}
}

func (r *commandRunner) exclusively(reap func()) {
	r.reapLock.Lock()
	defer r.reapLock.Unlock()
	reap()
}
