package guest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"prison/internal/broker/protocol"
)

const readyTimeout = 3 * time.Second

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
	if err := checkBrokerReady(context.Background(), newBoxClient(readyTimeout),
		resolver, readyTimeout); err != nil {
		logger.Printf("not ready: %v", err)
		return 1
	}
	return 0
}

func checkBrokerReady(ctx context.Context, client *boxClient,
	resolverAddress string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet,
		client.baseURL+protocol.PathHosts, nil)
	if err != nil {
		return err
	}
	response, err := client.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("cannot reach the broker: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("broker returned %s for %s", response.Status,
			protocol.PathHosts)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maximumAnswerBytes))
	if err != nil {
		return fmt.Errorf("cannot read %s: %w", protocol.PathHosts, err)
	}
	_, count, err := parseHostsAnswer(string(body))
	if err != nil {
		return fmt.Errorf("cannot parse %s: %w", protocol.PathHosts, err)
	}
	if count == 0 {
		return errors.New("the broker returned no allowed hosts")
	}
	if probeHost := firstLiteralHost(string(body)); probeHost != "" {
		if _, err := queryResolver(resolverAddress, probeHost, timeout); err != nil {
			return fmt.Errorf("the box allowlist is not loaded yet: %w", err)
		}
	}
	return nil
}

func firstLiteralHost(body string) string {
	for _, rawLine := range strings.Split(body, "\n") {
		entry := strings.TrimSpace(rawLine)
		if entry == "" || strings.HasPrefix(entry, "#") {
			continue
		}
		host := entry
		if splitHost, _, err := net.SplitHostPort(entry); err == nil {
			host = splitHost
		}
		if host == "" || strings.HasPrefix(host, "*.") ||
			strings.Contains(host, "*") {
			continue
		}
		return host
	}
	return ""
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
