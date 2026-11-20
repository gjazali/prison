package broker

import (
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"prison/internal/broker/brokerlog"
	"prison/internal/broker/relay"
	"prison/internal/state"
	"prison/internal/vault"
)

func (broker *Broker) inmateHandler(projectID, inmate,
	overrideHost string, overridePort int, successOutcome string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		logHost := overrideHost
		refuse := func(status int, message string) {
			relay.WriteError(w, status, message)
			broker.record(brokerlog.Entry{
				Kind: brokerlog.KindInmate, Project: projectID, Inmate: inmate,
				Method: r.Method, Path: r.URL.Path, Host: logHost, Status: status,
				Outcome: outcomeRefused, DurationMS: elapsedMilliseconds(started),
			})
		}
		snapshot := broker.snapshot(projectID)
		if snapshot == nil {
			refuse(http.StatusNotFound, errNoProject.Error())
			return
		}
		route := findInmateRoute(snapshot.profile, inmate)
		if route == nil {
			refuse(http.StatusNotFound,
				fmt.Sprintf("the %s inmate is not enabled for this project", inmate))
			return
		}
		targetHost := ""
		if overrideHost != "" {
			targetHost = net.JoinHostPort(overrideHost, strconv.Itoa(overridePort))
		} else if route.Upstream != "" {
			targetHost = upstreamAddress(route.Upstream)
			logHost, _ = vault.SplitUpstream(route.Upstream)
		}
		if targetHost == "" {
			refuse(http.StatusNotFound,
				fmt.Sprintf("the %s inmate has no upstream host", inmate))
			return
		}
		prefix := route.PathPrefix
		if prefix == "" {
			prefix = "/"
		}
		if !isCanonicalPath(r.URL) {
			refuse(http.StatusBadRequest, errPathNotCanonical.Error())
			return
		}
		if !strings.HasPrefix(r.URL.Path, prefix) {
			refuse(http.StatusNotFound, "path is not proxied")
			return
		}
		header, value, refusal, ok := broker.chooseInmateCredential(
			r.Context(), snapshot, inmate, route.Credentials)
		if !ok {
			refuse(http.StatusServiceUnavailable, refusal)
			return
		}
		upstream := relay.Upstream{
			Scheme:          "https",
			Host:            targetHost,
			Header:          header,
			Value:           value,
			DropCredentials: true,
		}
		proxy := relay.NewUpstreamProxy(broker.inmateTransport, upstream,
			func(result relay.Result) {
				broker.record(brokerlog.Entry{
					Kind: brokerlog.KindInmate, Project: projectID, Inmate: inmate,
					Method: r.Method, Path: r.URL.Path, Host: logHost, Status: result.Status,
					Model: result.Model, InputTokens: result.InputTokens,
					OutputTokens: result.OutputTokens, Bytes: result.Bytes,
					Outcome:    proxyOutcome(result, successOutcome),
					DurationMS: elapsedMilliseconds(started), Error: result.Error,
				})
			})
		proxy.ServeHTTP(w, r)
	})
}

func findInmateRoute(profile *state.Profile, inmate string) *state.InmateRoute {
	if profile == nil {
		return nil
	}
	for index := range profile.Inmates {
		if profile.Inmates[index].Name == inmate {
			return &profile.Inmates[index]
		}
	}
	return nil
}

func proxyOutcome(result relay.Result, success string) string {
	switch {
	case result.ClientGone:
		return outcomeClientGone
	case result.Error != "":
		return outcomeUpstreamFailed
	default:
		return success
	}
}
