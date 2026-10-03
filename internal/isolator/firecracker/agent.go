package firecracker

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"prison/internal/machine"
)

// dialAgent connects to the guest agent through the vsock socket of
// Firecracker. The socket takes a `CONNECT <port>` line first.
func dialAgent(ctx context.Context, socket string) (net.Conn, error) {
	var dialer net.Dialer
	connection, err := dialer.DialContext(ctx, "unix", socket)
	if err != nil {
		return nil, err
	}
	if deadline, found := ctx.Deadline(); found {
		connection.SetDeadline(deadline)
	}
	_, err = fmt.Fprintf(connection, "CONNECT %d\n", machine.AgentPort)
	if err != nil {
		connection.Close()
		return nil, err
	}
	reply, err := readLine(connection)
	if err != nil {
		connection.Close()
		return nil, err
	}
	if !strings.HasPrefix(reply, "OK ") {
		connection.Close()
		return nil, fmt.Errorf("unexpected vsock reply %q", reply)
	}
	connection.SetDeadline(time.Time{})
	return connection, nil
}

// readLine reads one byte at a time, so that no byte after the line is
// lost to a buffer.
func readLine(connection net.Conn) (string, error) {
	var line []byte
	buffer := make([]byte, 1)
	for {
		if _, err := connection.Read(buffer); err != nil {
			return "", err
		}
		if buffer[0] == '\n' {
			return string(line), nil
		}
		line = append(line, buffer[0])
	}
}

func askAgent(ctx context.Context, socket, operation string) error {
	connection, err := dialAgent(ctx, socket)
	if err != nil {
		return err
	}
	defer connection.Close()
	if deadline, found := ctx.Deadline(); found {
		connection.SetDeadline(deadline)
	}
	request, _ := json.Marshal(machine.Request{Operation: operation})
	if _, err := connection.Write(append(request, '\n')); err != nil {
		return err
	}
	line, err := bufio.NewReader(connection).ReadBytes('\n')
	if err != nil {
		return err
	}
	var response machine.Response
	if err := json.Unmarshal(line, &response); err != nil {
		return fmt.Errorf("the agent reply is not valid: %w", err)
	}
	if response.Error != "" {
		return errors.New(response.Error)
	}
	return nil
}
