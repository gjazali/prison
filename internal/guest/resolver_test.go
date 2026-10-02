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

func newTestResolver(t *testing.T, patterns ...string) *resolver {
	t.Helper()
	return newLoggingTestResolver(t, io.Discard, patterns...)
}

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

func ask(t *testing.T, r *resolver, name string,
	recordType dnsmessage.Type) (dnsmessage.RCode, []netip.Addr) {
	t.Helper()
	query, err := buildQuery(0x1234, name, recordType)
	if err != nil {
		t.Fatal(err)
	}
	response, ok := r.answer(query)
	if !ok {
		t.Fatalf("answer(%s %v) = no response, want a response", name,
			recordType)
	}
	var parser dnsmessage.Parser
	header, err := parser.Start(response)
	if err != nil {
		t.Fatal(err)
	}
	if header.ID != 0x1234 || !header.Response {
		t.Fatalf("header = %+v, want the query ID in a response", header)
	}
	question, err := parser.Question()
	if err != nil || question.Name.String() != name+"." {
		t.Fatalf("question = %v, %v, want %s.", question, err, name)
	}
	code, addresses, err := parseAddressAnswer(response)
	if err != nil {
		t.Fatal(err)
	}
	return code, addresses
}

func TestResolverAnswersAllowedAndZoneNames(t *testing.T) {
	r := newTestResolver(t, "example.test", "*.wild.test:8443")
	code, addresses := ask(t, r, "example.test", dnsmessage.TypeA)
	if code != dnsmessage.RCodeSuccess || len(addresses) != 1 {
		t.Fatalf("ask(example.test) = %v, %v, want success and 1 address",
			code, addresses)
	}
	if !netip.MustParsePrefix("127.99.0.0/16").Contains(addresses[0]) {
		t.Fatalf("address = %v, want inside 127.99.0.0/16", addresses[0])
	}
	_, again := ask(t, r, "EXAMPLE.test", dnsmessage.TypeA)
	if again[0] != addresses[0] {
		t.Fatalf("ask(EXAMPLE.test) = %v, want %v", again, addresses[0])
	}
	_, deep := ask(t, r, "a.b.wild.test", dnsmessage.TypeA)
	if len(deep) != 1 || deep[0] == addresses[0] {
		t.Fatalf("ask(a.b.wild.test) = %v, want 1 new address", deep)
	}
	_, broker := ask(t, r, protocol.BrokerHost, dnsmessage.TypeA)
	if len(broker) != 1 {
		t.Fatalf("ask(broker) = %v, want 1 address", broker)
	}
	empty := newTestResolver(t)
	_, inmate := ask(t, empty, protocol.InmateHost("claude"), dnsmessage.TypeA)
	if len(inmate) != 1 {
		t.Fatalf("ask(inmate) = %v, want 1 address", inmate)
	}
	if name, _ := r.sentinels.Lookup(addresses[0]); name != "example.test" {
		t.Fatalf("Lookup() = %q, want example.test", name)
	}
}

func TestResolverRefusesUnknownNamesAndOtherTypes(t *testing.T) {
	r := newTestResolver(t, "example.test")
	code, addresses := ask(t, r, "nope.test", dnsmessage.TypeA)
	if code != dnsmessage.RCodeNameError || len(addresses) != 0 {
		t.Fatalf("ask(nope.test) = %v, %v, want NXDOMAIN and no address",
			code, addresses)
	}
	code, _ = ask(t, r, "nope.test", dnsmessage.TypeAAAA)
	if code != dnsmessage.RCodeNameError {
		t.Fatalf("ask(nope.test AAAA) = %v, want NXDOMAIN", code)
	}
	code, addresses = ask(t, r, "example.test", dnsmessage.TypeAAAA)
	if code != dnsmessage.RCodeSuccess || len(addresses) != 0 {
		t.Fatalf("ask(example.test AAAA) = %v, %v, want success and no "+
			"address", code, addresses)
	}
	code, addresses = ask(t, r, "example.test", dnsmessage.TypeMX)
	if code != dnsmessage.RCodeSuccess || len(addresses) != 0 {
		t.Fatalf("ask(example.test MX) = %v, %v, want success and no "+
			"address", code, addresses)
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
		t.Fatalf("AnswerHeader() = %+v, %v, want TTL %d", header, err,
			sentinelTTL)
	}
}

func TestResolverLogsEachRefusedNameOnce(t *testing.T) {
	var logged bytes.Buffer
	r := newLoggingTestResolver(t, &logged, "example.test")
	ask(t, r, "nope.test", dnsmessage.TypeA)
	first := logged.String()
	if !strings.Contains(first, "nope.test") ||
		!strings.Contains(first, "egress.hosts") {
		t.Fatalf("log = %q, want nope.test and egress.hosts", first)
	}
	if lines := strings.Count(first, "\n"); lines != 1 {
		t.Fatalf("log lines = %d, want 1: %q", lines, first)
	}
	ask(t, r, "nope.test", dnsmessage.TypeA)
	ask(t, r, "nope.test", dnsmessage.TypeAAAA)
	ask(t, r, "NOPE.test", dnsmessage.TypeA)
	ask(t, r, "example.test", dnsmessage.TypeA)
	if again := logged.String(); again != first {
		t.Fatalf("log = %q, want %q", again, first)
	}
	ask(t, r, "other.test", dnsmessage.TypeA)
	if lines := strings.Count(logged.String(), "\n"); lines != 2 {
		t.Fatalf("log lines = %d, want 2", lines)
	}
}

func TestResolverStopsNamingRefusalsAtTheCap(t *testing.T) {
	var logged bytes.Buffer
	r := newLoggingTestResolver(t, &logged)
	for i := 0; i < maximumReportedRefusals+10; i++ {
		ask(t, r, fmt.Sprintf("host%d.test", i), dnsmessage.TypeA)
	}
	lines := strings.Count(logged.String(), "\n")
	if lines != maximumReportedRefusals+1 {
		t.Fatalf("log lines = %d, want %d", lines,
			maximumReportedRefusals+1)
	}
	if !strings.Contains(logged.String(), "Later refusals are not logged") {
		t.Fatal("log = no cap line, want a cap line")
	}
	if strings.Contains(logged.String(), "host1030.test") {
		t.Fatal("log = has host1030.test, want no name past the cap")
	}
}

func TestResolverDropsMalformedPackets(t *testing.T) {
	r := newTestResolver(t, "example.test")
	if _, ok := r.answer([]byte{1, 2, 3}); ok {
		t.Fatal("answer(garbage) = ok, want dropped")
	}
	query, _ := buildQuery(1, "example.test", dnsmessage.TypeA)
	response, _ := r.answer(query)
	if _, ok := r.answer(response); ok {
		t.Fatal("answer(response) = ok, want dropped")
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
		t.Fatal("answer(two questions) = ok, want dropped")
	}
	chaos := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: 3})
	_ = chaos.StartQuestions()
	_ = chaos.Question(dnsmessage.Question{Name: name, Type: dnsmessage.TypeA,
		Class: dnsmessage.ClassCHAOS})
	packet, _ = chaos.Finish()
	if _, ok := r.answer(packet); ok {
		t.Fatal("answer(CHAOS) = ok, want dropped")
	}
}

func TestResolverFollowsAllowListReplacement(t *testing.T) {
	r := newTestResolver(t)
	if code, _ := ask(t, r, "late.test", dnsmessage.TypeA); code !=
		dnsmessage.RCodeNameError {
		t.Fatalf("ask(late.test) = %v before refresh, want NXDOMAIN", code)
	}
	list, count, err := parseHostsAnswer("# floor\nlate.test:*\n\n")
	if err != nil || count != 1 {
		t.Fatalf("parseHostsAnswer() = %d, %v, want 1, nil", count, err)
	}
	r.allowList.Replace(list)
	if code, _ := ask(t, r, "late.test", dnsmessage.TypeA); code !=
		dnsmessage.RCodeSuccess {
		t.Fatalf("ask(late.test) = %v after refresh, want success", code)
	}
}

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
		t.Fatalf("queryResolver(broker) = %v, %v, want 1 address", addresses,
			err)
	}
	if _, err := queryResolver(conn.LocalAddr().String(), "nope.test",
		2*time.Second); err == nil {
		t.Fatal("queryResolver(nope.test) = nil error, want an error")
	}
}
