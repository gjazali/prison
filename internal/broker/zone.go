package broker

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"prison/internal/broker/brokerlog"
	"prison/internal/broker/protocol"
	"prison/internal/broker/relay"
	"prison/internal/broker/signing"
	"prison/internal/vault"
)

// maxSignBodyBytes is the size limit for signing request bodies.
const maxSignBodyBytes = 16 << 20

// unknownPathMessage is the 404 text for unrecognized broker paths.
const unknownPathMessage = "prison answers /v1/hosts, /v1/certificates, " +
	"/v1/environment, /v1/keys, and /v1/sign here"

// keyDescription is one entry in the GET /v1/keys response. PublicKey
// is null when the key is not available from the SSH agent.
type keyDescription struct {
	Secret      string  `json:"secret"`
	Algorithm   string  `json:"algorithm"`
	Fingerprint string  `json:"fingerprint"`
	Namespace   string  `json:"namespace"`
	PublicKey   *string `json:"public_key"`
	Available   bool    `json:"available"`
}

// brokerAPIHandler returns an HTTP handler for the broker API of
// one project.
func (broker *Broker) brokerAPIHandler(projectID string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		snapshot := broker.snapshot(projectID)
		if snapshot == nil {
			relay.WriteError(w, http.StatusNotFound, errNoProject.Error())
			return
		}
		wantMethod := http.MethodGet
		if r.URL.Path == protocol.PathSign {
			wantMethod = http.MethodPost
		}
		switch r.URL.Path {
		case protocol.PathHosts, protocol.PathCertificates, protocol.PathEnvironment,
			protocol.PathKeys, protocol.PathSign:
		default:
			relay.WriteError(w, http.StatusNotFound, unknownPathMessage)
			return
		}
		if r.Method != wantMethod {
			w.Header().Set("Allow", wantMethod)
			relay.WriteError(w, http.StatusMethodNotAllowed,
				fmt.Sprintf("%s takes %s only", r.URL.Path, wantMethod))
			return
		}
		switch r.URL.Path {
		case protocol.PathHosts:
			writeTextLines(w, broker.allowListFor(snapshot).Strings())
		case protocol.PathCertificates:
			broker.serveCertificates(w, snapshot)
		case protocol.PathEnvironment:
			writeTextLines(w, broker.environmentLines(snapshot))
		case protocol.PathKeys:
			broker.serveKeys(w, r, snapshot)
		case protocol.PathSign:
			broker.serveSign(w, r, snapshot)
		}
	})
}

// writeTextLines writes lines as a text/plain 200 response, one per
// line.
func writeTextLines(w http.ResponseWriter, lines []string) {
	var body strings.Builder
	for _, line := range lines {
		body.WriteString(line)
		body.WriteByte('\n')
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(body.Len()))
	w.WriteHeader(http.StatusOK)
	io.WriteString(w, body.String())
}

// serveCertificates responds with the PEM bundle of CA certificates
// for the project's intercepting routes. Empty when the vault is
// locked or there are no intercepts.
func (broker *Broker) serveCertificates(w http.ResponseWriter, snapshot *projectSnapshot) {
	var bundle []byte
	hosts := broker.interceptHosts(snapshot)
	if authorities := broker.currentAuthorities(); authorities != nil && len(hosts) > 0 {
		built, err := authorities.Bundle(hosts)
		if err != nil {
			relay.WriteError(w, http.StatusInternalServerError,
				"prison cannot produce the certificate bundle: "+err.Error())
			return
		}
		bundle = built
	}
	w.Header().Set("Content-Type", "application/x-pem-file")
	w.Header().Set("Content-Length", strconv.Itoa(len(bundle)))
	w.WriteHeader(http.StatusOK)
	w.Write(bundle)
}

// serveKeys responds with the project's granted signing secrets,
// sorted by name. Queries the SSH agent for public keys.
func (broker *Broker) serveKeys(w http.ResponseWriter, r *http.Request, snapshot *projectSnapshot) {
	keys := []keyDescription{}
	for _, secret := range broker.grantedSecrets(snapshot) {
		if secret.Mode != vault.ModeSign || secret.Sign == nil {
			continue
		}
		described := keyDescription{
			Secret:      secret.Name,
			Algorithm:   secret.Sign.Algorithm,
			Fingerprint: secret.Sign.Fingerprint,
			Namespace:   secret.Sign.Namespace,
			Available:   secret.Sign.Algorithm != vault.AlgorithmSSHAgent,
		}
		if secret.Sign.Algorithm == vault.AlgorithmSSHAgent {
			if key, err := broker.agent.FindKey(r.Context(), secret.Sign.Fingerprint); err == nil {
				publicKey := key.PublicKey
				described.PublicKey = &publicKey
				described.Available = true
			}
		}
		keys = append(keys, described)
	}
	writeJSON(w, http.StatusOK, map[string]any{"keys": keys})
}

// serveSign handles POST /v1/sign requests. Validates the body,
// grant, rate limit, and confirmation, then signs with HMAC or the
// SSH agent.
func (broker *Broker) serveSign(w http.ResponseWriter, r *http.Request, snapshot *projectSnapshot) {
	started := time.Now()
	secretName := ""
	status := 0
	defer func() {
		broker.record(brokerlog.Entry{
			Kind: brokerlog.KindSign, Project: snapshot.id, Secret: secretName,
			Status: status, DurationMS: elapsedMilliseconds(started),
		})
	}()
	refuse := func(code int, message string) {
		status = code
		relay.WriteError(w, code, message)
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxSignBodyBytes))
	if err != nil {
		refuse(http.StatusBadRequest, "cannot read the request body: "+err.Error())
		return
	}
	var parsed any
	if err := json.Unmarshal(body, &parsed); err != nil {
		refuse(http.StatusBadRequest, "the request body is not JSON")
		return
	}
	request, ok := parsed.(map[string]any)
	if !ok {
		refuse(http.StatusBadRequest, "the request body is not an object")
		return
	}
	secretName, _ = request["secret"].(string)
	secret := broker.grantedSecret(snapshot, secretName)
	if secret == nil || secret.Mode != vault.ModeSign || secret.Sign == nil {
		refuse(http.StatusNotFound, fmt.Sprintf(
			"prison holds no signing secret called %q for this project", secretName))
		return
	}
	data, ok := decodeSignData(request["data"])
	if !ok {
		refuse(http.StatusBadRequest, `"data" must be base64 of the bytes to sign`)
		return
	}
	if !broker.limiter.Allow(rateKey(snapshot.id, secretName), secret.Sign.RateLimit) {
		refuse(http.StatusTooManyRequests, fmt.Sprintf(
			"the %s secret allows %d signatures a minute", secretName, secret.Sign.RateLimit))
		return
	}
	if secret.Confirm && !broker.confirmer.Confirm(snapshot.id, secretName,
		fmt.Sprintf("It is asking for a signature over %d bytes.", len(data))) {
		refuse(http.StatusForbidden, fmt.Sprintf(
			"the %s secret was not approved on the host", secretName))
		return
	}
	if secret.Sign.Algorithm == vault.AlgorithmHMACSHA256 {
		digest := signing.HMACSHA256([]byte(secret.Value), data)
		status = http.StatusOK
		writeJSON(w, status, map[string]any{
			"secret":    secretName,
			"algorithm": vault.AlgorithmHMACSHA256,
			"signature": base64.StdEncoding.EncodeToString(digest),
		})
		return
	}
	namespace, _ := request["namespace"].(string)
	if namespace == "" {
		namespace = secret.Sign.Namespace
	}
	if namespace == "" {
		namespace = "git"
	}
	signature, err := broker.agent.SignSSHSIG(r.Context(), secret.Sign.Fingerprint,
		namespace, data)
	if err != nil {
		refuse(http.StatusServiceUnavailable, err.Error())
		return
	}
	status = http.StatusOK
	writeJSON(w, status, map[string]any{
		"secret":     secretName,
		"algorithm":  vault.AlgorithmSSHAgent,
		"namespace":  signature.Namespace,
		"public_key": signature.PublicKey,
		"signature":  string(signature.Armored),
	})
}

// decodeSignData decodes the base64 "data" field from a signing
// request. Returns empty bytes for an absent field. Returns false if
// the field is not valid base64.
func decodeSignData(field any) ([]byte, bool) {
	if field == nil {
		return []byte{}, true
	}
	text, ok := field.(string)
	if !ok {
		return nil, false
	}
	data, err := base64.StdEncoding.DecodeString(text)
	if err != nil {
		return nil, false
	}
	return data, true
}

// rateKey returns the rate limiter key for a project's secret.
func rateKey(projectID, secretName string) string {
	return projectID + "/" + secretName
}
