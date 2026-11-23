package broker

import (
	"errors"
	"fmt"
	"log"
	"net"
	"net/netip"
	"strconv"
	"sync"
	"time"

	"prison/internal/broker/control"
	"prison/internal/broker/relay"
)

const forwardDialTimeout = 5 * time.Second

type forwardTable struct {
	mutex      sync.Mutex
	byBox      map[string][]net.Listener
	logger     *log.Logger
	dialUplink func(box string, guestPort int) (net.Conn, error)
}

// publish replaces the forwards of a box. On failure, the box has no
// forwards.
func (table *forwardTable) publish(box string,
	request control.PublishRequest) error {
	var dialBox func(guestPort int) (net.Conn, error)
	var labelFor func(guestPort int) string
	switch {
	case request.Uplink != "":
		if table.dialUplink == nil {
			return fmt.Errorf("uplink forwarding is not available")
		}
		uplinkBox := request.Uplink
		dialBox = func(guestPort int) (net.Conn, error) {
			return table.dialUplink(uplinkBox, guestPort)
		}
		labelFor = func(guestPort int) string {
			return fmt.Sprintf("uplink:%s:%d", uplinkBox, guestPort)
		}
	case request.Address != "":
		target, err := netip.ParseAddr(request.Address)
		if err != nil {
			return fmt.Errorf("box address %q is not valid", request.Address)
		}
		dialBox = func(guestPort int) (net.Conn, error) {
			destination := net.JoinHostPort(target.String(),
				strconv.Itoa(guestPort))
			return net.DialTimeout("tcp", destination, forwardDialTimeout)
		}
		labelFor = func(guestPort int) string {
			return net.JoinHostPort(target.String(), strconv.Itoa(guestPort))
		}
	default:
		return fmt.Errorf("a box address or uplink is required")
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
		guestPort := port.Guest
		go table.serve(listener, labelFor(guestPort), func() (net.Conn, error) {
			return dialBox(guestPort)
		})
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

func (table *forwardTable) serve(listener net.Listener, label string,
	dial func() (net.Conn, error)) {
	for {
		client, err := listener.Accept()
		if errors.Is(err, net.ErrClosed) {
			return
		}
		if err != nil {
			table.logf("forward %s: %v", label, err)
			return
		}
		go table.forward(client, dial)
	}
}

func (table *forwardTable) forward(client net.Conn, dial func() (net.Conn, error)) {
	defer client.Close()
	box, err := dial()
	if err != nil {
		return
	}
	relay.Splice(client, box, tunnelIdleTimeout)
}

func (table *forwardTable) logf(format string, arguments ...any) {
	if table.logger != nil {
		table.logger.Printf(format, arguments...)
	}
}
