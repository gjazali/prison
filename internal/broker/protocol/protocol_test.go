package protocol

import "testing"

func TestParseZoneName(t *testing.T) {
	cases := []struct {
		host string
		kind string
		name string
		ok   bool
	}{
		{"broker.prison.internal", KindBroker, "", true},
		{"BROKER.prison.internal.", KindBroker, "", true},
		{"claude.inmate.prison.internal", KindInmate, "claude", true},
		{"stripe.route.prison.internal", KindRoute, "stripe", true},
		{"prison.internal", "", "", false},
		{"x.other.prison.internal", "", "", false},
		{"a.b.inmate.prison.internal", "", "", false},
		{".inmate.prison.internal", "", "", false},
		{"inmate.prison.internal", "", "", false},
		{"example.com", "", "", false},
		{"notprison.internal", "", "", false},
	}
	for _, c := range cases {
		kind, name, ok := ParseZoneName(c.host)
		if kind != c.kind || name != c.name || ok != c.ok {
			t.Errorf("ParseZoneName(%q) = %q %q %v, want %q %q %v",
				c.host, kind, name, ok, c.kind, c.name, c.ok)
		}
	}
	if !InZone("x.route.prison.internal") || InZone("example.com") {
		t.Error("InZone returned a wrong result")
	}
}

func TestProxyAuthorization(t *testing.T) {
	header := ProxyAuthorization("secret-token")
	token, ok := TokenFromProxyAuthorization(header)
	if !ok || token != "secret-token" {
		t.Fatalf("TokenFromProxyAuthorization = %q, %v, want secret-token, true",
			token, ok)
	}
	for _, bad := range []string{"", "Bearer abc", "Basic !!!", "Basic cHJpc29uOg=="} {
		if _, ok := TokenFromProxyAuthorization(bad); ok {
			t.Errorf("TokenFromProxyAuthorization(%q) = ok, want not ok", bad)
		}
	}
}
