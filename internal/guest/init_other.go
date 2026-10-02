//go:build !linux

package guest

import "log"

func runInit(arguments []string, logger *log.Logger) int {
	logger.Printf("init runs only on Linux")
	return 1
}
