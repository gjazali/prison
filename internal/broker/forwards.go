package broker

import (
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/netip"
	"strconv"
	"sync"
	"time"

	"prison/internal/broker/control"
)

const forwardDialTimeout = 5 * time.Second

// forwardTable publishes box ports on the host loopback for cages that do
// not publish ports themselves.
type forwardTable struct {
	mutex  sync.Mutex
	byBox  map[string][]net.Listener
	logger *log.Logger
}

// publish replaces the forwards of a box. On failure, the box has no
// forwards.
func (table *forwardTable) publish(box string,
	request control.PublishRequest) error {
	target, err := netip.ParseAddr(request.Address)
	if err != nil {
		return fmt.Errorf("box address %q is not valid", request.Address)
	}
	table.unpublish(box)
	var listeners []net.Listener
	for _, port := range request.Ports {
		listener, err := net.Listen("tcp",
			net.JoinHostPort("127.0.0.1", strconv.Itoa(port.Host)))
		if err != nil {
			for _, opened := range listeners {
				opened.Close()
			}
			return fmt.Errorf("cannot publish port %d: %w", port.Host, err)
		}
		listeners = append(listeners, listener)
		destination := net.JoinHostPort(target.String(),
			strconv.Itoa(port.Guest))
		go table.serve(listener, destination)
	}
	table.mutex.Lock()
	if table.byBox == nil {
		table.byBox = map[string][]net.Listener{}
	}
	table.byBox[box] = listeners
	table.mutex.Unlock()
	return nil
}

func (table *forwardTable) unpublish(box string) {
	table.mutex.Lock()
	listeners := table.byBox[box]
	delete(table.byBox, box)
	table.mutex.Unlock()
	for _, listener := range listeners {
		listener.Close()
	}
}

func (table *forwardTable) closeAll() {
	table.mutex.Lock()
	boxes := make([]string, 0, len(table.byBox))
	for box := range table.byBox {
		boxes = append(boxes, box)
	}
	table.mutex.Unlock()
	for _, box := range boxes {
		table.unpublish(box)
	}
}

func (table *forwardTable) serve(listener net.Listener, destination string) {
	for {
		client, err := listener.Accept()
		if errors.Is(err, net.ErrClosed) {
			return
		}
		if err != nil {
			table.logf("forward %s: %v", destination, err)
			return
		}
		go table.relay(client, destination)
	}
}

func (table *forwardTable) relay(client net.Conn, destination string) {
	defer client.Close()
	box, err := net.DialTimeout("tcp", destination, forwardDialTimeout)
	if err != nil {
		return
	}
	defer box.Close()
	done := make(chan struct{}, 2)
	copyHalf := func(to, from net.Conn) {
		io.Copy(to, from)
		if half, isTCP := to.(*net.TCPConn); isTCP {
			half.CloseWrite()
		}
		done <- struct{}{}
	}
	go copyHalf(box, client)
	go copyHalf(client, box)
	<-done
	<-done
}

func (table *forwardTable) logf(format string, arguments ...any) {
	if table.logger != nil {
		table.logger.Printf(format, arguments...)
	}
}
