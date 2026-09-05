package cli

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"prison"
	"prison/internal/session"
)

// loadEnvironment loads the host environment from the embedded assets
// and build version. Returns the environment or an error.
func loadEnvironment() (*session.Environment, error) {
	return session.Load(prison.Assets, Version)
}

// openSession loads the environment and opens the session for the
// current working directory. Returns the session or an error.
func openSession() (*session.Session, error) {
	environment, err := loadEnvironment()
	if err != nil {
		return nil, err
	}
	return environment.OpenCurrent()
}

// commandContext returns a context that cancels on SIGINT or SIGTERM.
func commandContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}
