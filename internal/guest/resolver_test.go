package guest

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"

	"prison/internal/broker/protocol"
	"prison/internal/policy"
)

// newTestResolver returns a resolver with the given allow patterns.
func newTestResolver(t *testing.T, patterns ...string) *resolver {
	t.Helper()
	return newLoggingTestResolver(t, io.Discard, patterns...)
}

// newLoggingTestResolver returns a resolver with the given allow
// patterns.
func newLoggingTestResolver(t *testing.T, sink io.Writer,
	patterns ...string) *resolver {
	t.Helper()
	parsed, err := policy.ParseStrings(patterns)
	if err != nil {
		t.Fatal(err)
	}
	holder := newAllowListHolder()
	holder.Replace(policy.NewAllowList(parsed))
	return &resolver{
		sentinels: newSentinelAllocator(),
		allowList: holder,
		logger:    log.New(sink, "", 0),
	}
}

// ask sends a DNS query and returns the response code and addresses.
func ask(t *testing.T, r *resolver, name string,
	recordType dnsmessage.Type) (dnsmessage.RCode, []netip.Addr) {
	t.Helper()
	query, err := buildQuery(0x1234, name, recordType)
	if err != nil {
		t.Fatal(err)
	}
	response, ok := r.answer(query)
	if !ok {
		t.Fatalf("no answer for %s %v", name, recordType)
	}
	var parser dnsmessage.Parser
	header, err := parser.Start(response)
	if err != nil {
		t.Fatal(err)
	}
	if header.ID != 0x1234 || !header.Response {
		t.Fatalf("header %+v does not echo the query", header)
	}
	question, err := parser.Question()
	if err != nil || question.Name.String() != name+"." {
		t.Fatalf("question echoed as %v, %v", question, err)
	}
	code, addresses, err := parseAddressAnswer(response)
	if err != nil {
		t.Fatal(err)
	}
	return code, addresses
}

// TestResolverAnswersAllowedAndZoneNames checks A answers for
// allowed names, wildcards, and zone names. Addresses are stable
// per name.
func TestResolverAnswersAllowedAndZoneNames(t *testing.T) {
	r := newTestResolver(t, "example.test", "*.wild.test:8443")
	code, addresses := ask(t, r, "example.test", dnsmessage.TypeA)
	if code != dnsmessage.RCodeSuccess || len(addresses) != 1 {
		t.Fatalf("example.test: code %v addresses %v", code, addresses)
	}
	if !netip.MustParsePrefix("127.99.0.0/16").Contains(addresses[0]) {
		t.Fatalf("address %v outside the sentinel range", addresses[0])
	}
	_, again := ask(t, r, "EXAMPLE.test", dnsmessage.TypeA)
	if again[0] != addresses[0] {
		t.Fatalf("case-folded repeat moved from %v to %v", addresses[0],
			again)
	}
	_, deep := ask(t, r, "a.b.wild.test", dnsmessage.TypeA)
	if len(deep) != 1 || deep[0] == addresses[0] {
		t.Fatalf("wildcard answer %v", deep)
	}
	_, broker := ask(t, r, protocol.BrokerHost, dnsmessage.TypeA)
	if len(broker) != 1 {
		t.Fatalf("zone name did not resolve: %v", broker)
	}
	empty := newTestResolver(t)
	_, inmate := ask(t, empty, protocol.InmateHost("claude"), dnsmessage.TypeA)
	if len(inmate) != 1 {
		t.Fatal("zone names must resolve with an empty allowlist")
	}
	if name, _ := r.sentinels.Lookup(addresses[0]); name != "example.test" {
		t.Fatalf("reverse map gave %q", name)
	}
}

// TestResolverRefusesUnknownNamesAndOtherTypes checks NXDOMAIN for
// unmatched names, empty NOERROR for AAAA and MX, and the TTL on
// A answers.
func TestResolverRefusesUnknownNamesAndOtherTypes(t *testing.T) {
	r := newTestResolver(t, "example.test")
	code, addresses := ask(t, r, "nope.test", dnsmessage.TypeA)
	if code != dnsmessage.RCodeNameError || len(addresses) != 0 {
		t.Fatalf("nope.test: code %v addresses %v", code, addresses)
	}
	code, _ = ask(t, r, "nope.test", dnsmessage.TypeAAAA)
	if code != dnsmessage.RCodeNameError {
		t.Fatalf("nope.test AAAA: code %v, want NXDOMAIN", code)
	}
	code, addresses = ask(t, r, "example.test", dnsmessage.TypeAAAA)
	if code != dnsmessage.RCodeSuccess || len(addresses) != 0 {
		t.Fatalf("example.test AAAA: code %v addresses %v", code, addresses)
	}
	code, addresses = ask(t, r, "example.test", dnsmessage.TypeMX)
	if code != dnsmessage.RCodeSuccess || len(addresses) != 0 {
		t.Fatalf("example.test MX: code %v addresses %v", code, addresses)
	}
	query, _ := buildQuery(7, "example.test", dnsmessage.TypeA)
	response, _ := r.answer(query)
	var parser dnsmessage.Parser
	if _, err := parser.Start(response); err != nil {
		t.Fatal(err)
	}
	if err := parser.SkipAllQuestions(); err != nil {
		t.Fatal(err)
	}
	header, err := parser.AnswerHeader()
	if err != nil || header.TTL != sentinelTTL {
		t.Fatalf("answer header %+v, %v; want TTL %d", header, err, sentinelTTL)
	}
}

// TestResolverLogsEachRefusedNameOnce checks that a name matching
// nothing is logged.
func TestResolverLogsEachRefusedNameOnce(t *testing.T) {
	var logged bytes.Buffer
	r := newLoggingTestResolver(t, &logged, "example.test")
	ask(t, r, "nope.test", dnsmessage.TypeA)
	first := logged.String()
	if !strings.Contains(first, "nope.test") ||
		!strings.Contains(first, "egress.hosts") {
		t.Fatalf("refusal logged as %q", first)
	}
	if lines := strings.Count(first, "\n"); lines != 1 {
		t.Fatalf("one refusal wrote %d lines: %q", lines, first)
	}
	ask(t, r, "nope.test", dnsmessage.TypeA)
	ask(t, r, "nope.test", dnsmessage.TypeAAAA)
	ask(t, r, "NOPE.test", dnsmessage.TypeA)
	ask(t, r, "example.test", dnsmessage.TypeA)
	if again := logged.String(); again != first {
		t.Fatalf("the log grew from %q to %q", first, again)
	}
	ask(t, r, "other.test", dnsmessage.TypeA)
	if lines := strings.Count(logged.String(), "\n"); lines != 2 {
		t.Fatalf("a second name wrote %d lines total", lines)
	}
}

// TestResolverStopsNamingRefusalsAtTheCap checks that the resolver
// falls silent after maximumReportedRefusals distinct names.
func TestResolverStopsNamingRefusalsAtTheCap(t *testing.T) {
	var logged bytes.Buffer
	r := newLoggingTestResolver(t, &logged)
	for i := 0; i < maximumReportedRefusals+10; i++ {
		ask(t, r, fmt.Sprintf("host%d.test", i), dnsmessage.TypeA)
	}
	lines := strings.Count(logged.String(), "\n")
	if lines != maximumReportedRefusals+1 {
		t.Fatalf("%d lines logged, want %d", lines,
			maximumReportedRefusals+1)
	}
	if !strings.Contains(logged.String(), "go unnamed") {
		t.Fatal("the cap was reached without saying so")
	}
	if strings.Contains(logged.String(), "host1030.test") {
		t.Fatal("a name past the cap was logged")
	}
}

// TestResolverDropsMalformedPackets checks that garbage, response
// packets, multi-question packets, and non-INET classes are dropped.
func TestResolverDropsMalformedPackets(t *testing.T) {
	r := newTestResolver(t, "example.test")
	if _, ok := r.answer([]byte{1, 2, 3}); ok {
		t.Fatal("garbage was answered")
	}
	query, _ := buildQuery(1, "example.test", dnsmessage.TypeA)
	response, _ := r.answer(query)
	if _, ok := r.answer(response); ok {
		t.Fatal("a response packet was answered")
	}
	name := dnsmessage.MustNewName("example.test.")
	two := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: 2})
	_ = two.StartQuestions()
	_ = two.Question(dnsmessage.Question{Name: name, Type: dnsmessage.TypeA,
		Class: dnsmessage.ClassINET})
	_ = two.Question(dnsmessage.Question{Name: name, Type: dnsmessage.TypeAAAA,
		Class: dnsmessage.ClassINET})
	packet, _ := two.Finish()
	if _, ok := r.answer(packet); ok {
		t.Fatal("a two-question packet was answered")
	}
	chaos := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: 3})
	_ = chaos.StartQuestions()
	_ = chaos.Question(dnsmessage.Question{Name: name, Type: dnsmessage.TypeA,
		Class: dnsmessage.ClassCHAOS})
	packet, _ = chaos.Finish()
	if _, ok := r.answer(packet); ok {
		t.Fatal("a CHAOS question was answered")
	}
}

// TestResolverFollowsAllowListReplacement checks that a replaced
// allow list takes effect for subsequent queries.
func TestResolverFollowsAllowListReplacement(t *testing.T) {
	r := newTestResolver(t)
	if code, _ := ask(t, r, "late.test", dnsmessage.TypeA); code !=
		dnsmessage.RCodeNameError {
		t.Fatalf("before refresh: %v", code)
	}
	list, count, err := parseHostsAnswer("# floor\nlate.test:*\n\n")
	if err != nil || count != 1 {
		t.Fatalf("parseHostsAnswer: %d, %v", count, err)
	}
	r.allowList.Replace(list)
	if code, _ := ask(t, r, "late.test", dnsmessage.TypeA); code !=
		dnsmessage.RCodeSuccess {
		t.Fatalf("after refresh: %v", code)
	}
}

// TestResolverServesOverUDP runs serve on a loopback socket and
// checks queryResolver against it.
func TestResolverServesOverUDP(t *testing.T) {
	r := newTestResolver(t, "example.test")
	conn, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	go func() { _ = r.serve(conn) }()
	addresses, err := queryResolver(conn.LocalAddr().String(),
		protocol.BrokerHost, 2*time.Second)
	if err != nil || len(addresses) != 1 {
		t.Fatalf("queryResolver: %v, %v", addresses, err)
	}
	if _, err := queryResolver(conn.LocalAddr().String(), "nope.test",
		2*time.Second); err == nil {
		t.Fatal("an NXDOMAIN answer did not fail queryResolver")
	}
}
