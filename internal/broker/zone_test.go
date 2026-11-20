package broker

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"prison/internal/broker/brokerlog"
	"prison/internal/broker/protocol"
	"prison/internal/broker/signing"
	"prison/internal/state"
	"prison/internal/vault"
)

type upstreamRecorder struct {
	mutex   sync.Mutex
	headers http.Header
	path    string
	body    []byte
}

func (recorder *upstreamRecorder) record(r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	recorder.mutex.Lock()
	defer recorder.mutex.Unlock()
	recorder.headers = r.Header.Clone()
	recorder.path = r.URL.RequestURI()
	recorder.body = body
}

func (recorder *upstreamRecorder) header(name string) string {
	recorder.mutex.Lock()
	defer recorder.mutex.Unlock()
	return recorder.headers.Get(name)
}

func TestInmateProxy(t *testing.T) {
	recorder := &upstreamRecorder{}
	var upstream *httptest.Server
	h := newHarness(t, func(h *harness, options *Options) {
		upstream = h.newTLSUpstream(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			recorder.record(r)
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"id":"msg","usage":{"input_tokens":12,"output_tokens":34}}`)
		}))
		profile := &state.Profile{
			Inmates: []state.InmateRoute{{
				Name:       "claude",
				Upstream:   hostPort(upstream),
				PathPrefix: "/v1/",
				Credentials: []state.CredentialSpec{
					{Variable: "ANTHROPIC_API_KEY", Header: "x-api-key"},
				},
			}},
		}
		if err := h.project.SaveProfile(profile); err != nil {
			t.Fatal(err)
		}
	})
	client := h.tunnelClient(protocol.InmateHost("claude"))
	ctx := context.Background()

	response, err := h.tunnelClient(protocol.InmateHost("codex")).Get("http://x/v1/messages")
	if err != nil {
		t.Fatal(err)
	}
	expectRefusal(t, response, http.StatusNotFound,
		"the codex inmate is not enabled for this project")

	response, err = client.Get("http://x/other")
	if err != nil {
		t.Fatal(err)
	}
	expectRefusal(t, response, http.StatusNotFound, "path is not proxied")

	response, err = client.Get("http://x/v1/messages")
	if err != nil {
		t.Fatal(err)
	}
	expectRefusal(t, response, http.StatusServiceUnavailable,
		"no credential for the claude inmate. Run `prison secret init`, "+
			"then `prison inmate auth set claude`")

	h.createVault()
	response, err = client.Get("http://x/v1/messages")
	if err != nil {
		t.Fatal(err)
	}
	expectRefusal(t, response, http.StatusServiceUnavailable,
		"the vault is locked. Run `prison secret unlock`")

	h.unlock()
	response, err = client.Get("http://x/v1/messages")
	if err != nil {
		t.Fatal(err)
	}
	expectRefusal(t, response, http.StatusServiceUnavailable,
		"no credential for the claude inmate. Run `prison inmate auth set claude`")

	h.putInmateCredentials(&vault.InmateCredential{
		Inmate:   "claude",
		Variable: "ANTHROPIC_API_KEY",
		Value:    "sk-real-key",
	})
	request, _ := http.NewRequest(http.MethodPost, "http://x/v1/messages",
		strings.NewReader(`{"model":"claude-test-1","messages":[]}`))
	request.Header.Set("x-api-key", "placeholder-from-the-box")
	request.Header.Set("Authorization", "Bearer smuggled")
	response, err = client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != 200 || !strings.Contains(string(body), "msg") {
		t.Fatalf("status %d body %q", response.StatusCode, body)
	}
	if got := recorder.header("x-api-key"); got != "sk-real-key" {
		t.Fatalf("upstream x-api-key = %q", got)
	}
	if got := recorder.header("Authorization"); got != "" {
		t.Fatalf("upstream Authorization = %q, want dropped", got)
	}
	entry := h.waitForEntry(brokerlog.KindInmate, func(entry brokerlog.Entry) bool {
		return entry.Status == 200 && entry.Inmate == "claude"
	})
	if entry.Model != "claude-test-1" || entry.InputTokens == nil || *entry.InputTokens != 12 ||
		entry.OutputTokens == nil || *entry.OutputTokens != 34 || entry.Path != "/v1/messages" {
		t.Fatalf("entry = %+v", entry)
	}
	status, err := h.client.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(status.InmateCredentials) != 1 ||
		status.InmateCredentials[0].Inmate != "claude" ||
		status.InmateCredentials[0].Profile != vault.DefaultInmateProfile ||
		status.InmateCredentials[0].Variable != "ANTHROPIC_API_KEY" {
		t.Fatalf("InmateCredentials = %+v", status.InmateCredentials)
	}
}

func TestInmateProfileOverrideFailsClosed(t *testing.T) {
	recorder := &upstreamRecorder{}
	h := newHarness(t, func(h *harness, options *Options) {
		upstream := h.newTLSUpstream(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			recorder.record(r)
			io.WriteString(w, "ok")
		}))
		profile := &state.Profile{
			Inmates: []state.InmateRoute{{
				Name:       "claude-code",
				Upstream:   hostPort(upstream),
				PathPrefix: "/v1/",
				Credentials: []state.CredentialSpec{
					{Variable: "ANTHROPIC_API_KEY", Header: "x-api-key"},
					{Variable: "CLAUDE_CODE_OAUTH_TOKEN", Header: "Authorization", Prefix: "Bearer "},
				},
			}},
		}
		if err := h.project.SaveProfile(profile); err != nil {
			t.Fatal(err)
		}
		h.putInmateCredentials(
			&vault.InmateCredential{
				Inmate:   "claude-code",
				Profile:  vault.DefaultInmateProfile,
				Variable: "CLAUDE_CODE_OAUTH_TOKEN",
				Value:    "personal-oauth",
			},
			&vault.InmateCredential{
				Inmate:   "claude-code",
				Profile:  "work",
				Variable: "ANTHROPIC_API_KEY",
				Value:    "sk-work-key",
			},
		)
	})
	h.unlock()
	client := h.tunnelClient(protocol.InmateHost("claude-code"))

	response, err := client.Get("http://x/v1/messages")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 200 || recorder.header("Authorization") != "Bearer personal-oauth" {
		t.Fatalf("default: status=%d auth=%q", response.StatusCode, recorder.header("Authorization"))
	}

	if err := h.project.SetInmateAuthProfile("claude-code", "work"); err != nil {
		t.Fatal(err)
	}
	if err := h.client.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	response, err = client.Get("http://x/v1/messages")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 200 || recorder.header("x-api-key") != "sk-work-key" {
		t.Fatalf("work: status=%d x-api-key=%q", response.StatusCode, recorder.header("x-api-key"))
	}

	opened, err := vault.Open(h.root.VaultFile(), testPassphrase)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := opened.DeleteInmateProfileCredentials("claude-code", "work"); err != nil {
		t.Fatal(err)
	}
	opened.Lock()

	response, err = client.Get("http://x/v1/messages")
	if err != nil {
		t.Fatal(err)
	}
	expectRefusal(t, response, http.StatusServiceUnavailable,
		`no credential for the claude-code inmate in profile "work". `+
			"Run `prison inmate auth set claude-code --profile work`")

	if err := h.project.SetInmateAuthProfile("claude-code", "default"); err != nil {
		t.Fatal(err)
	}
	if err := h.client.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	response, err = client.Get("http://x/v1/messages")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 200 || recorder.header("Authorization") != "Bearer personal-oauth" {
		t.Fatalf("reverted: status=%d auth=%q", response.StatusCode, recorder.header("Authorization"))
	}
}

func TestInmateSSEStreams(t *testing.T) {
	release := make(chan struct{})
	h := newHarness(t, func(h *harness, options *Options) {
		upstream := h.newTLSUpstream(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			flusher := w.(http.Flusher)
			io.WriteString(w, "data: one\n\n")
			flusher.Flush()
			<-release
			io.WriteString(w, "data: {\"output_tokens\": 5}\n\n")
			flusher.Flush()
		}))
		profile := &state.Profile{Inmates: []state.InmateRoute{{
			Name: "claude", Upstream: hostPort(upstream), PathPrefix: "/",
			Credentials: []state.CredentialSpec{{Variable: "KEY", Header: "x-api-key"}},
		}}}
		if err := h.project.SaveProfile(profile); err != nil {
			t.Fatal(err)
		}
		h.putInmateCredentials(&vault.InmateCredential{
			Inmate: "claude", Variable: "KEY", Value: "k",
		})
	})
	h.unlock()
	response, err := h.tunnelClient(protocol.InmateHost("claude")).Get("http://x/stream")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	reader := bufio.NewReader(response.Body)
	first, err := reader.ReadString('\n')
	if err != nil || first != "data: one\n" {
		t.Fatalf("first line = %q, err = %v", first, err)
	}
	close(release)
	reader.ReadString('\n')
	second, err := reader.ReadString('\n')
	if err != nil || !strings.HasPrefix(second, "data: {") {
		t.Fatalf("second line = %q, err = %v", second, err)
	}
	entry := h.waitForEntry(brokerlog.KindInmate, func(entry brokerlog.Entry) bool {
		return entry.Path == "/stream" && entry.Status == 200
	})
	if entry.OutputTokens == nil || *entry.OutputTokens != 5 {
		t.Fatalf("entry = %+v", entry)
	}
}

func TestRouteRules(t *testing.T) {
	recorder := &upstreamRecorder{}
	h := newHarness(t, func(h *harness, options *Options) {
		upstream := h.newTLSUpstream(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			recorder.record(r)
			io.WriteString(w, "routed")
		}))
		h.createVault(
			routeSecret("api", hostPort(upstream), vault.RouteSpec{
				PathPrefix: "/v1/", Header: "Authorization", Prefix: "Bearer ",
				Methods: []string{"GET", "POST"}, RateLimit: 2,
			}),
			routeSecret("gated", hostPort(upstream), vault.RouteSpec{}),
			routeSecret("other", hostPort(upstream), vault.RouteSpec{}),
		)
		h.grant("api", "gated")
	})
	api := h.tunnelClient(protocol.RouteHost("api"))
	missing := `no route "api" for this project. ` +
		"Run `prison secret grant api`"

	response, err := api.Get("http://x/v1/thing")
	if err != nil {
		t.Fatal(err)
	}
	expectRefusal(t, response, http.StatusNotFound, missing)

	h.unlock()
	response, _ = api.Get("http://x/outside")
	expectRefusal(t, response, http.StatusNotFound,
		"the api route only forwards /v1/")

	request, _ := http.NewRequest(http.MethodDelete, "http://x/v1/thing", nil)
	response, _ = api.Do(request)
	expectRefusal(t, response, http.StatusMethodNotAllowed, "the api route allows GET, POST")
	if got := response.Header.Get("Allow"); got != "GET, POST" {
		t.Fatalf("Allow = %q", got)
	}

	for attempt := 0; attempt < 2; attempt++ {
		request, _ := http.NewRequest(http.MethodGet, "http://x/v1/thing?x=1", nil)
		request.Header.Set("Authorization", "Bearer from-the-box")
		response, err = api.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		if response.StatusCode != 200 || string(body) != "routed" {
			t.Fatalf("attempt %d: status %d body %q", attempt, response.StatusCode, body)
		}
	}
	if got := recorder.header("Authorization"); got != "Bearer s3cret-api" {
		t.Fatalf("upstream Authorization = %q", got)
	}
	if recorder.path != "/v1/thing?x=1" {
		t.Fatalf("upstream path = %q", recorder.path)
	}
	response, _ = api.Get("http://x/v1/thing")
	expectRefusal(t, response, http.StatusTooManyRequests, "the api route allows 2 requests a minute")

	response, _ = h.tunnelClient(protocol.RouteHost("other")).Get("http://x/")
	expectRefusal(t, response, http.StatusNotFound,
		`no route "other" for this project. `+
			"Run `prison secret grant other`")

	response, _ = h.tunnelClient(protocol.RouteHost("gated")).Get("http://x/some/path")
	if response.StatusCode != 200 {
		t.Fatalf("gated status = %d, want 200", response.StatusCode)
	}

	entry := h.waitForEntry(brokerlog.KindRoute, func(entry brokerlog.Entry) bool {
		return entry.Outcome == "named" && entry.Secret == "api"
	})
	if entry.Method != http.MethodGet || entry.Path != "/v1/thing" || entry.Status != 200 {
		t.Fatalf("entry = %+v", entry)
	}
	h.waitForEntry(brokerlog.KindRoute, func(entry brokerlog.Entry) bool {
		return entry.Outcome == "refused" && entry.Status == http.StatusTooManyRequests
	})
}

func TestInterceptRoute(t *testing.T) {
	recorder := &upstreamRecorder{}
	var upstream *httptest.Server
	h := newHarness(t, func(h *harness, options *Options) {
		upstream = h.newTLSUpstream(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			recorder.record(r)
			io.WriteString(w, "intercepted ok")
		}))
		_, port, _ := strings.Cut(hostPort(upstream), ":")
		h.createVault(routeSecret("gh", "localhost:"+port, vault.RouteSpec{
			Header: "Authorization", Prefix: "token ", Intercept: true,
		}))
		h.grant("gh")
	})
	_, port, _ := strings.Cut(hostPort(upstream), ":")
	h.unlock()

	api := h.tunnelClient(protocol.BrokerHost)
	response, err := api.Get("http://x" + protocol.PathHosts)
	if err != nil {
		t.Fatal(err)
	}
	hosts, _ := io.ReadAll(response.Body)
	if !strings.Contains(string(hosts), "localhost:"+port+"\n") {
		t.Fatalf("/v1/hosts = %q", hosts)
	}
	response, err = api.Get("http://x" + protocol.PathCertificates)
	if err != nil {
		t.Fatal(err)
	}
	bundle, _ := io.ReadAll(response.Body)
	if response.Header.Get("Content-Type") != "application/x-pem-file" || len(bundle) == 0 {
		t.Fatalf("certificates: type %q, %d bytes", response.Header.Get("Content-Type"), len(bundle))
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(bundle) {
		t.Fatal("bundle has no certificate")
	}
	client := h.proxiedClient(pool)
	request, _ := http.NewRequest(http.MethodGet, "https://localhost:"+port+"/repo", nil)
	request.Header.Set("Authorization", "token from-the-box")
	response, err = client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != 200 || string(body) != "intercepted ok" {
		t.Fatalf("status %d body %q", response.StatusCode, body)
	}
	if got := recorder.header("Authorization"); got != "token s3cret-gh" {
		t.Fatalf("upstream Authorization = %q", got)
	}
	entry := h.waitForEntry(brokerlog.KindRoute, func(entry brokerlog.Entry) bool {
		return entry.Outcome == "intercepted"
	})
	if entry.Secret != "gh" || entry.Host != "localhost" || entry.Path != "/repo" {
		t.Fatalf("entry = %+v", entry)
	}
}

func TestInterceptRouteHTTP2AndTrailers(t *testing.T) {
	var upstream *httptest.Server
	var mu sync.Mutex
	var upstreamProtos []int
	var upstreamTE string
	var upstreamAuth string

	h := newHarness(t, func(h *harness, options *Options) {
		upstream = h.newHTTP2TLSUpstream(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			upstreamProtos = append(upstreamProtos, r.ProtoMajor)
			upstreamTE = r.Header.Get("Te")
			upstreamAuth = r.Header.Get("Authorization")
			mu.Unlock()

			w.Header().Set("Content-Type", "application/grpc")
			w.Header().Add("Trailer", "Grpc-Status")
			w.Header().Add("Trailer", "Grpc-Message")
			w.WriteHeader(http.StatusOK)
			flusher := w.(http.Flusher)
			io.WriteString(w, "chunk-1:")
			flusher.Flush()
			io.WriteString(w, r.URL.Path)
			w.Header().Set("Grpc-Status", "0")
			w.Header().Set("Grpc-Message", "ok")
		}))
		_, port, _ := strings.Cut(hostPort(upstream), ":")
		h.createVault(routeSecret("grpc", "localhost:"+port, vault.RouteSpec{
			Header: "Authorization", Prefix: "Bearer ", Intercept: true,
		}))
		h.grant("grpc")
	})
	_, port, _ := strings.Cut(hostPort(upstream), ":")
	h.unlock()

	api := h.tunnelClient(protocol.BrokerHost)
	certResp, err := api.Get("http://x" + protocol.PathCertificates)
	if err != nil {
		t.Fatal(err)
	}
	bundle, _ := io.ReadAll(certResp.Body)
	certResp.Body.Close()
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(bundle) {
		t.Fatal("bundle has no certificate")
	}

	var h2Only http.Protocols
	h2Only.SetHTTP2(true)
	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			Proxy:           http.ProxyURL(h.proxyURL()),
			TLSClientConfig: &tls.Config{RootCAs: pool},
			Protocols:       &h2Only,
		},
	}
	defer client.CloseIdleConnections()

	var group sync.WaitGroup
	errs := make(chan error, 4)
	for index := 0; index < 4; index++ {
		group.Add(1)
		go func(i int) {
			defer group.Done()
			path := fmt.Sprintf("/service.Method/%d", i)
			request, _ := http.NewRequest(http.MethodPost, "https://localhost:"+port+path,
				strings.NewReader("payload"))
			request.Header.Set("Te", "trailers")
			request.Header.Set("Authorization", "Bearer placeholder")
			response, err := client.Do(request)
			if err != nil {
				errs <- err
				return
			}
			body, err := io.ReadAll(response.Body)
			response.Body.Close()
			if err != nil {
				errs <- err
				return
			}
			if response.ProtoMajor != 2 {
				errs <- fmt.Errorf("client proto = %d, want 2", response.ProtoMajor)
				return
			}
			if string(body) != "chunk-1:"+path {
				errs <- fmt.Errorf("body = %q, want %q", body, "chunk-1:"+path)
				return
			}
			if got := response.Trailer.Get("Grpc-Status"); got != "0" {
				errs <- fmt.Errorf("Grpc-Status trailer = %q, want 0", got)
				return
			}
			if got := response.Trailer.Get("Grpc-Message"); got != "ok" {
				errs <- fmt.Errorf("Grpc-Message trailer = %q, want ok", got)
				return
			}
		}(index)
	}
	group.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(upstreamProtos) != 4 {
		t.Fatalf("upstream requests = %d, want 4", len(upstreamProtos))
	}
	for _, proto := range upstreamProtos {
		if proto != 2 {
			t.Fatalf("upstream proto = %d, want 2", proto)
		}
	}
	if upstreamTE != "trailers" {
		t.Fatalf("upstream Te = %q, want trailers", upstreamTE)
	}
	if upstreamAuth != "Bearer s3cret-grpc" {
		t.Fatalf("upstream Authorization = %q, want Bearer s3cret-grpc", upstreamAuth)
	}
}

func TestInmateProxyH2CAndHTTP2Upstream(t *testing.T) {
	var upstreamProto int
	var upstreamAuth string
	h := newHarness(t, func(h *harness, options *Options) {
		upstream := h.newHTTP2TLSUpstream(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			upstreamProto = r.ProtoMajor
			upstreamAuth = r.Header.Get("Authorization")
			w.Header().Add("Trailer", "X-Done")
			w.WriteHeader(http.StatusOK)
			io.WriteString(w, "h2-ok")
			w.Header().Set("X-Done", "yes")
		}))
		profile := &state.Profile{
			Inmates: []state.InmateRoute{{
				Name:       "claude",
				Upstream:   hostPort(upstream),
				PathPrefix: "/v1/",
				Credentials: []state.CredentialSpec{
					{Variable: "TOKEN", Header: "Authorization", Prefix: "Bearer "},
				},
			}},
		}
		if err := h.project.SaveProfile(profile); err != nil {
			t.Fatal(err)
		}
		h.putInmateCredentials(&vault.InmateCredential{
			Inmate: "claude", Variable: "TOKEN", Value: "tok-h2",
		})
	})
	h.unlock()

	var h2cOnly http.Protocols
	h2cOnly.SetUnencryptedHTTP2(true)
	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			Protocols: &h2cOnly,
			DialContext: func(context.Context, string, string) (net.Conn, error) {
				response, conn := h.rawConnect(protocol.InmateHost("claude")+":80", h.token)
				if conn == nil {
					return nil, fmt.Errorf("CONNECT answered %s", response.Status)
				}
				return conn, nil
			},
		},
	}
	defer client.CloseIdleConnections()

	response, err := client.Get("http://x/v1/call")
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if response.ProtoMajor != 2 || string(body) != "h2-ok" {
		t.Fatalf("proto=%d body=%q", response.ProtoMajor, body)
	}
	if response.Trailer.Get("X-Done") != "yes" {
		t.Fatalf("X-Done trailer = %q, want yes", response.Trailer.Get("X-Done"))
	}
	if upstreamProto != 2 || upstreamAuth != "Bearer tok-h2" {
		t.Fatalf("upstream proto=%d auth=%q", upstreamProto, upstreamAuth)
	}
}

func TestInmateOAuthRefreshAndRotation(t *testing.T) {
	var mu sync.Mutex
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	tokenCalls := 0
	var seenRefreshTokens []string
	var seenClientID, seenClientSecret string
	failRefresh := false

	recorder := &upstreamRecorder{}
	var tokenServer *httptest.Server
	h := newHarness(t, func(h *harness, options *Options) {
		options.Now = func() time.Time {
			mu.Lock()
			defer mu.Unlock()
			return now
		}
		tokenServer = h.newTLSUpstream(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if err := r.ParseForm(); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			mu.Lock()
			tokenCalls++
			call := tokenCalls
			seenRefreshTokens = append(seenRefreshTokens, r.FormValue("refresh_token"))
			seenClientID = r.FormValue("client_id")
			seenClientSecret = r.FormValue("client_secret")
			shouldFail := failRefresh
			mu.Unlock()

			w.Header().Set("Content-Type", "application/json")
			if shouldFail {
				w.WriteHeader(http.StatusBadRequest)
				io.WriteString(w, `{"error":"invalid_grant","error_description":"token revoked"}`)
				return
			}
			if call == 1 {
				io.WriteString(w, `{"access_token":"acc-1","expires_in":3600,"refresh_token":"rt-2"}`)
				return
			}
			io.WriteString(w, `{"access_token":"acc-2","expires_in":3600}`)
		}))
		upstream := h.newTLSUpstream(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			recorder.record(r)
			io.WriteString(w, "ok")
		}))
		profile := &state.Profile{
			Inmates: []state.InmateRoute{{
				Name:       "agy",
				Upstream:   hostPort(upstream),
				PathPrefix: "/",
				Credentials: []state.CredentialSpec{{
					Variable: "OAUTH_REFRESH",
					Header:   "Authorization",
					Prefix:   "Bearer ",
					TokenURL: tokenServer.URL + "/token",
					ClientID: "manifest-client-id",
				}},
			}},
		}
		if err := h.project.SaveProfile(profile); err != nil {
			t.Fatal(err)
		}
		h.putInmateCredentials(&vault.InmateCredential{
			Inmate:   "agy",
			Variable: "OAUTH_REFRESH",
			Value:    "rt-1",
			OAuth: &vault.InmateOAuthSpec{
				ClientSecret: "super-client-secret",
			},
		})
	})
	h.unlock()
	client := h.tunnelClient(protocol.InmateHost("agy"))

	resp, err := client.Get("http://x/call-1")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 || recorder.header("Authorization") != "Bearer acc-1" {
		t.Fatalf("call 1: status=%d auth=%q", resp.StatusCode, recorder.header("Authorization"))
	}

	resp, err = client.Get("http://x/call-2")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 || recorder.header("Authorization") != "Bearer acc-1" {
		t.Fatalf("call 2: status=%d auth=%q", resp.StatusCode, recorder.header("Authorization"))
	}

	mu.Lock()
	callsBeforeExpiry := tokenCalls
	mu.Unlock()
	if callsBeforeExpiry != 1 {
		t.Fatalf("tokenCalls before expiry = %d, want 1", callsBeforeExpiry)
	}

	opened, err := vault.Open(h.root.VaultFile(), testPassphrase)
	if err != nil {
		t.Fatal(err)
	}
	stored, found := opened.InmateCredential("agy", vault.DefaultInmateProfile, "OAUTH_REFRESH")
	opened.Lock()
	if !found || stored.Value != "rt-2" {
		t.Fatalf("stored refresh token = %+v, %v, want rt-2", stored, found)
	}

	mu.Lock()
	now = now.Add(3550 * time.Second)
	mu.Unlock()

	resp, err = client.Get("http://x/call-3")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 || recorder.header("Authorization") != "Bearer acc-2" {
		t.Fatalf("call 3: status=%d auth=%q", resp.StatusCode, recorder.header("Authorization"))
	}

	mu.Lock()
	if tokenCalls != 2 || len(seenRefreshTokens) != 2 ||
		seenRefreshTokens[0] != "rt-1" || seenRefreshTokens[1] != "rt-2" ||
		seenClientID != "manifest-client-id" || seenClientSecret != "super-client-secret" {
		t.Fatalf("token calls=%d rts=%v cid=%q csec=%q",
			tokenCalls, seenRefreshTokens, seenClientID, seenClientSecret)
	}
	failRefresh = true
	now = now.Add(3600 * time.Second)
	mu.Unlock()

	resp, err = client.Get("http://x/call-4")
	if err != nil {
		t.Fatal(err)
	}
	msg := errorMessage(t, resp)
	if resp.StatusCode != http.StatusServiceUnavailable || !strings.Contains(msg, "invalid_grant") {
		t.Fatalf("failed refresh: status=%d msg=%q", resp.StatusCode, msg)
	}
	if strings.Contains(msg, "rt-2") || strings.Contains(msg, "super-client-secret") {
		t.Fatalf("error leaked secret: %q", msg)
	}
}

func TestInmateInterceptTLSAndHTTP2(t *testing.T) {
	recorder := &upstreamRecorder{}
	var upstream *httptest.Server
	var upstreamProto int
	h := newHarness(t, func(h *harness, options *Options) {
		upstream = h.newHTTP2TLSUpstream(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			upstreamProto = r.ProtoMajor
			recorder.record(r)
			io.WriteString(w, "inmate-intercepted")
		}))
		_, port, _ := strings.Cut(hostPort(upstream), ":")
		profile := &state.Profile{
			Inmates: []state.InmateRoute{{
				Name:           "agy",
				Upstream:       "localhost:" + port,
				Intercept:      true,
				InterceptHosts: []string{"*.rep.googleapis.com"},
				PathPrefix:     "/v1internal/",
				Credentials: []state.CredentialSpec{{
					Variable: "TOKEN",
					Header:   "Authorization",
					Prefix:   "Bearer ",
				}},
			}},
		}
		if err := h.project.SaveProfile(profile); err != nil {
			t.Fatal(err)
		}
		h.putInmateCredentials(&vault.InmateCredential{
			Inmate: "agy", Variable: "TOKEN", Value: "real-inmate-token",
		})
	})
	_, port, _ := strings.Cut(hostPort(upstream), ":")
	h.unlock()

	api := h.tunnelClient(protocol.BrokerHost)
	hostsResp, err := api.Get("http://x" + protocol.PathHosts)
	if err != nil {
		t.Fatal(err)
	}
	hostsBody, _ := io.ReadAll(hostsResp.Body)
	hostsResp.Body.Close()
	if !strings.Contains(string(hostsBody), "localhost:"+port+"\n") ||
		!strings.Contains(string(hostsBody), "*.rep.googleapis.com:443\n") {
		t.Fatalf("/v1/hosts = %q", hostsBody)
	}

	certResp, err := api.Get("http://x" + protocol.PathCertificates)
	if err != nil {
		t.Fatal(err)
	}
	bundle, _ := io.ReadAll(certResp.Body)
	certResp.Body.Close()
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(bundle) {
		t.Fatal("bundle has no certificates")
	}

	var h2Only http.Protocols
	h2Only.SetHTTP2(true)
	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			Proxy:           http.ProxyURL(h.proxyURL()),
			TLSClientConfig: &tls.Config{RootCAs: pool},
			Protocols:       &h2Only,
		},
	}
	defer client.CloseIdleConnections()

	request, _ := http.NewRequest(http.MethodPost,
		"https://localhost:"+port+"/v1internal/generate", strings.NewReader("{}"))
	request.Header.Set("Authorization", "Bearer placeholder")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != 200 || response.ProtoMajor != 2 || string(body) != "inmate-intercepted" {
		t.Fatalf("status=%d proto=%d body=%q", response.StatusCode, response.ProtoMajor, body)
	}
	if upstreamProto != 2 || recorder.header("Authorization") != "Bearer real-inmate-token" {
		t.Fatalf("upstream proto=%d auth=%q", upstreamProto, recorder.header("Authorization"))
	}

	denied, err := client.Get("https://localhost:" + port + "/outside")
	if err != nil {
		t.Fatal(err)
	}
	expectRefusal(t, denied, http.StatusNotFound, "path is not proxied")

	entry := h.waitForEntry(brokerlog.KindInmate, func(entry brokerlog.Entry) bool {
		return entry.Outcome == "intercepted" && entry.Inmate == "agy"
	})
	if entry.Host != "localhost" || entry.Path != "/v1internal/generate" {
		t.Fatalf("entry = %+v", entry)
	}
}

func TestInmatePlaceholderTokenEndpoint(t *testing.T) {
	h := newHarness(t, func(h *harness, options *Options) {
		profile := &state.Profile{
			Inmates: []state.InmateRoute{{
				Name:                "agy",
				PlaceholderTokenURL: "https://oauth2.test:8443/token",
				Credentials: []state.CredentialSpec{{
					Variable: "GOOGLE_OAUTH_REFRESH_TOKEN",
					Header:   "Authorization",
					Prefix:   "Bearer ",
					TokenURL: "https://oauth2.googleapis.com/token",
				}},
			}},
		}
		if err := h.project.SaveProfile(profile); err != nil {
			t.Fatal(err)
		}
		h.putInmateCredentials(&vault.InmateCredential{
			Inmate:   "agy",
			Variable: "GOOGLE_OAUTH_REFRESH_TOKEN",
			Value:    "real-rt",
		})
	})
	h.unlock()

	api := h.tunnelClient(protocol.BrokerHost)
	hostsResp, err := api.Get("http://x" + protocol.PathHosts)
	if err != nil {
		t.Fatal(err)
	}
	hostsBody, _ := io.ReadAll(hostsResp.Body)
	hostsResp.Body.Close()
	if !strings.Contains(string(hostsBody), "oauth2.test:8443\n") {
		t.Fatalf("/v1/hosts = %q", hostsBody)
	}

	certResp, err := api.Get("http://x" + protocol.PathCertificates)
	if err != nil {
		t.Fatal(err)
	}
	bundle, _ := io.ReadAll(certResp.Body)
	certResp.Body.Close()
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(bundle) {
		t.Fatal("bundle has no certificates")
	}

	client := h.proxiedClient(pool)
	resp, err := client.Post("https://oauth2.test:8443/token",
		"application/x-www-form-urlencoded",
		strings.NewReader("grant_type=refresh_token&refresh_token=prison-placeholder"))
	if err != nil {
		t.Fatal(err)
	}
	var tokenResp struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 ||
		tokenResp.AccessToken != "prison-placeholder-"+h.project.ID ||
		tokenResp.TokenType != "Bearer" || tokenResp.ExpiresIn != 3600 {
		t.Fatalf("status=%d tokenResp=%+v", resp.StatusCode, tokenResp)
	}

	getResp, err := client.Get("https://oauth2.test:8443/token")
	if err != nil {
		t.Fatal(err)
	}
	expectRefusal(t, getResp, http.StatusMethodNotAllowed, "only POST is accepted")

	otherResp, err := client.Post("https://oauth2.test:8443/other", "text/plain", nil)
	if err != nil {
		t.Fatal(err)
	}
	expectRefusal(t, otherResp, http.StatusNotFound, "path is not proxied")

	h.waitForEntry(brokerlog.KindInmate, func(entry brokerlog.Entry) bool {
		return entry.Outcome == "placeholder-token" && entry.Inmate == "agy"
	})
}

func TestSignHMAC(t *testing.T) {
	h := newHarness(t, func(h *harness, options *Options) {
		h.createVault(&vault.Secret{
			Name: "hm", Mode: vault.ModeSign, Value: "the-key",
			Sign: &vault.SignSpec{Algorithm: vault.AlgorithmHMACSHA256, RateLimit: 3},
		})
		h.grant("hm")
	})
	h.unlock()
	api := h.tunnelClient(protocol.BrokerHost)
	post := func(body string) *http.Response {
		t.Helper()
		response, err := api.Post("http://x"+protocol.PathSign, "application/json",
			strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		return response
	}
	message := []byte("sign me please")
	encoded := base64.StdEncoding.EncodeToString(message)

	response := post(`{"secret":"hm","data":"` + encoded + `"}`)
	if response.StatusCode != 200 {
		t.Fatalf("status %d: %s", response.StatusCode, errorMessage(t, response))
	}
	var answer struct {
		Secret, Algorithm, Signature string
	}
	if err := json.NewDecoder(response.Body).Decode(&answer); err != nil {
		t.Fatal(err)
	}
	want := base64.StdEncoding.EncodeToString(signing.HMACSHA256([]byte("the-key"), message))
	if answer.Signature != want || answer.Algorithm != "hmac-sha256" || answer.Secret != "hm" {
		t.Fatalf("answer = %+v", answer)
	}

	expectRefusal(t, post(`not json`), http.StatusBadRequest, "the request body is not JSON")
	expectRefusal(t, post(`[1]`), http.StatusBadRequest, "the request body is not an object")
	expectRefusal(t, post(`{"secret":"hm","data":"%%%"}`), http.StatusBadRequest,
		`"data" must be base64 of the bytes to sign`)
	expectRefusal(t, post(`{"secret":"nope","data":""}`), http.StatusNotFound,
		`no signing secret "nope" for this project`)
	post(`{"secret":"hm","data":""}`)
	post(`{"secret":"hm","data":""}`)
	expectRefusal(t, post(`{"secret":"hm","data":""}`), http.StatusTooManyRequests,
		"the hm secret allows 3 signatures a minute")
	entry := h.waitForEntry(brokerlog.KindSign, func(entry brokerlog.Entry) bool {
		return entry.Status == 200
	})
	if entry.Secret != "hm" || entry.Project != h.project.ID {
		t.Fatalf("entry = %+v", entry)
	}
}

func TestKeysWithoutAgent(t *testing.T) {
	fingerprint := "SHA256:" + strings.Repeat("A", 43)
	h := newHarness(t, func(h *harness, options *Options) {
		options.SSHAuthSocket = ""
		h.createVault(&vault.Secret{
			Name: "commit", Mode: vault.ModeSign,
			Sign: &vault.SignSpec{Algorithm: vault.AlgorithmSSHAgent,
				Fingerprint: fingerprint, Namespace: "git"},
		})
		h.grant("commit")
	})
	h.unlock()
	api := h.tunnelClient(protocol.BrokerHost)
	response, err := api.Get("http://x" + protocol.PathKeys)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	var listed struct {
		Keys []struct {
			Secret      string  `json:"secret"`
			Algorithm   string  `json:"algorithm"`
			Fingerprint string  `json:"fingerprint"`
			Namespace   string  `json:"namespace"`
			PublicKey   *string `json:"public_key"`
			Available   bool    `json:"available"`
		} `json:"keys"`
	}
	if err := json.Unmarshal(body, &listed); err != nil {
		t.Fatalf("keys body %q: %v", body, err)
	}
	if len(listed.Keys) != 1 || listed.Keys[0].Secret != "commit" ||
		listed.Keys[0].Algorithm != "ssh-agent" || listed.Keys[0].Fingerprint != fingerprint ||
		listed.Keys[0].PublicKey != nil || listed.Keys[0].Available {
		t.Fatalf("keys = %s", body)
	}
	if !bytes.Contains(body, []byte(`"public_key":null`)) {
		t.Fatalf("public_key is not null in %s", body)
	}
	response, err = api.Post("http://x"+protocol.PathSign, "application/json",
		strings.NewReader(`{"secret":"commit","data":"aGk="}`))
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", response.StatusCode)
	}
	if message := errorMessage(t, response); !strings.Contains(message, "SSH_AUTH_SOCK") {
		t.Fatalf("message = %q", message)
	}
	response, err = api.Post("http://x"+protocol.PathSign, "application/json",
		strings.NewReader(`{"secret":"commit","data":"aGk=","namespace":"file"}`))
	if err != nil {
		t.Fatal(err)
	}
	expectRefusal(t, response, http.StatusForbidden,
		`the commit secret signs only in the "git" namespace`)
	response, _ = api.Get("http://x/v1/nothing")
	expectRefusal(t, response, http.StatusNotFound, unknownPathMessage)
}

func TestInmateInBoxLoginDisablesBrokerRouteAndIntercept(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "ok")
	}))
	t.Cleanup(upstream.Close)
	_, port, _ := strings.Cut(hostPort(upstream), ":")

	h := newHarness(t, func(h *harness, options *Options) {
		h.createVault()
		h.putInmateCredentials(&vault.InmateCredential{
			Inmate: "agy", Profile: vault.DefaultInmateProfile,
			Variable: "GEMINI_API_KEY", Value: "secret-gemini-key",
		})
		if err := h.project.SaveProfile(&state.Profile{
			Inmates: []state.InmateRoute{{
				Name:       "agy",
				Upstream:   "localhost:" + port,
				Intercept:  true,
				PathPrefix: "/",
				Credentials: []state.CredentialSpec{{
					Variable: "GEMINI_API_KEY",
					Header:   "x-goog-api-key",
				}},
			}},
		}); err != nil {
			t.Fatal(err)
		}
		if err := h.project.SetInmateAuthProfile("agy", vault.InBoxInmateProfile); err != nil {
			t.Fatal(err)
		}
	})
	h.unlock()

	api := h.tunnelClient(protocol.BrokerHost)
	hostsResponse, err := api.Get("http://x" + protocol.PathHosts)
	if err != nil {
		t.Fatal(err)
	}
	hosts, _ := io.ReadAll(hostsResponse.Body)
	hostsResponse.Body.Close()
	if strings.Contains(string(hosts), "localhost:"+port) {
		t.Fatalf("/v1/hosts still contains intercepted host: %q", hosts)
	}

	client := h.tunnelClient(protocol.InmateHost("agy"))
	response, err := client.Get("http://x/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	expectRefusal(t, response, http.StatusServiceUnavailable, "the agy inmate uses in-box login")
}
