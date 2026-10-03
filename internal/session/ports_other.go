//go:build !linux

package session

import (
	"context"

	"prison/internal/broker/control"
	"prison/internal/isolator"
)

func (s *Session) publishPorts(ctx context.Context, client *control.Client,
	ports []isolator.PortMapping) {
}

func (e *Environment) UnpublishPorts(ctx context.Context, boxName string) {}
