package guest

import (
	"fmt"
	"log"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"prison/internal/broker/protocol"
)

const readyTimeout = 2 * time.Second

func runReady(arguments []string, logger *log.Logger) int {
	if len(arguments) != 0 {
		logger.Printf("ready takes no arguments")
		return 2
	}
	resolver := net.JoinHostPort(resolverAddress, strconv.Itoa(resolverPort))
	if err := checkReady(resolvConfPath, resolver, readyTimeout); err != nil {
		logger.Printf("not ready: %v", err)
		return 1
	}
	return 0
}

func checkReady(resolvConf, resolverAddress string,
	timeout time.Duration) error {
	content, err := os.ReadFile(resolvConf)
	if err != nil {
		return err
	}
	if !resolvConfClaimed(string(content)) {
		return fmt.Errorf("%s has no prison marker", resolvConf)
	}
	if _, err := queryResolver(resolverAddress, protocol.BrokerHost,
		timeout); err != nil {
		return fmt.Errorf("cannot query the resolver: %w", err)
	}
	return nil
}

func resolvConfClaimed(content string) bool {
	for _, line := range strings.Split(content, "\n") {
		if strings.TrimSpace(line) == resolvConfMarker {
			return true
		}
	}
	return false
}

func resolvConfContent() string {
	return resolvConfMarker + "\nnameserver " + resolverAddress +
		"\noptions ndots:1\n"
}
