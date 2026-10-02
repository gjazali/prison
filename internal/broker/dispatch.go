package broker

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"prison/internal/broker/brokerlog"
	"prison/internal/broker/protocol"
	"prison/internal/broker/relay"
	"prison/internal/policy"
)

const (
	outcomeAllowed         = "allowed"
	outcomeDeniedHost      = "denied-host"
	outcomeDeniedPort      = "denied-port"
	outcomeUpstreamFailed  = "upstream-failed"
	outcomeUnauthenticated = "unauthenticated"
	outcomeMalformed       = "malformed"
	outcomeRefused         = "refused"
	outcomeForwarded       = "forwarded"
	outcomeClientGone      = "client-gone"
)

func (broker *Broker) serveTCP(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	client := clientAddress(r.RemoteAddr)
	host, port := requestTarget(r)
	snapshot := broker.authenticate(r)
	if snapshot == nil {
		w.Header().Set("Proxy-Authenticate", `Basic realm="prison"`)
		relay.WriteError(w, http.StatusProxyAuthRequired,
			"the Proxy-Authorization password must be the project token")
		broker.record(brokerlog.Entry{
			Kind: brokerlog.KindEgress, Client: client, Method: r.Method,
			Host: host, Port: port, Outcome: outcomeUnauthenticated,
			Status:     http.StatusProxyAuthRequired,
			DurationMS: elapsedMilliseconds(started),
		})
		return
	}
	switch {
	case r.Method == http.MethodConnect:
		broker.serveConnect(w, r, snapshot, client, started)
	case r.URL.Host != "" && strings.EqualFold(r.URL.Scheme, "http"):
		broker.serveAbsoluteURI(w, r, snapshot, client, started)
	default:
		relay.WriteError(w, http.StatusBadRequest,
			"only CONNECT and absolute http URIs are proxied")
		broker.record(brokerlog.Entry{
			Kind: brokerlog.KindEgress, Project: snapshot.id, Client: client,
			Method: r.Method, Host: host, Port: port, Outcome: outcomeMalformed,
			Status: http.StatusBadRequest, DurationMS: elapsedMilliseconds(started),
		})
	}
}

func (broker *Broker) authenticate(r *http.Request) *projectSnapshot {
	token, ok := protocol.TokenFromProxyAuthorization(r.Header.Get("Proxy-Authorization"))
	if !ok {
		return nil
	}
	return broker.projects.Load().lookupToken(token)
}

func (broker *Broker) serveConnect(w http.ResponseWriter, r *http.Request,
	snapshot *projectSnapshot, client string, started time.Time) {
	target := r.URL.Host
	if target == "" {
		target = r.Host
	}
	host, port, ok := splitConnectTarget(target)
	if !ok {
		relay.WriteError(w, http.StatusBadRequest,
			fmt.Sprintf("%q is not a valid host:port", target))
		broker.record(brokerlog.Entry{
			Kind: brokerlog.KindEgress, Project: snapshot.id, Client: client,
			Method: r.Method, Host: target, Outcome: outcomeMalformed,
			Status: http.StatusBadRequest, DurationMS: elapsedMilliseconds(started),
		})
		return
	}
	if kind, name, ok := protocol.ParseZoneName(host); ok {
		conn, err := acknowledgeTunnel(w)
		if err != nil {
			return
		}
		broker.serveTunnel(conn, broker.zoneHandler(snapshot.id, kind, name))
		return
	}
	if route := broker.interceptRouteFor(snapshot, host, port); route != nil {
		broker.serveIntercept(w, snapshot, route.Name, host, port, started)
		return
	}
	decision := broker.allowListFor(snapshot).Allows(host, port)
	if !decision.Allowed {
		broker.refuseDestination(w, snapshot, client, r.Method, host, port,
			decision, started)
		return
	}
	dialer := net.Dialer{Timeout: connectDialTimeout}
	upstream, err := dialer.DialContext(r.Context(), "tcp",
		net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		relay.WriteError(w, http.StatusBadGateway,
			fmt.Sprintf("cannot reach %s: %v", host, err))
		broker.record(brokerlog.Entry{
			Kind: brokerlog.KindEgress, Project: snapshot.id, Client: client,
			Method: r.Method, Host: host, Port: port, Outcome: outcomeUpstreamFailed,
			Status: http.StatusBadGateway, DurationMS: elapsedMilliseconds(started),
			Error: err.Error(),
		})
		return
	}
	conn, err := acknowledgeTunnel(w)
	if err != nil {
		upstream.Close()
		return
	}
	moved := relay.Splice(conn, upstream, tunnelIdleTimeout)
	broker.record(brokerlog.Entry{
		Kind: brokerlog.KindEgress, Project: snapshot.id, Client: client,
		Method: r.Method, Host: host, Port: port, Outcome: outcomeAllowed,
		Status: http.StatusOK, Bytes: moved, DurationMS: elapsedMilliseconds(started),
	})
}

func (broker *Broker) serveIntercept(w http.ResponseWriter, snapshot *projectSnapshot,
	secretName, host string, port int, started time.Time) {
	refuse := func(message string) {
		relay.WriteError(w, http.StatusBadGateway, message)
		broker.record(brokerlog.Entry{
			Kind: brokerlog.KindRoute, Project: snapshot.id, Secret: secretName,
			Method: http.MethodConnect, Host: host, Port: port,
			Outcome: outcomeRefused, Status: http.StatusBadGateway,
			DurationMS: elapsedMilliseconds(started),
		})
	}
	authorities := broker.currentAuthorities()
	if authorities == nil {
		refuse(fmt.Sprintf("no certificate authority to terminate TLS for %s "+
			"on the %s route", host, secretName))
		return
	}
	tlsConfig, err := authorities.TLSConfig(host)
	if err != nil {
		refuse(fmt.Sprintf("cannot make a certificate for %s: %v", host, err))
		return
	}
	conn, err := acknowledgeTunnel(w)
	if err != nil {
		return
	}
	broker.serveTunnel(tls.Server(conn, tlsConfig),
		broker.routeHandler(snapshot.id, secretName, brokerlog.KindRoute, "intercepted"))
}

func (broker *Broker) serveAbsoluteURI(w http.ResponseWriter, r *http.Request,
	snapshot *projectSnapshot, client string, started time.Time) {
	host := normalizeHost(r.URL.Hostname())
	port := 80
	if portText := r.URL.Port(); portText != "" {
		parsed, err := strconv.Atoi(portText)
		if err != nil || parsed < 1 || parsed > 65535 {
			relay.WriteError(w, http.StatusBadRequest,
				fmt.Sprintf("%q is not a valid port", portText))
			return
		}
		port = parsed
	}
	if kind, name, ok := protocol.ParseZoneName(host); ok {
		broker.zoneHandler(snapshot.id, kind, name).ServeHTTP(w, r)
		return
	}
	decision := broker.allowListFor(snapshot).Allows(host, port)
	if !decision.Allowed {
		broker.refuseDestination(w, snapshot, client, r.Method, host, port,
			decision, started)
		return
	}
	upstream := relay.Upstream{Scheme: "http", Host: r.URL.Host}
	proxy := relay.NewUpstreamProxy(broker.routeTransport, upstream,
		func(result relay.Result) {
			outcome := outcomeAllowed
			if result.ClientGone {
				outcome = outcomeClientGone
			} else if result.Error != "" {
				outcome = outcomeUpstreamFailed
			}
			broker.record(brokerlog.Entry{
				Kind: brokerlog.KindEgress, Project: snapshot.id, Client: client,
				Method: r.Method, Host: host, Port: port, Outcome: outcome,
				Status: result.Status, Bytes: result.Bytes,
				DurationMS: elapsedMilliseconds(started), Error: result.Error,
			})
		})
	proxy.ServeHTTP(w, r)
}

func (broker *Broker) refuseDestination(w http.ResponseWriter, snapshot *projectSnapshot,
	client, method, host string, port int, decision policy.Decision, started time.Time) {
	message := fmt.Sprintf("egress policy does not allow %s", host)
	outcome := outcomeDeniedHost
	if decision.HostKnown {
		message = fmt.Sprintf("%s is not allowed on port %d. Add %q",
			host, port, decision.Suggested)
		outcome = outcomeDeniedPort
	}
	relay.WriteError(w, http.StatusForbidden, message)
	broker.record(brokerlog.Entry{
		Kind: brokerlog.KindEgress, Project: snapshot.id, Client: client,
		Method: method, Host: host, Port: port, Outcome: outcome,
		Status: http.StatusForbidden, DurationMS: elapsedMilliseconds(started),
	})
}

func acknowledgeTunnel(w http.ResponseWriter) (net.Conn, error) {
	conn, buffered, err := http.NewResponseController(w).Hijack()
	if err != nil {
		relay.WriteError(w, http.StatusInternalServerError,
			"cannot hijack the connection: "+err.Error())
		return nil, err
	}
	if _, err := buffered.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		conn.Close()
		return nil, err
	}
	if err := buffered.Flush(); err != nil {
		conn.Close()
		return nil, err
	}
	return relay.NewBufferedConn(conn, buffered.Reader), nil
}

func (broker *Broker) serveTunnel(conn net.Conn, handler http.Handler) {
	server := &http.Server{
		Handler:           tunnelHandler(handler),
		ReadHeaderTimeout: readHeaderTimeout,
		IdleTimeout:       tunnelIdleTimeout,
		ErrorLog:          broker.quietLog,
	}
	relay.ServeConnection(server, conn)
}

func tunnelHandler(inner http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodConnect {
			relay.WriteError(w, http.StatusMethodNotAllowed,
				"CONNECT is not allowed inside a tunnel")
			return
		}
		inner.ServeHTTP(w, r)
	})
}

func (broker *Broker) zoneHandler(projectID, kind, name string) http.Handler {
	switch kind {
	case protocol.KindInmate:
		return broker.inmateHandler(projectID, name)
	case protocol.KindRoute:
		return broker.routeHandler(projectID, name, brokerlog.KindRoute, "named")
	default:
		return broker.brokerAPIHandler(projectID)
	}
}

func splitConnectTarget(target string) (host string, port int, ok bool) {
	host, portText, err := net.SplitHostPort(target)
	if err != nil {
		if strings.Contains(target, ":") {
			return "", 0, false
		}
		host, portText = target, "443"
	}
	port, err = strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return "", 0, false
	}
	host = normalizeHost(host)
	if host == "" {
		return "", 0, false
	}
	return host, port, true
}

func normalizeHost(host string) string {
	return strings.TrimSuffix(strings.ToLower(host), ".")
}

func requestTarget(r *http.Request) (string, int) {
	if r.Method == http.MethodConnect {
		target := r.URL.Host
		if target == "" {
			target = r.Host
		}
		host, port, ok := splitConnectTarget(target)
		if !ok {
			return target, 0
		}
		return host, port
	}
	if r.URL.Host == "" {
		return "", 0
	}
	port := 80
	if parsed, err := strconv.Atoi(r.URL.Port()); err == nil {
		port = parsed
	}
	return normalizeHost(r.URL.Hostname()), port
}

func clientAddress(remoteAddress string) string {
	host, _, err := net.SplitHostPort(remoteAddress)
	if err != nil {
		return remoteAddress
	}
	return host
}
