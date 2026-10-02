//go:build linux

package guest

import (
	"bufio"
	"encoding/json"
	"io"
	"log"
	"os"
	"testing"

	"golang.org/x/sys/unix"

	"prison/internal/machine"
)

func askTestAgent(t *testing.T, request string) (machine.Response, bool) {
	t.Helper()
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	host := os.NewFile(uintptr(pair[0]), "host")
	guest := os.NewFile(uintptr(pair[1]), "guest")
	defer host.Close()
	stopped := false
	done := make(chan struct{})
	go func() {
		handleAgentConnection(guest, &commandRunner{},
			log.New(io.Discard, "", 0), func() { stopped = true })
		close(done)
	}()
	if _, err := host.WriteString(request + "\n"); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(host).ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	<-done
	var response machine.Response
	if err := json.Unmarshal(line, &response); err != nil {
		t.Fatalf("response %q: %v", line, err)
	}
	return response, stopped
}

func TestAgentAnswersPing(t *testing.T) {
	response, stopped := askTestAgent(t, `{"operation":"ping"}`)
	if response.Error != "" || stopped {
		t.Errorf("ping = %+v, stopped %v, want no error", response, stopped)
	}
}

func TestAgentShutsDownAfterAnswering(t *testing.T) {
	response, stopped := askTestAgent(t, `{"operation":"shutdown"}`)
	if response.Error != "" || !stopped {
		t.Errorf("shutdown = %+v, stopped %v, want stopped", response, stopped)
	}
}

func TestAgentRefusesUnknownOperations(t *testing.T) {
	response, stopped := askTestAgent(t, `{"operation":"format"}`)
	if response.Error == "" || stopped {
		t.Errorf("format = %+v, stopped %v, want an error", response, stopped)
	}
}
