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

const (
	dialTimeout         = 15 * time.Second
	tlsHandshakeTimeout = 15 * time.Second
	idleConnTimeout     = 90 * time.Second
	maxIdleConnsPerHost = 8
)

// StatusClientGone is a nonstandard status. It marks a caller that left
// before the upstream answered.
const StatusClientGone = 499

var hopByHopHeaders = []string{
	"Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization",
	"Proxy-Connection", "Te", "Trailer", "Transfer-Encoding", "Upgrade",
}

var upgradeHeaders = map[string]bool{"Connection": true, "Upgrade": true}

// credentialHeaders are removed so that a box cannot send its own
// credentials upstream.
var credentialHeaders = []string{
	"Authorization", "X-Api-Key", "Api-Key", "Proxy-Authorization",
}

// forwardedHeaders are removed to hide the box network from upstreams.
var forwardedHeaders = []string{
	"Forwarded", "X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto",
	"X-Real-Ip",
}

type Upstream struct {
	Scheme          string
	Host            string
	Header          string
	Value           string
	DropCredentials bool
}

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

// NewTransport disables HTTP/2 for streaming compatibility.
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

func DropCredentialHeaders(header http.Header) {
	for _, name := range credentialHeaders {
		header.Del(name)
	}
}

type upstreamProxy struct {
	transport http.RoundTripper
	upstream  Upstream
	onDone    func(Result)
	errorLog  *log.Logger
}

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

type exchange struct {
	started time.Time
	preview previewCapture
	usage   UsageScanner
	bytes   int64
	once    sync.Once
	onDone  func(Result)
}

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

func (exchange *exchange) fail(w http.ResponseWriter, r *http.Request,
	err error) {
	if r.Context().Err() != nil || errors.Is(err, context.Canceled) {
		exchange.finish(StatusClientGone, err.Error(), true)
		return
	}
	exchange.finish(http.StatusBadGateway, err.Error(), false)
	WriteError(w, http.StatusBadGateway, "upstream request failed: "+err.Error())
}

type previewingBody struct {
	io.ReadCloser
	capture *previewCapture
}

func (body *previewingBody) Read(buffer []byte) (int, error) {
	count, err := body.ReadCloser.Read(buffer)
	if count > 0 {
		body.capture.observe(buffer[:count])
	}
	return count, err
}

type scanningBody struct {
	io.ReadCloser
	exchange *exchange
	status   int
}

func (body *scanningBody) Read(buffer []byte) (int, error) {
	count, err := body.ReadCloser.Read(buffer)
	if count > 0 {
		body.exchange.bytes += int64(count)
		body.exchange.usage.Feed(buffer[:count])
	}
	return count, err
}

func (body *scanningBody) Close() error {
	err := body.ReadCloser.Close()
	body.exchange.finish(body.status, "", false)
	return err
}
