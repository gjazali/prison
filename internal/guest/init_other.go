//go:build !linux

package guest

import "log"

// runInit refuses to run on non-Linux platforms. The guest agent only
// works inside a Linux box.
func runInit(arguments []string, logger *log.Logger) int {
	logger.Printf("init only runs on Linux, inside a box")
	return 1
}
