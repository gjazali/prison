package broker

import (
	"bufio"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/yamux"

	"prison/internal/broker/control"
	"prison/internal/broker/protocol"
	"prison/internal/state"
)

type testGuestUplink struct {
	socketPath string
	listener   net.Listener
	mu         sync.Mutex
	session    *yamux.Session
	ready      chan struct{}
	closed     bool
}

func startTestGuestUplink(t *testing.T) *testGuestUplink {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "bu-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	socketPath := filepath.Join(dir, "u.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen unix: %v", err)
	}
	guest := &testGuestUplink{
		socketPath: socketPath,
		listener:   listener,
		ready:      make(chan struct{}),
	}
	t.Cleanup(func() {
		guest.mu.Lock()
		guest.closed = true
		if guest.session != nil {
			guest.session.Close()
		}
		guest.mu.Unlock()
		listener.Close()
	})
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			cfg := yamux.DefaultConfig()
			cfg.LogOutput = io.Discard
			session, err := yamux.Client(conn, cfg)
			if err != nil {
				conn.Close()
				continue
			}
			guest.mu.Lock()
			if guest.closed {
				guest.mu.Unlock()
				session.Close()
				return
			}
			prev := guest.session
			guest.session = session
			select {
			case <-guest.ready:
			default:
				close(guest.ready)
			}
			guest.mu.Unlock()
			if prev != nil {
				prev.Close()
			}
			go guest.serveForwards(session)
		}
	}()
	return guest
}

func (g *testGuestUplink) serveForwards(session *yamux.Session) {
	defer func() {
		g.mu.Lock()
		if !g.closed && g.session == session {
			g.session = nil
			g.ready = make(chan struct{})
		}
		g.mu.Unlock()
		session.Close()
	}()
	for {
		stream, err := session.AcceptStream()
		if err != nil {
			return
		}
		go func(s *yamux.Stream) {
			conn := newYamuxConn(s, "guest")
			defer conn.Close()
			var hdr [2]byte
			if _, err := io.ReadFull(conn, hdr[:]); err != nil {
				return
			}
			port := int(binary.BigEndian.Uint16(hdr[:]))
			target, err := net.DialTimeout("tcp4",
				net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 2*time.Second)
			if err != nil {
				_, _ = conn.Write([]byte{1})
				return
			}
			defer target.Close()
			_, _ = conn.Write([]byte{0})
			var wg sync.WaitGroup
			wg.Add(2)
			go func() {
				defer wg.Done()
				_, _ = io.Copy(target, conn)
				if tc, ok := target.(*net.TCPConn); ok {
					_ = tc.CloseWrite()
				}
			}()
			go func() {
				defer wg.Done()
				_, _ = io.Copy(conn, target)
				_ = conn.CloseWrite()
			}()
			wg.Wait()
		}(stream)
	}
}

func (g *testGuestUplink) openStream(t *testing.T) *yamuxConn {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		g.mu.Lock()
		session := g.session
		wait := g.ready
		g.mu.Unlock()
		if session != nil && !session.IsClosed() {
			stream, err := session.OpenStream()
			if err == nil {
				return newYamuxConn(stream, "guest")
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for guest uplink session")
		}
		select {
		case <-wait:
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func TestBrokerUplinkConnectForwardAndRestore(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	rootDir, err := os.MkdirTemp("/tmp", "br-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(rootDir) })
	root, err := state.OpenRoot(rootDir)
	if err != nil {
		t.Fatal(err)
	}
	projectDir := t.TempDir()
	project, err := root.Project(projectDir)
	if err != nil {
		t.Fatal(err)
	}
	token, err := project.Token()
	if err != nil {
		t.Fatal(err)
	}
	if err := project.SaveProfile(&state.Profile{}); err != nil {
		t.Fatal(err)
	}

	guest := startTestGuestUplink(t)

	devListener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer devListener.Close()
	devPort := devListener.Addr().(*net.TCPAddr).Port
	go func() {
		for {
			conn, err := devListener.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				_, _ = io.WriteString(c, "hello-from-guest-port")
			}(conn)
		}
	}()

	startBroker := func() (context.CancelFunc, <-chan error, *control.Client) {
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() {
			done <- Run(ctx, Options{
				Root:    root,
				Version: "test",
				Logger:  log.New(io.Discard, "", 0),
			})
		}()
		client := control.NewClient(root.BrokerSocket())
		deadline := time.Now().Add(5 * time.Second)
		for !client.Alive(context.Background()) {
			if time.Now().After(deadline) {
				t.Fatal("broker did not start")
			}
			time.Sleep(25 * time.Millisecond)
		}
		return cancel, done, client
	}

	cancel1, done1, client := startBroker()
	boxName := "app-1234567890ab.prison"

	uplink, err := client.AttachUplink(context.Background(), boxName, guest.socketPath)
	if err != nil {
		t.Fatalf("AttachUplink: %v", err)
	}
	if !uplink.Connected {
		t.Fatalf("AttachUplink Connected = false (err=%q), want true", uplink.Error)
	}

	status, err := client.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(status.Uplinks) != 1 || status.Uplinks[0].Box != boxName || !status.Uplinks[0].Connected {
		t.Fatalf("Status.Uplinks = %+v, want connected %s", status.Uplinks, boxName)
	}

	fetchBrokerHosts := func() string {
		stream := guest.openStream(t)
		defer stream.Close()
		req := fmt.Sprintf("CONNECT %s:80 HTTP/1.1\r\nHost: %s:80\r\nProxy-Authorization: %s\r\n\r\n",
			protocol.BrokerHost, protocol.BrokerHost, protocol.ProxyAuthorization(token))
		if _, err := io.WriteString(stream, req); err != nil {
			t.Fatalf("write CONNECT: %v", err)
		}
		reader := bufio.NewReader(stream)
		line, err := reader.ReadString('\n')
		if err != nil || !strings.Contains(line, "200") {
			t.Fatalf("CONNECT response = %q, %v", line, err)
		}
		for {
			hdr, err := reader.ReadString('\n')
			if err != nil {
				t.Fatalf("read CONNECT header: %v", err)
			}
			if hdr == "\r\n" {
				break
			}
		}
		_, err = io.WriteString(stream, fmt.Sprintf(
			"GET /v1/hosts HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n",
			protocol.BrokerHost))
		if err != nil {
			t.Fatalf("write GET: %v", err)
		}
		resp, err := http.ReadResponse(reader, nil)
		if err != nil {
			t.Fatalf("ReadResponse: %v", err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET /v1/hosts = %d %s", resp.StatusCode, string(body))
		}
		return string(body)
	}

	if body := fetchBrokerHosts(); !strings.Contains(body, "github.com") {
		t.Fatalf("GET /v1/hosts body = %q", body)
	}

	probe, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	hostPort := probe.Addr().(*net.TCPAddr).Port
	probe.Close()

	if err := client.Publish(context.Background(), boxName, control.PublishRequest{
		Uplink: boxName,
		Ports:  []control.PortForward{{Host: hostPort, Guest: devPort}},
	}); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	fwdConn, err := net.DialTimeout("tcp4",
		net.JoinHostPort("127.0.0.1", strconv.Itoa(hostPort)), 2*time.Second)
	if err != nil {
		t.Fatalf("dial published port: %v", err)
	}
	got, err := io.ReadAll(fwdConn)
	fwdConn.Close()
	if err != nil || string(got) != "hello-from-guest-port" {
		t.Fatalf("published port returned %q, %v, want hello-from-guest-port", got, err)
	}

	cancel1()
	<-done1

	cancel2, done2, client2 := startBroker()
	defer func() {
		cancel2()
		<-done2
	}()

	deadline := time.Now().Add(5 * time.Second)
	for {
		st, err := client2.Status(context.Background())
		if err == nil && len(st.Uplinks) == 1 && st.Uplinks[0].Connected {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("restored uplink did not connect; status=%+v err=%v", st, err)
		}
		time.Sleep(50 * time.Millisecond)
	}

	if body := fetchBrokerHosts(); !strings.Contains(body, "github.com") {
		t.Fatalf("after restart GET /v1/hosts body = %q", body)
	}

	if err := client2.DetachUplink(context.Background(), boxName); err != nil {
		t.Fatalf("DetachUplink: %v", err)
	}
	st, err := client2.Status(context.Background())
	if err != nil {
		t.Fatalf("Status after detach: %v", err)
	}
	if len(st.Uplinks) != 0 {
		t.Fatalf("Status.Uplinks after detach = %+v, want empty", st.Uplinks)
	}
}
