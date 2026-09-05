package broker

import (
	"bufio"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"prison/internal/broker/brokerlog"
	"prison/internal/broker/protocol"
)

// TestUnauthenticatedGets407 checks that a missing or invalid token
// gets a 407 response with a challenge header.
func TestUnauthenticatedGets407(t *testing.T) {
	h := newHarness(t, nil)
	for _, token := range []string{"", "not-the-token"} {
		response, conn := h.rawConnect("example.com:443", token)
		if conn != nil {
			t.Fatal("CONNECT succeeded without a valid token")
		}
		if response.StatusCode != http.StatusProxyAuthRequired {
			t.Fatalf("status = %d, want 407", response.StatusCode)
		}
		if got := response.Header.Get("Proxy-Authenticate"); got != `Basic realm="prison"` {
			t.Fatalf("Proxy-Authenticate = %q", got)
		}
		if message := errorMessage(t, response); !strings.Contains(message, "Proxy-Authorization") {
			t.Fatalf("message = %q", message)
		}
	}
	entry := h.waitForEntry(brokerlog.KindEgress, func(entry brokerlog.Entry) bool {
		return entry.Outcome == "unauthenticated"
	})
	if entry.Project != "" || entry.Client != "127.0.0.1" || entry.Host != "example.com" ||
		entry.Port != 443 || entry.Status != 407 {
		t.Fatalf("entry = %+v", entry)
	}
}

// TestConnectSplicesTLS checks that an allowed CONNECT tunnel
// passes TLS traffic end-to-end and logs the byte count.
func TestConnectSplicesTLS(t *testing.T) {
	var h *harness
	var upstream *httptest.Server
	h = newHarness(t, func(h *harness, options *Options) {
		upstream = h.newTLSUpstream(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			io.WriteString(w, "hello through the tunnel")
		}))
		h.setEgressAllow(hostPort(upstream))
	})
	client := h.proxiedClient(h.pool)
	response, err := client.Get(upstream.URL + "/hello")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if string(body) != "hello through the tunnel" {
		t.Fatalf("body = %q", body)
	}
	client.CloseIdleConnections()
	entry := h.waitForEntry(brokerlog.KindEgress, func(entry brokerlog.Entry) bool {
		return entry.Outcome == "allowed" && entry.Method == http.MethodConnect
	})
	if entry.Project != h.project.ID || entry.Bytes == 0 || entry.Status != 200 {
		t.Fatalf("entry = %+v", entry)
	}
}

// TestConnectDenied checks refusal for an unknown host and for a
// known host on a disallowed port.
func TestConnectDenied(t *testing.T) {
	h := newHarness(t, func(h *harness, options *Options) {
		h.setEgressAllow("known.example")
	})
	response, _ := h.rawConnect("denied.example:443", h.token)
	expectRefusal(t, response, http.StatusForbidden,
		"prison egress policy does not allow denied.example")
	response, _ = h.rawConnect("known.example:8443", h.token)
	expectRefusal(t, response, http.StatusForbidden,
		`prison allows known.example but not on port 8443; add "known.example:8443" to allow it`)
	response, _ = h.rawConnect("known.example", h.token)
	if response.StatusCode == http.StatusForbidden {
		t.Fatal("a bare CONNECT target did not default to port 443")
	}
	h.waitForEntry(brokerlog.KindEgress, func(entry brokerlog.Entry) bool {
		return entry.Outcome == "denied-port" && entry.Port == 8443
	})
	h.waitForEntry(brokerlog.KindEgress, func(entry brokerlog.Entry) bool {
		return entry.Outcome == "denied-host" && entry.Host == "denied.example"
	})
}

// TestAbsoluteURIProxy checks plain HTTP proxying: path and query
// pass through, proxy credentials are stripped, and origin-form or
// https requests are refused.
func TestAbsoluteURIProxy(t *testing.T) {
	var upstream *httptest.Server
	var seenProxyAuthorization, seenPath, seenHost string
	h := newHarness(t, func(h *harness, options *Options) {
		upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			seenProxyAuthorization = r.Header.Get("Proxy-Authorization")
			seenPath = r.URL.RequestURI()
			seenHost = r.Host
			io.WriteString(w, "plain ok")
		}))
		t.Cleanup(upstream.Close)
		h.setEgressAllow(hostPort(upstream))
	})
	client := h.proxiedClient(nil)
	response, err := client.Get(upstream.URL + "/path?q=1")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if string(body) != "plain ok" || seenPath != "/path?q=1" || seenProxyAuthorization != "" ||
		seenHost != hostPort(upstream) {
		t.Fatalf("body=%q path=%q proxyauth=%q host=%q", body, seenPath,
			seenProxyAuthorization, seenHost)
	}
	h.waitForEntry(brokerlog.KindEgress, func(entry brokerlog.Entry) bool {
		return entry.Outcome == "allowed" && entry.Method == http.MethodGet && entry.Status == 200
	})

	authorization := "Proxy-Authorization: " + protocol.ProxyAuthorization(h.token) + "\r\n"
	response, conn, _ := h.rawRequest("GET / HTTP/1.1\r\nHost: example.com\r\n" + authorization + "\r\n")
	expectRefusal(t, response, http.StatusBadRequest,
		"prison proxies CONNECT and absolute http URIs only")
	conn.Close()
	response, conn, _ = h.rawRequest("GET https://example.com/ HTTP/1.1\r\nHost: example.com\r\n" +
		authorization + "\r\n")
	expectRefusal(t, response, http.StatusBadRequest,
		"prison proxies CONNECT and absolute http URIs only")
	conn.Close()
	response, conn, _ = h.rawRequest("GET http://denied.example/ HTTP/1.1\r\nHost: denied.example\r\n" +
		authorization + "\r\n")
	expectRefusal(t, response, http.StatusForbidden,
		"prison egress policy does not allow denied.example")
	conn.Close()
}

// TestUpgradePassthrough checks that a 101 upgrade becomes a
// two-way byte stream through the proxy.
func TestUpgradePassthrough(t *testing.T) {
	var upstream *httptest.Server
	h := newHarness(t, func(h *harness, options *Options) {
		upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Upgrade") != "echo" {
				http.Error(w, "no upgrade", http.StatusBadRequest)
				return
			}
			conn, buffered, err := http.NewResponseController(w).Hijack()
			if err != nil {
				return
			}
			defer conn.Close()
			buffered.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: echo\r\n" +
				"Connection: Upgrade\r\n\r\n")
			buffered.Flush()
			line, err := buffered.ReadString('\n')
			if err != nil {
				return
			}
			io.WriteString(conn, "echo: "+line)
		}))
		t.Cleanup(upstream.Close)
		h.setEgressAllow(hostPort(upstream))
	})
	request := fmt.Sprintf("GET %s/ws HTTP/1.1\r\nHost: %s\r\nProxy-Authorization: %s\r\n"+
		"Connection: Upgrade\r\nUpgrade: echo\r\n\r\n", upstream.URL, hostPort(upstream),
		protocol.ProxyAuthorization(h.token))
	response, conn, reader := h.rawRequest(request)
	defer conn.Close()
	if response.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("status = %d, want 101", response.StatusCode)
	}
	io.WriteString(conn, "ping\n")
	line, err := reader.ReadString('\n')
	if err != nil || line != "echo: ping\n" {
		t.Fatalf("line = %q, err = %v", line, err)
	}
	h.waitForEntry(brokerlog.KindEgress, func(entry brokerlog.Entry) bool {
		return entry.Status == http.StatusSwitchingProtocols
	})
}

// TestTunnelRefusesNestedConnect checks that a CONNECT inside an
// already-open tunnel gets a 405.
func TestTunnelRefusesNestedConnect(t *testing.T) {
	h := newHarness(t, nil)
	_, conn := h.rawConnect(protocol.BrokerHost+":80", h.token)
	if conn == nil {
		t.Fatal("CONNECT to the broker host failed")
	}
	defer conn.Close()
	fmt.Fprintf(conn, "CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\n\r\n")
	response, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: http.MethodConnect})
	if err != nil {
		t.Fatal(err)
	}
	expectRefusal(t, response, http.StatusMethodNotAllowed,
		"prison has already terminated this connection")
}
