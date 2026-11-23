//go:build !darwin

package session

import (
	"context"

	"prison/internal/isolator"
)

func (s *Session) ensureHostDNS(ctx context.Context) {}

func (e *Environment) ensureHostRoute(
	ctx context.Context, network isolator.NetworkInfo) {
}

func (s *Session) ensureHostAnchor(
	ctx context.Context, recreateCurrent bool,
) func() {
	return nil
}
