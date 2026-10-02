package broker

import (
	"bufio"
	"net"
	"strconv"
	"strings"
	"testing"

	"prison/internal/broker/control"
)

func freePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}

func TestForwardTableRelaysAndUnpublishes(t *testing.T) {
	box, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer box.Close()
	go func() {
		for {
			connection, err := box.Accept()
			if err != nil {
				return
			}
			line, _ := bufio.NewReader(connection).ReadString('\n')
			connection.Write([]byte(strings.ToUpper(line)))
			connection.Close()
		}
	}()
	hostPort := freePort(t)
	table := &forwardTable{}
	err = table.publish("demo", control.PublishRequest{
		Address: "127.0.0.1",
		Ports: []control.PortForward{{Host: hostPort,
			Guest: box.Addr().(*net.TCPAddr).Port}},
	})
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	address := "127.0.0.1:" + strconv.Itoa(hostPort)
	connection, err := net.Dial("tcp", address)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	connection.Write([]byte("hello\n"))
	answer, _ := bufio.NewReader(connection).ReadString('\n')
	connection.Close()
	if answer != "HELLO\n" {
		t.Errorf("answer = %q, want HELLO", answer)
	}
	table.unpublish("demo")
	if connection, err := net.Dial("tcp", address); err == nil {
		connection.Close()
		t.Error("Dial after unpublish succeeded, want an error")
	}
}

func TestForwardTableRefusesBadRequests(t *testing.T) {
	table := &forwardTable{}
	err := table.publish("demo", control.PublishRequest{Address: "box"})
	if err == nil {
		t.Error("publish accepted a box address that is not an address")
	}
	taken, _ := net.Listen("tcp", "127.0.0.1:0")
	defer taken.Close()
	free := freePort(t)
	err = table.publish("demo", control.PublishRequest{
		Address: "127.0.0.1",
		Ports: []control.PortForward{{Host: free, Guest: 1},
			{Host: taken.Addr().(*net.TCPAddr).Port, Guest: 2}},
	})
	if err == nil {
		t.Fatal("publish accepted a taken port")
	}
	if connection, err := net.Dial("tcp",
		"127.0.0.1:"+strconv.Itoa(free)); err == nil {
		connection.Close()
		t.Error("a failed publish left a port open")
	}
}
