package broker

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"prison/internal/broker/brokerlog"
	"prison/internal/broker/relay"
	"prison/internal/state"
)

// inmateHandler returns an HTTP handler for one inmate in a project.
// It verifies the inmate is in the profile, checks the path prefix,
// picks a credential, and proxies to the upstream.
func (broker *Broker) inmateHandler(projectID, inmate string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		refuse := func(status int, message string) {
			relay.WriteError(w, status, message)
			broker.record(brokerlog.Entry{
				Kind: brokerlog.KindInmate, Project: projectID, Inmate: inmate,
				Method: r.Method, Path: r.URL.Path, Status: status,
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
		prefix := route.PathPrefix
		if prefix == "" {
			prefix = "/"
		}
		if !strings.HasPrefix(r.URL.Path, prefix) {
			refuse(http.StatusNotFound, "not proxied by prison")
			return
		}
		header, value, ok := broker.chooseCredential(route.Credentials)
		if !ok {
			refuse(http.StatusServiceUnavailable, fmt.Sprintf(
				"prison holds no credential for the %s inmate; export %s and "+
					"run `prison up` again", inmate, credentialVariableList(route.Credentials)))
			return
		}
		upstream := relay.Upstream{
			Scheme:          "https",
			Host:            upstreamAddress(route.Upstream),
			Header:          header,
			Value:           value,
			DropCredentials: true,
		}
		proxy := relay.NewUpstreamProxy(broker.inmateTransport, upstream,
			func(result relay.Result) {
				broker.record(brokerlog.Entry{
					Kind: brokerlog.KindInmate, Project: projectID, Inmate: inmate,
					Method: r.Method, Path: r.URL.Path, Status: result.Status,
					Model: result.Model, InputTokens: result.InputTokens,
					OutputTokens: result.OutputTokens, Bytes: result.Bytes,
					Outcome:    proxyOutcome(result, outcomeForwarded),
					DurationMS: elapsedMilliseconds(started), Error: result.Error,
				})
			})
		proxy.ServeHTTP(w, r)
	})
}

// findInmateRoute looks up an inmate by name in the profile. It
// returns the matching route or nil.
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

// credentialVariableList joins the variable names from specs into a
// comma-separated string for error messages.
func credentialVariableList(specs []state.CredentialSpec) string {
	names := make([]string, 0, len(specs))
	for _, spec := range specs {
		if spec.Variable != "" {
			names = append(names, spec.Variable)
		}
	}
	if len(names) == 0 {
		return "the inmate's credential variable"
	}
	return strings.Join(names, ", ")
}

// proxyOutcome returns the outcome string for a proxied exchange. It
// takes a relay.Result and the success label. It returns the matching
// outcome constant.
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
