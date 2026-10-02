package guest

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"prison/internal/broker/protocol"
)

const (
	brokerWaitTimeout   = 180 * time.Second
	brokerRetryInterval = 2 * time.Second
	refreshInterval     = 30 * time.Second
	fetchTimeout        = 20 * time.Second
	maximumAnswerBytes  = 4 * 1024 * 1024
)

type brokerRefresher struct {
	client       *http.Client
	baseURL      string
	allowList    *allowListHolder
	certificates *certificateInstaller
	logger       *log.Logger

	lastHostsError        string
	lastHostCount         int
	haveHosts             bool
	lastCertificatesError string
}

func (r *brokerRefresher) fetch(ctx context.Context,
	path string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet,
		r.baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	response, err := r.client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", path, response.Status)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maximumAnswerBytes))
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", path, err)
	}
	return body, nil
}

func (r *brokerRefresher) refreshHosts(ctx context.Context) bool {
	body, err := r.fetch(ctx, protocol.PathHosts)
	if err != nil {
		r.noteHostsError(err.Error())
		return false
	}
	r.noteHostsError("")
	list, count, err := parseHostsAnswer(string(body))
	if err != nil {
		r.logger.Printf("hosts: cannot parse the broker list: %v", err)
		return true
	}
	if count == 0 {
		return true
	}
	r.allowList.Replace(list)
	if !r.haveHosts || count != r.lastHostCount {
		r.logger.Printf("hosts: the broker allows %d pattern%s", count,
			plural(count))
	}
	r.haveHosts, r.lastHostCount = true, count
	return true
}

func (r *brokerRefresher) noteHostsError(message string) {
	if message == r.lastHostsError {
		return
	}
	switch {
	case message == "":
		r.logger.Printf("hosts: the broker is reachable")
	case r.haveHosts:
		r.logger.Printf("hosts: the broker is unreachable: %s", message)
	default:
		r.logger.Printf("hosts: the broker is not ready: %s", message)
	}
	r.lastHostsError = message
}

func (r *brokerRefresher) refreshCertificates(ctx context.Context) {
	body, err := r.fetch(ctx, protocol.PathCertificates)
	if err != nil {
		if err.Error() != r.lastCertificatesError {
			r.lastCertificatesError = err.Error()
			if r.haveHosts {
				r.logger.Printf("certificates: %v", err)
			}
		}
		return
	}
	r.lastCertificatesError = ""
	changed, err := r.certificates.install(body)
	if err != nil {
		r.logger.Printf("certificates: %v", err)
		return
	}
	if changed {
		count := len(splitCertificateBundle(body))
		noun := "authorities"
		if count == 1 {
			noun = "authority"
		}
		r.logger.Printf("certificates: trusting %d route %s", count, noun)
	}
}

// run closes `firstAnswer` after the first successful broker request or
// after the wait times out.
func (r *brokerRefresher) run(ctx context.Context,
	firstAnswer chan<- struct{}) {
	deadline := time.Now().Add(brokerWaitTimeout)
	for {
		answered := r.refreshHosts(ctx)
		if answered {
			r.refreshCertificates(ctx)
			break
		}
		if time.Now().After(deadline) {
			r.logger.Printf("cannot reach the broker after %s. The box has no"+
				" egress until the broker is reachable", brokerWaitTimeout)
			break
		}
		if !sleepContext(ctx, brokerRetryInterval) {
			close(firstAnswer)
			return
		}
	}
	close(firstAnswer)
	for sleepContext(ctx, refreshInterval) {
		r.refreshHosts(ctx)
		r.refreshCertificates(ctx)
	}
}

func sleepContext(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

func plural(count int) string {
	if count == 1 {
		return ""
	}
	return "s"
}
