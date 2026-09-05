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

// readyTimeout is the deadline for the ready check's resolver query.
const readyTimeout = 2 * time.Second

// runReady implements `prison-guest ready`. Returns 0 if the agent
// owns resolv.conf and the resolver answers. Returns 1 with the
// reason on stderr otherwise.
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

// checkReady returns an error unless resolv.conf has the prison marker
// and the resolver answers within the timeout.
func checkReady(resolvConf, resolverAddress string,
	timeout time.Duration) error {
	content, err := os.ReadFile(resolvConf)
	if err != nil {
		return err
	}
	if !resolvConfClaimed(string(content)) {
		return fmt.Errorf("%s is not yet written by prison", resolvConf)
	}
	if _, err := queryResolver(resolverAddress, protocol.BrokerHost,
		timeout); err != nil {
		return fmt.Errorf("the resolver is not answering: %w", err)
	}
	return nil
}

// resolvConfClaimed returns true if content contains the prison marker
// line.
func resolvConfClaimed(content string) bool {
	for _, line := range strings.Split(content, "\n") {
		if strings.TrimSpace(line) == resolvConfMarker {
			return true
		}
	}
	return false
}

// resolvConfContent returns the resolv.conf text the agent writes.
func resolvConfContent() string {
	return resolvConfMarker + "\nnameserver " + resolverAddress +
		"\noptions ndots:1\n"
}
