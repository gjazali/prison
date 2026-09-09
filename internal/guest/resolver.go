package guest

import (
	"errors"
	"fmt"
	"log"
	"net"
	"net/netip"
	"sync"
	"time"

	"golang.org/x/net/dns/dnsmessage"

	"prison/internal/broker/protocol"
)

// sentinelTTL is the TTL in seconds for every A answer.
const sentinelTTL = 60

// maximumQueryBytes is the size limit for a single DNS query datagram.
const maximumQueryBytes = 4096

// maximumReportedRefusals is the size limit for how many distinct refused names
// the resolver remembers.
const maximumReportedRefusals = 1024

// resolver is the box's DNS server. Names in the reserved zone or
// matched by the allowlist resolve to sentinel addresses. All other
// names return NXDOMAIN.
type resolver struct {
	sentinels *sentinelAllocator
	allowList *allowListHolder
	logger    *log.Logger

	refusalMutex sync.Mutex
	reported     map[string]bool
	reportedFull bool
}

// noteRefusal logs the names that aren't in the allowlist.
func (r *resolver) noteRefusal(name string) {
	r.refusalMutex.Lock()
	defer r.refusalMutex.Unlock()
	if r.reportedFull || r.reported[name] {
		return
	}
	if len(r.reported) >= maximumReportedRefusals {
		r.reportedFull = true
		r.logger.Printf("resolver: %d refused names logged; the rest will"+
			" go unnamed", maximumReportedRefusals)
		return
	}
	if r.reported == nil {
		r.reported = make(map[string]bool)
	}
	r.reported[name] = true
	r.logger.Printf("resolver: %s does not resolve, nothing in the allowlist"+
		" matches it; add it to egress.hosts in prison.toml", name)
}

// resolves returns true if name should get an address. Names in the
// zone always resolve. Others resolve if the allowlist matches.
func (r *resolver) resolves(name string) bool {
	return protocol.InZone(name) || r.allowList.Current().HostMatches(name)
}

// answer builds a DNS response for one query packet. Returns false
// for non-INET or multi-question packets. Matched names get an A
// record. Unmatched names get NXDOMAIN.
func (r *resolver) answer(packet []byte) ([]byte, bool) {
	var parser dnsmessage.Parser
	header, err := parser.Start(packet)
	if err != nil || header.Response {
		return nil, false
	}
	question, err := parser.Question()
	if err != nil {
		return nil, false
	}
	if _, err := parser.Question(); err != dnsmessage.ErrSectionDone {
		return nil, false
	}
	if question.Class != dnsmessage.ClassINET {
		return nil, false
	}
	name := normalizeName(question.Name.String())
	code := dnsmessage.RCodeSuccess
	var address netip.Addr
	haveAddress := false
	switch {
	case !r.resolves(name):
		code = dnsmessage.RCodeNameError
		r.noteRefusal(name)
	case question.Type == dnsmessage.TypeA:
		address, haveAddress = r.sentinels.Allocate(name)
		if !haveAddress {
			code = dnsmessage.RCodeServerFailure
		}
	}
	builder := dnsmessage.NewBuilder(make([]byte, 0, 512), dnsmessage.Header{
		ID:                 header.ID,
		Response:           true,
		OpCode:             header.OpCode,
		Authoritative:      true,
		RecursionDesired:   header.RecursionDesired,
		RecursionAvailable: true,
		RCode:              code,
	})
	builder.EnableCompression()
	if err := builder.StartQuestions(); err != nil {
		return nil, false
	}
	if err := builder.Question(question); err != nil {
		return nil, false
	}
	if haveAddress {
		if err := builder.StartAnswers(); err != nil {
			return nil, false
		}
		resourceHeader := dnsmessage.ResourceHeader{
			Name:  question.Name,
			Type:  dnsmessage.TypeA,
			Class: dnsmessage.ClassINET,
			TTL:   sentinelTTL,
		}
		resource := dnsmessage.AResource{A: address.As4()}
		if err := builder.AResource(resourceHeader, resource); err != nil {
			return nil, false
		}
	}
	response, err := builder.Finish()
	if err != nil {
		return nil, false
	}
	return response, true
}

// serve answers DNS queries on conn until a read error occurs.
// Returns the read error. Write failures are ignored.
func (r *resolver) serve(conn net.PacketConn) error {
	buffer := make([]byte, maximumQueryBytes)
	for {
		count, peer, err := conn.ReadFrom(buffer)
		if err != nil {
			return fmt.Errorf("the resolver stopped reading: %w", err)
		}
		if response, ok := r.answer(buffer[:count]); ok {
			_, _ = conn.WriteTo(response, peer)
		}
	}
}

// buildQuery builds a DNS query packet. Takes an identifier, name,
// and record type. Returns the wire-format bytes.
func buildQuery(identifier uint16, name string,
	recordType dnsmessage.Type) ([]byte, error) {
	dnsName, err := dnsmessage.NewName(name + ".")
	if err != nil {
		return nil, err
	}
	builder := dnsmessage.NewBuilder(nil, dnsmessage.Header{
		ID:               identifier,
		RecursionDesired: true,
	})
	if err := builder.StartQuestions(); err != nil {
		return nil, err
	}
	question := dnsmessage.Question{
		Name:  dnsName,
		Type:  recordType,
		Class: dnsmessage.ClassINET,
	}
	if err := builder.Question(question); err != nil {
		return nil, err
	}
	return builder.Finish()
}

// parseAddressAnswer parses a DNS response. Returns the response code
// and A record addresses. Non-A records are skipped.
func parseAddressAnswer(packet []byte) (dnsmessage.RCode, []netip.Addr, error) {
	var parser dnsmessage.Parser
	header, err := parser.Start(packet)
	if err != nil {
		return 0, nil, err
	}
	if err := parser.SkipAllQuestions(); err != nil {
		return header.RCode, nil, err
	}
	var addresses []netip.Addr
	for {
		answerHeader, err := parser.AnswerHeader()
		if errors.Is(err, dnsmessage.ErrSectionDone) {
			return header.RCode, addresses, nil
		}
		if err != nil {
			return header.RCode, addresses, err
		}
		if answerHeader.Type != dnsmessage.TypeA {
			if err := parser.SkipAnswer(); err != nil {
				return header.RCode, addresses, err
			}
			continue
		}
		resource, err := parser.AResource()
		if err != nil {
			return header.RCode, addresses, err
		}
		addresses = append(addresses, netip.AddrFrom4(resource.A))
	}
}

// queryResolver sends a DNS A query for name to the resolver at
// address. Returns the addresses from the answer, or an error.
func queryResolver(address, name string,
	timeout time.Duration) ([]netip.Addr, error) {
	packet, err := buildQuery(uint16(time.Now().UnixNano()), name,
		dnsmessage.TypeA)
	if err != nil {
		return nil, err
	}
	conn, err := net.DialTimeout("udp4", address, timeout)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(timeout)); err != nil {
		return nil, err
	}
	if _, err := conn.Write(packet); err != nil {
		return nil, err
	}
	buffer := make([]byte, maximumQueryBytes)
	count, err := conn.Read(buffer)
	if err != nil {
		return nil, err
	}
	code, addresses, err := parseAddressAnswer(buffer[:count])
	if err != nil {
		return nil, err
	}
	if code != dnsmessage.RCodeSuccess {
		return nil, fmt.Errorf("%s answered %v for %s", address, code, name)
	}
	if len(addresses) == 0 {
		return nil, fmt.Errorf("%s answered without an address for %s",
			address, name)
	}
	return addresses, nil
}
