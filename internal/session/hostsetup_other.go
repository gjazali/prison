//go:build !darwin

package session

import (
	"context"

	"prison/internal/isolator"
)

func (s *Session) ensureHostDNS(ctx context.Context) {}

func (s *Session) ensureHostRoute(
	ctx context.Context, network isolator.NetworkInfo) {
}
