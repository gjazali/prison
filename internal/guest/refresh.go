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

// Broker refresh timing constants.
const (
	brokerWaitTimeout   = 180 * time.Second
	brokerRetryInterval = 2 * time.Second
	refreshInterval     = 30 * time.Second
	fetchTimeout        = 20 * time.Second
	maximumAnswerBytes  = 4 * 1024 * 1024
)

// brokerRefresher keeps the allowlist and trust stores synced with the
// broker. Logs only on transitions, not on every poll.
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

// fetch GETs the given path from the broker. Returns the response
// body on 200, or an error.
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

// refreshHosts fetches /v1/hosts and installs the allowlist. Returns
// true if the broker answered. Unparsable answers keep the previous
// list.
func (r *brokerRefresher) refreshHosts(ctx context.Context) bool {
	body, err := r.fetch(ctx, protocol.PathHosts)
	if err != nil {
		r.noteHostsError(err.Error())
		return false
	}
	r.noteHostsError("")
	list, count, err := parseHostsAnswer(string(body))
	if err != nil {
		r.logger.Printf("hosts: the broker's list does not parse, keeping"+
			" the previous one: %v", err)
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

// noteHostsError logs each distinct error once and logs recovery when
// the error clears.
func (r *brokerRefresher) noteHostsError(message string) {
	if message == r.lastHostsError {
		return
	}
	switch {
	case message == "":
		r.logger.Printf("hosts: the broker is answering")
	case r.haveHosts:
		r.logger.Printf("hosts: the broker stopped answering: %s", message)
	default:
		r.logger.Printf("hosts: not yet: %s", message)
	}
	r.lastHostsError = message
}

// refreshCertificates fetches /v1/certificates and installs the
// bundle if it changed. Fetch errors are logged once per distinct
// message.
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

// run polls the broker until it answers or the wait timeout expires,
// then refreshes periodically until ctx is done. Closes firstAnswer
// after the first response.
func (r *brokerRefresher) run(ctx context.Context,
	firstAnswer chan<- struct{}) {
	deadline := time.Now().Add(brokerWaitTimeout)
	r.logger.Printf("waiting for the broker at %s", r.baseURL)
	for {
		answered := r.refreshHosts(ctx)
		if answered {
			r.refreshCertificates(ctx)
			break
		}
		if time.Now().After(deadline) {
			r.logger.Printf("the broker did not answer within %s; the box"+
				" runs without egress until it does", brokerWaitTimeout)
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

// sleepContext waits for the given duration. Returns false early if
// ctx is done.
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

// plural returns "s" for counts other than one.
func plural(count int) string {
	if count == 1 {
		return ""
	}
	return "s"
}
