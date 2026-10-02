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

const sentinelTTL = 60

const maximumQueryBytes = 4096

const maximumReportedRefusals = 1024

// resolver is the DNS server of the box. It returns a sentinel address for
// an allowed name and NXDOMAIN for other names.
type resolver struct {
	sentinels *sentinelAllocator
	allowList *allowListHolder
	logger    *log.Logger

	refusalMutex sync.Mutex
	reported     map[string]bool
	reportedFull bool
}

func (r *resolver) noteRefusal(name string) {
	r.refusalMutex.Lock()
	defer r.refusalMutex.Unlock()
	if r.reportedFull || r.reported[name] {
		return
	}
	if len(r.reported) >= maximumReportedRefusals {
		r.reportedFull = true
		r.logger.Printf("resolver: logged %d refused names. Later refusals"+
			" are not logged", maximumReportedRefusals)
		return
	}
	if r.reported == nil {
		r.reported = make(map[string]bool)
	}
	r.reported[name] = true
	r.logger.Printf("resolver: %s is not in the allowlist. Add it to"+
		" egress.hosts in prison.toml", name)
}

func (r *resolver) resolves(name string) bool {
	return protocol.InZone(name) || r.allowList.Current().HostMatches(name)
}

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

func (r *resolver) serve(conn net.PacketConn) error {
	buffer := make([]byte, maximumQueryBytes)
	for {
		count, peer, err := conn.ReadFrom(buffer)
		if err != nil {
			return fmt.Errorf("cannot read DNS queries: %w", err)
		}
		if response, ok := r.answer(buffer[:count]); ok {
			_, _ = conn.WriteTo(response, peer)
		}
	}
}

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
		return nil, fmt.Errorf("%s returned no address for %s",
			address, name)
	}
	return addresses, nil
}
