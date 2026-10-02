package broker

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"prison/internal/broker/brokerlog"
	"prison/internal/broker/relay"
	"prison/internal/vault"
)

func (broker *Broker) routeHandler(projectID, secretName, logKind, successOutcome string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		var upstreamHost string
		refuse := func(status int, message string) {
			relay.WriteError(w, status, message)
			broker.record(brokerlog.Entry{
				Kind: logKind, Project: projectID, Secret: secretName,
				Method: r.Method, Path: r.URL.Path, Host: upstreamHost,
				Status: status, Outcome: outcomeRefused,
				DurationMS: elapsedMilliseconds(started),
			})
		}
		snapshot := broker.snapshot(projectID)
		if snapshot == nil {
			refuse(http.StatusNotFound, errNoProject.Error())
			return
		}
		secret := broker.grantedSecret(snapshot, secretName)
		if secret == nil || secret.Mode != vault.ModeRoute || secret.Route == nil {
			refuse(http.StatusNotFound, fmt.Sprintf(
				"no route %q for this project. Run `prison secret grant %s`",
				secretName, secretName))
			return
		}
		spec := secret.Route
		upstreamHost, _ = vault.SplitUpstream(spec.Upstream)
		if !strings.HasPrefix(r.URL.Path, spec.PathPrefix) {
			refuse(http.StatusNotFound, fmt.Sprintf(
				"the %s route only forwards %s", secretName, spec.PathPrefix))
			return
		}
		if len(spec.Methods) > 0 && !containsString(spec.Methods, r.Method) {
			w.Header().Set("Allow", strings.Join(spec.Methods, ", "))
			refuse(http.StatusMethodNotAllowed, fmt.Sprintf(
				"the %s route allows %s", secretName, strings.Join(spec.Methods, ", ")))
			return
		}
		if !broker.limiter.Allow(rateKey(projectID, secretName), spec.RateLimit) {
			refuse(http.StatusTooManyRequests, fmt.Sprintf(
				"the %s route allows %d requests a minute", secretName, spec.RateLimit))
			return
		}
		if secret.Value == "" {
			refuse(http.StatusServiceUnavailable, fmt.Sprintf(
				"the %s secret has no value", secretName))
			return
		}
		upstream := relay.Upstream{
			Scheme:          "https",
			Host:            upstreamAddress(spec.Upstream),
			Header:          spec.Header,
			Value:           spec.Prefix + secret.Value,
			DropCredentials: true,
		}
		proxy := relay.NewUpstreamProxy(broker.routeTransport, upstream,
			func(result relay.Result) {
				broker.record(brokerlog.Entry{
					Kind: logKind, Project: projectID, Secret: secretName,
					Method: r.Method, Path: r.URL.Path, Host: upstreamHost,
					Status: result.Status, Bytes: result.Bytes,
					Outcome:    proxyOutcome(result, successOutcome),
					DurationMS: elapsedMilliseconds(started), Error: result.Error,
				})
			})
		proxy.ServeHTTP(w, r)
	})
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
