package relay

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Transport settings for upstream connections.
const (
	dialTimeout         = 15 * time.Second
	tlsHandshakeTimeout = 15 * time.Second
	idleConnTimeout     = 90 * time.Second
	maxIdleConnsPerHost = 8
)

// StatusClientGone is recorded when the caller disconnects before the
// upstream responds.
const StatusClientGone = 499

// hopByHopHeaders are removed before forwarding upstream.
var hopByHopHeaders = []string{
	"Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization",
	"Proxy-Connection", "Te", "Trailer", "Transfer-Encoding", "Upgrade",
}

// upgradeHeaders are hop-by-hop headers kept during protocol switches.
var upgradeHeaders = map[string]bool{"Connection": true, "Upgrade": true}

// credentialHeaders are dropped to prevent credential smuggling.
var credentialHeaders = []string{
	"Authorization", "X-Api-Key", "Api-Key", "Proxy-Authorization",
}

// forwardedHeaders are removed to hide the box network from upstreams.
var forwardedHeaders = []string{
	"Forwarded", "X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto",
	"X-Real-Ip",
}

// Upstream describes the target of a proxied request and the
// credential to inject.
type Upstream struct {
	Scheme          string
	Host            string
	Header          string
	Value           string
	DropCredentials bool
}

// HostHeader returns the Host header value. Omits the port when it
// matches the scheme default.
func (upstream Upstream) HostHeader() string {
	host, port, err := net.SplitHostPort(upstream.Host)
	if err != nil {
		return upstream.Host
	}
	if (upstream.Scheme == "https" && port == "443") ||
		(upstream.Scheme == "http" && port == "80") {
		return host
	}
	return upstream.Host
}

// Result describes a completed proxied exchange.
type Result struct {
	Status       int
	Model        string
	InputTokens  *int
	OutputTokens *int
	Bytes        int64
	Duration     time.Duration
	Error        string
	ClientGone   bool
}

// NewTransport returns an http.Transport for upstream requests. Takes
// a response header timeout and an optional TLS config (nil uses
// system roots). HTTP/2 is disabled for streaming compatibility.
func NewTransport(responseHeaderTimeout time.Duration,
	tlsConfig *tls.Config) *http.Transport {
	dialer := &net.Dialer{Timeout: dialTimeout, KeepAlive: 30 * time.Second}
	return &http.Transport{
		Proxy:                 nil,
		DialContext:           dialer.DialContext,
		TLSClientConfig:       tlsConfig,
		TLSHandshakeTimeout:   tlsHandshakeTimeout,
		ResponseHeaderTimeout: responseHeaderTimeout,
		MaxIdleConnsPerHost:   maxIdleConnsPerHost,
		IdleConnTimeout:       idleConnTimeout,
		ForceAttemptHTTP2:     false,
		TLSNextProto:          map[string]func(string, *tls.Conn) http.RoundTripper{},
	}
}

// IsUpgradeRequest returns true if the headers request a protocol
// switch.
func IsUpgradeRequest(header http.Header) bool {
	if strings.TrimSpace(header.Get("Upgrade")) == "" {
		return false
	}
	for _, value := range header.Values("Connection") {
		for _, token := range strings.Split(value, ",") {
			if strings.EqualFold(strings.TrimSpace(token), "upgrade") {
				return true
			}
		}
	}
	return false
}

// DropHopByHopHeaders removes hop-by-hop headers and Connection-named
// headers. When keepUpgrade is true, Connection and Upgrade headers
// are kept.
func DropHopByHopHeaders(header http.Header, keepUpgrade bool) {
	for _, value := range header.Values("Connection") {
		for _, token := range strings.Split(value, ",") {
			name := http.CanonicalHeaderKey(strings.TrimSpace(token))
			if name != "" && !(keepUpgrade && upgradeHeaders[name]) {
				header.Del(name)
			}
		}
	}
	for _, name := range hopByHopHeaders {
		if keepUpgrade && upgradeHeaders[name] {
			continue
		}
		header.Del(name)
	}
}

// DropCredentialHeaders removes all credential-bearing headers.
func DropCredentialHeaders(header http.Header) {
	for _, name := range credentialHeaders {
		header.Del(name)
	}
}

// upstreamProxy is the handler returned by NewUpstreamProxy.
type upstreamProxy struct {
	transport http.RoundTripper
	upstream  Upstream
	onDone    func(Result)
	errorLog  *log.Logger
}

// NewUpstreamProxy returns a handler that forwards requests to the
// given upstream over transport. Calls onDone once per request with
// the result. A nil onDone is allowed.
func NewUpstreamProxy(transport http.RoundTripper, upstream Upstream,
	onDone func(Result)) http.Handler {
	if onDone == nil {
		onDone = func(Result) {}
	}
	return &upstreamProxy{
		transport: transport,
		upstream:  upstream,
		onDone:    onDone,
		errorLog:  log.New(io.Discard, "", 0),
	}
}

// ServeHTTP proxies a single request.
func (proxy *upstreamProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	exchange := &exchange{
		started: time.Now(),
		preview: previewCapture{limit: ModelPreviewBytes},
		onDone:  proxy.onDone,
	}
	reverse := &httputil.ReverseProxy{
		Transport:     proxy.transport,
		FlushInterval: -1,
		ErrorLog:      proxy.errorLog,
		Rewrite: func(request *httputil.ProxyRequest) {
			proxy.rewrite(request, exchange)
		},
		ModifyResponse: exchange.observeResponse,
		ErrorHandler:   exchange.fail,
	}
	reverse.ServeHTTP(w, r)
}

// rewrite sets the upstream URL, fixes headers, and tees the request
// body through the model preview.
func (proxy *upstreamProxy) rewrite(request *httputil.ProxyRequest,
	exchange *exchange) {
	request.SetURL(&url.URL{
		Scheme: proxy.upstream.Scheme,
		Host:   proxy.upstream.Host,
	})
	request.Out.Host = proxy.upstream.HostHeader()
	header := request.Out.Header
	DropHopByHopHeaders(header, IsUpgradeRequest(request.In.Header))
	if proxy.upstream.DropCredentials {
		DropCredentialHeaders(header)
	} else {
		header.Del("Proxy-Authorization")
	}
	for _, name := range forwardedHeaders {
		header.Del(name)
	}
	if proxy.upstream.Header != "" {
		header.Set(proxy.upstream.Header, proxy.upstream.Value)
	}
	if request.Out.Body != nil && request.Out.Body != http.NoBody {
		request.Out.Body = &previewingBody{
			ReadCloser: request.Out.Body,
			capture:    &exchange.preview,
		}
	}
}

// exchange holds per-request state shared by the proxy hooks.
type exchange struct {
	started time.Time
	preview previewCapture
	usage   UsageScanner
	bytes   int64
	once    sync.Once
	onDone  func(Result)
}

// finish reports the result once. Later calls are no-ops.
func (exchange *exchange) finish(status int, errorText string,
	clientGone bool) {
	exchange.once.Do(func() {
		exchange.onDone(Result{
			Status:       status,
			Model:        ModelFromPreview(exchange.preview.data),
			InputTokens:  exchange.usage.InputTokens,
			OutputTokens: exchange.usage.OutputTokens,
			Bytes:        exchange.bytes,
			Duration:     time.Since(exchange.started),
			Error:        errorText,
			ClientGone:   clientGone,
		})
	})
}

// observeResponse wraps the response body for usage scanning. A 101
// (switching protocols) response is reported finished immediately.
func (exchange *exchange) observeResponse(response *http.Response) error {
	if response.StatusCode == http.StatusSwitchingProtocols {
		exchange.finish(response.StatusCode, "", false)
		return nil
	}
	response.Body = &scanningBody{
		ReadCloser: response.Body,
		exchange:   exchange,
		status:     response.StatusCode,
	}
	return nil
}

// fail handles proxy errors. Records 499 if the client disconnected,
// or 502 with the error otherwise.
func (exchange *exchange) fail(w http.ResponseWriter, r *http.Request,
	err error) {
	if r.Context().Err() != nil || errors.Is(err, context.Canceled) {
		exchange.finish(StatusClientGone, err.Error(), true)
		return
	}
	exchange.finish(http.StatusBadGateway, err.Error(), false)
	WriteError(w, http.StatusBadGateway, "upstream request failed: "+err.Error())
}

// previewingBody captures the first bytes of a request body into the
// model preview.
type previewingBody struct {
	io.ReadCloser
	capture *previewCapture
}

// Read reads from the body and captures the prefix.
func (body *previewingBody) Read(buffer []byte) (int, error) {
	count, err := body.ReadCloser.Read(buffer)
	if count > 0 {
		body.capture.observe(buffer[:count])
	}
	return count, err
}

// scanningBody feeds a response body through the usage scanner and
// counts bytes. Finishes the exchange when closed.
type scanningBody struct {
	io.ReadCloser
	exchange *exchange
	status   int
}

// Read reads from the upstream body and scans for usage data.
func (body *scanningBody) Read(buffer []byte) (int, error) {
	count, err := body.ReadCloser.Read(buffer)
	if count > 0 {
		body.exchange.bytes += int64(count)
		body.exchange.usage.Feed(buffer[:count])
	}
	return count, err
}

// Close closes the upstream body and reports the exchange as finished.
func (body *scanningBody) Close() error {
	err := body.ReadCloser.Close()
	body.exchange.finish(body.status, "", false)
	return err
}
