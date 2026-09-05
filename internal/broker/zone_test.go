package broker

import (
	"bufio"
	"bytes"
	"context"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"prison/internal/broker/brokerlog"
	"prison/internal/broker/control"
	"prison/internal/broker/protocol"
	"prison/internal/broker/signing"
	"prison/internal/state"
	"prison/internal/vault"
)

// upstreamRecorder captures the most recent request an upstream
// received.
type upstreamRecorder struct {
	mutex   sync.Mutex
	headers http.Header
	path    string
	body    []byte
}

// record stores the request.
func (recorder *upstreamRecorder) record(r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	recorder.mutex.Lock()
	defer recorder.mutex.Unlock()
	recorder.headers = r.Header.Clone()
	recorder.path = r.URL.RequestURI()
	recorder.body = body
}

// header returns one recorded header value.
func (recorder *upstreamRecorder) header(name string) string {
	recorder.mutex.Lock()
	defer recorder.mutex.Unlock()
	return recorder.headers.Get(name)
}

// TestInmateProxy checks the inmate proxy: profile membership, path
// prefix, credential swap, and token usage logging.
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
	expectRefusal(t, response, http.StatusNotFound, "not proxied by prison")

	response, err = client.Get("http://x/v1/messages")
	if err != nil {
		t.Fatal(err)
	}
	expectRefusal(t, response, http.StatusServiceUnavailable,
		"prison holds no credential for the claude inmate; export ANTHROPIC_API_KEY "+
			"and run `prison up` again")

	err = h.client.UpdateProject(ctx, h.project.ID, control.ProjectUpdate{
		Credentials: map[string]string{"ANTHROPIC_API_KEY": "sk-real-key"},
	})
	if err != nil {
		t.Fatal(err)
	}
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
	if len(status.CredentialVariables) != 1 || status.CredentialVariables[0] != "ANTHROPIC_API_KEY" {
		t.Fatalf("CredentialVariables = %v", status.CredentialVariables)
	}
}

// TestInmateSSEStreams checks that SSE events stream through
// chunk by chunk without buffering the whole response.
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
	})
	if err := h.client.UpdateProject(context.Background(), h.project.ID,
		control.ProjectUpdate{Credentials: map[string]string{"KEY": "k"}}); err != nil {
		t.Fatal(err)
	}
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

// TestRouteRules checks route refusals (missing grant, wrong path,
// wrong method, rate limit) and a successful forwarded request.
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
		h.confirmer.deny["gated"] = true
	})
	api := h.tunnelClient(protocol.RouteHost("api"))
	missing := `prison holds no route called "api" for this project; ` +
		"`prison secret grant api` gives it one"

	response, err := api.Get("http://x/v1/thing")
	if err != nil {
		t.Fatal(err)
	}
	expectRefusal(t, response, http.StatusNotFound, missing)

	h.unlock()
	response, _ = api.Get("http://x/outside")
	expectRefusal(t, response, http.StatusNotFound,
		"the api route forwards nothing outside /v1/")

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
		`prison holds no route called "other" for this project; `+
			"`prison secret grant other` gives it one")

	gatedSecret, _ := vault.Secret{}, 0
	_ = gatedSecret
	response, _ = h.tunnelClient(protocol.RouteHost("gated")).Get("http://x/some/path")
	if response.StatusCode != 200 {
		t.Fatalf("gated without confirm flag: status %d", response.StatusCode)
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

// TestRouteConfirmation checks that a secret with confirm=true
// prompts the confirmer and is refused when denied.
func TestRouteConfirmation(t *testing.T) {
	h := newHarness(t, func(h *harness, options *Options) {
		upstream := h.newTLSUpstream(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			io.WriteString(w, "routed")
		}))
		gated := routeSecret("gated", hostPort(upstream), vault.RouteSpec{})
		gated.Confirm = true
		h.createVault(gated)
		h.grant("gated")
		h.confirmer.deny["gated"] = true
	})
	h.unlock()
	response, err := h.tunnelClient(protocol.RouteHost("gated")).Get("http://x/some/path")
	if err != nil {
		t.Fatal(err)
	}
	expectRefusal(t, response, http.StatusForbidden,
		"the gated secret was not approved on the host")
	h.confirmer.mutex.Lock()
	details := append([]string(nil), h.confirmer.details...)
	h.confirmer.mutex.Unlock()
	if len(details) != 1 || details[0] != "GET /some/path" {
		t.Fatalf("confirmer details = %v", details)
	}
}

// TestInterceptRoute checks TLS interception: the certificate bundle
// trusts the leaf, and the request is forwarded with the route's
// credential header.
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
		t.Fatal("bundle holds no certificate")
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

// TestSignHMAC checks the /v1/sign endpoint for HMAC secrets:
// success, various refusals, and rate limiting.
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
		`prison holds no signing secret called "nope" for this project`)
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

// TestKeysWithoutAgent checks /v1/keys when no SSH agent is
// available, and that signing returns 503.
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
	response, _ = api.Get("http://x/v1/nothing")
	expectRefusal(t, response, http.StatusNotFound, unknownPathMessage)
}
