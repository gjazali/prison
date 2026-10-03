package session

import (
	"context"

	"prison/internal/broker/control"
	"prison/internal/isolator"
	"prison/internal/ui"
)

// publishPorts has the broker forward the ports because Firecracker does not
// publish them.
func (s *Session) publishPorts(ctx context.Context, client *control.Client,
	ports []isolator.PortMapping) {
	if len(ports) == 0 {
		return
	}
	box, err := s.Isolator.Box(ctx, s.BoxName)
	if err != nil || box.Address == "" {
		ui.Warn("cannot publish the ports because the box has no address")
		return
	}
	request := control.PublishRequest{Address: box.Address}
	for _, port := range ports {
		request.Ports = append(request.Ports,
			control.PortForward{Host: port.Host, Guest: port.Guest})
	}
	if err := client.Publish(ctx, s.BoxName, request); err != nil {
		ui.Warn("cannot publish the ports: %v", err)
	}
}

// UnpublishPorts does not start the broker because a stopped broker has no
// forwards.
func (e *Environment) UnpublishPorts(ctx context.Context, boxName string) {
	client := control.NewClient(e.Root.BrokerSocket())
	if client.Alive(ctx) {
		client.Unpublish(ctx, boxName)
	}
}
