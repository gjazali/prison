// Package protocol holds the names and formats that the broker and the
// guest agent share.
package protocol

import (
	"encoding/base64"
	"strings"
)

const Zone = "prison.internal"

const BrokerHost = "broker." + Zone

const (
	KindBroker = "broker"
	KindInmate = "inmate"
	KindRoute  = "route"
)

const DefaultBrokerPort = 8787

const (
	PathHosts        = "/v1/hosts"
	PathCertificates = "/v1/certificates"
	PathEnvironment  = "/v1/environment"
	PathKeys         = "/v1/keys"
	PathSign         = "/v1/sign"
)

// ProxyUser is a fixed user name. The broker reads only the password,
// which is the project token.
const ProxyUser = "prison"

func InmateHost(inmate string) string {
	return inmate + ".inmate." + Zone
}

func RouteHost(secret string) string {
	return secret + ".route." + Zone
}

func InZone(host string) bool {
	host = normalize(host)
	return host == Zone || strings.HasSuffix(host, "."+Zone)
}

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

func ProxyAuthorization(token string) string {
	credentials := ProxyUser + ":" + token
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(credentials))
}

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

func normalize(host string) string {
	return strings.ToLower(strings.TrimSuffix(host, "."))
}
