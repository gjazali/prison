package cli

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"prison"
	"prison/internal/session"
)

func loadEnvironment() (*session.Environment, error) {
	return session.Load(prison.Assets, Version)
}

func openSession() (*session.Session, error) {
	environment, err := loadEnvironment()
	if err != nil {
		return nil, err
	}
	return environment.OpenCurrent()
}

func commandContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}
