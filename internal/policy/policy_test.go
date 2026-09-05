package policy

import (
	"strings"
	"testing"
)

// TestParsePattern checks every documented pattern form and the
// refusals.
func TestParsePattern(t *testing.T) {
	cases := []struct {
		in       string
		want     string
		wildcard bool
		all      bool
		ports    []int
		fails    bool
	}{
		{in: "example.com", want: "example.com", ports: []int{80, 443}},
		{in: "Example.COM.", want: "example.com", ports: []int{80, 443}},
		{in: "*.example.com", want: "*.example.com", wildcard: true, ports: []int{80, 443}},
		{in: "example.com:8443", want: "example.com:8443", ports: []int{8443}},
		{in: "*.example.com:8081", want: "*.example.com:8081", wildcard: true, ports: []int{8081}},
		{in: "example.com:*", want: "example.com:*", all: true},
		{in: "https://example.com", fails: true},
		{in: "example.com/path", fails: true},
		{in: "user@example.com", fails: true},
		{in: "example.com:0", fails: true},
		{in: "example.com:70000", fails: true},
		{in: "example.com:abc", fails: true},
		{in: ":443", fails: true},
		{in: "*", fails: true},
		{in: "foo.*.com", fails: true},
		{in: "", fails: true},
		{in: "::1", fails: true},
	}
	for _, c := range cases {
		got, err := ParsePattern(c.in)
		if c.fails {
			if err == nil {
				t.Errorf("ParsePattern(%q) accepted, want refusal", c.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParsePattern(%q): %v", c.in, err)
			continue
		}
		if got.String() != c.want || got.Wildcard != c.wildcard || got.AllPorts != c.all {
			t.Errorf("ParsePattern(%q) = %+v, want %s", c.in, got, c.want)
		}
		if !c.all && strings.Join(intsToStrings(got.Ports), ",") != strings.Join(intsToStrings(c.ports), ",") {
			t.Errorf("ParsePattern(%q) ports = %v, want %v", c.in, got.Ports, c.ports)
		}
	}
}

// TestAllows checks matching, wildcard depth, port refusal, and
// suggestions.
func TestAllows(t *testing.T) {
	patterns, err := ParseStrings([]string{"example.com", "*.wild.org:8081", "any.net:*"})
	if err != nil {
		t.Fatal(err)
	}
	list := NewAllowList(patterns)
	cases := []struct {
		host  string
		port  int
		allow bool
		known bool
	}{
		{"example.com", 443, true, true},
		{"example.com", 80, true, true},
		{"example.com", 22, false, true},
		{"www.example.com", 443, false, false},
		{"wild.org", 8081, true, true},
		{"a.wild.org", 8081, true, true},
		{"a.b.wild.org", 8081, true, true},
		{"a.wild.org", 443, false, true},
		{"evilwild.org", 8081, false, false},
		{"any.net", 5432, true, true},
		{"ANY.NET.", 22, true, true},
		{"unknown.example", 443, false, false},
	}
	for _, c := range cases {
		got := list.Allows(c.host, c.port)
		if got.Allowed != c.allow || got.HostKnown != c.known {
			t.Errorf("Allows(%q, %d) = %+v, want allowed=%v known=%v", c.host, c.port, got, c.allow, c.known)
		}
		if !got.Allowed && got.HostKnown && got.Suggested == "" {
			t.Errorf("Allows(%q, %d) refused a known host without a suggestion", c.host, c.port)
		}
	}
}

// TestParseListAndUnion checks comments, blanks, deduplication,
// and the union with the default floor.
func TestParseListAndUnion(t *testing.T) {
	text := "# floor\nexample.com\n\nexample.com # again\nother.org:8443\n"
	patterns, err := ParseList(strings.NewReader(text))
	if err != nil {
		t.Fatal(err)
	}
	if len(patterns) != 3 {
		t.Fatalf("ParseList returned %d patterns, want 3", len(patterns))
	}
	list := NewAllowList(patterns, DefaultFloor())
	if got := len(list.Patterns()); got != 2+len(DefaultFloor()) {
		t.Errorf("union has %d patterns, want %d", got, 2+len(DefaultFloor()))
	}
	if _, err := ParseList(strings.NewReader("ok.com\nbad host\n")); err == nil || !strings.Contains(err.Error(), "line 2") {
		t.Errorf("ParseList did not name the bad line: %v", err)
	}
	ports, all := list.PortsForHost("other.org")
	if all || len(ports) != 1 || ports[0] != 8443 {
		t.Errorf("PortsForHost(other.org) = %v %v", ports, all)
	}
}

// intsToStrings converts a slice of ints to strings.
func intsToStrings(values []int) []string {
	out := make([]string, len(values))
	for i, v := range values {
		out[i] = strings.TrimSpace(strings.Repeat(" ", 0) + itoa(v))
	}
	return out
}

// itoa converts an int to its decimal string.
func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	digits := ""
	for v > 0 {
		digits = string(rune('0'+v%10)) + digits
		v /= 10
	}
	return digits
}
