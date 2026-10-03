//go:build !darwin

package cli

import (
	"github.com/spf13/cobra"

	"prison/internal/session"
)

func platformHostCommands() []*cobra.Command { return nil }

func printHostnameSummary(current *session.Session) {}
