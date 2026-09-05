// Package protocol defines the shared constants between the broker
// and the guest agent: the DNS zone, API paths, and token format.
package protocol

import (
	"encoding/base64"
	"strings"
)

// Zone is the reserved DNS suffix for in-box resolution.
const Zone = "prison.internal"

// BrokerHost is the hostname for the box-facing broker API.
const BrokerHost = "broker." + Zone

// Name kinds for ParseZoneName results.
const (
	KindBroker = "broker"
	KindInmate = "inmate"
	KindRoute  = "route"
)

// DefaultBrokerPort is the default TCP port for the broker.
const DefaultBrokerPort = 8787

// API paths served at BrokerHost.
const (
	PathHosts        = "/v1/hosts"
	PathCertificates = "/v1/certificates"
	PathEnvironment  = "/v1/environment"
	PathKeys         = "/v1/keys"
	PathSign         = "/v1/sign"
)

// ProxyUser is the username in Basic auth credentials. The broker
// reads only the password (the project token).
const ProxyUser = "prison"

// InmateHost returns the zone hostname for reaching an inmate.
func InmateHost(inmate string) string {
	return inmate + ".inmate." + Zone
}

// RouteHost returns the zone hostname for a route secret.
func RouteHost(secret string) string {
	return secret + ".route." + Zone
}

// InZone returns true if host is in the reserved zone.
func InZone(host string) bool {
	host = normalize(host)
	return host == Zone || strings.HasSuffix(host, "."+Zone)
}

// ParseZoneName splits a zone hostname into its kind and label.
// Returns ok false for names outside the zone or with an unexpected
// shape.
func ParseZoneName(host string) (kind, name string, ok bool) {
	host = normalize(host)
	if host == BrokerHost {
		return KindBroker, "", true
	}
	rest, found := strings.CutSuffix(host, "."+Zone)
	if !found {
		return "", "", false
	}
	label, kindLabel, hasKind := strings.Cut(rest, ".")
	if !hasKind || label == "" || strings.Contains(kindLabel, ".") {
		return "", "", false
	}
	switch kindLabel {
	case KindInmate, KindRoute:
		return kindLabel, label, true
	}
	return "", "", false
}

// ProxyAuthorization builds the Proxy-Authorization header value for
// the given token.
func ProxyAuthorization(token string) string {
	credentials := ProxyUser + ":" + token
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(credentials))
}

// TokenFromProxyAuthorization extracts the token from a Basic
// Proxy-Authorization header. Returns ok false if the scheme, encoding,
// or password is invalid.
func TokenFromProxyAuthorization(header string) (token string, ok bool) {
	scheme, encoded, found := strings.Cut(strings.TrimSpace(header), " ")
	if !found || !strings.EqualFold(scheme, "Basic") {
		return "", false
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encoded))
	if err != nil {
		return "", false
	}
	_, password, hasColon := strings.Cut(string(decoded), ":")
	if !hasColon || password == "" {
		return "", false
	}
	return password, true
}

// normalize lowercases a hostname and strips a trailing dot.
func normalize(host string) string {
	return strings.ToLower(strings.TrimSuffix(host, "."))
}
