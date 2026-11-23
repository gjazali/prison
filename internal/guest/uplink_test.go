package guest

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hashicorp/yamux"
)

func TestUplinkTransportBidirectionalAndReconnect(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "pu-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	socketPath := filepath.Join(dir, "u.sock")
	transport, err := startUplinkTransport(socketPath, nil)
	if err != nil {
		t.Fatalf("startUplinkTransport: %v", err)
	}
	defer transport.Close()

	info, err := os.Stat(socketPath)
	if err != nil {
		t.Fatalf("stat socket: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("socket mode = %o, want 600", mode)
	}

	echoAddr := startEchoServer(t)
	_, portText, _ := net.SplitHostPort(echoAddr)
	echoListenerPort := 0
	for _, c := range portText {
		echoListenerPort = echoListenerPort*10 + int(c-'0')
	}

	dialServer := func() *yamux.Session {
		raw, err := net.DialTimeout("unix", socketPath, 2*time.Second)
		if err != nil {
			t.Fatalf("dial unix: %v", err)
		}
		cfg := yamux.DefaultConfig()
		cfg.LogOutput = io.Discard
		session, err := yamux.Server(raw, cfg)
		if err != nil {
			raw.Close()
			t.Fatalf("yamux.Server: %v", err)
		}
		return session
	}

	hostSession := dialServer()
	defer hostSession.Close()

	go func() {
		stream, err := hostSession.AcceptStream()
		if err != nil {
			return
		}
		conn := newUplinkStream(stream)
		defer conn.Close()
		data, _ := io.ReadAll(conn)
		_, _ = conn.Write(append([]byte("host:"), data...))
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	guestStream, err := transport.OpenStream(ctx)
	if err != nil {
		t.Fatalf("OpenStream: %v", err)
	}
	if _, err := guestStream.Write([]byte("hello")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := halfClose(guestStream); err != nil {
		t.Fatalf("halfClose: %v", err)
	}
	reply, err := io.ReadAll(guestStream)
	guestStream.Close()
	if err != nil || string(reply) != "host:hello" {
		t.Fatalf("guest->host reply = %q, %v, want host:hello", reply, err)
	}

	fwdStream, err := hostSession.OpenStream()
	if err != nil {
		t.Fatalf("host OpenStream: %v", err)
	}
	fwdConn := newUplinkStream(fwdStream)
	var header [2]byte
	binary.BigEndian.PutUint16(header[:], uint16(echoListenerPort))
	if _, err := fwdConn.Write(header[:]); err != nil {
		t.Fatalf("write port header: %v", err)
	}
	var status [1]byte
	if _, err := io.ReadFull(fwdConn, status[:]); err != nil {
		t.Fatalf("read forward status: %v", err)
	}
	if status[0] != 0 {
		t.Fatalf("forward status = %d, want 0", status[0])
	}
	if _, err := fwdConn.Write([]byte("devserver")); err != nil {
		t.Fatalf("write forward body: %v", err)
	}
	_ = fwdConn.CloseWrite()
	fwdReply, err := io.ReadAll(fwdConn)
	fwdConn.Close()
	if err != nil || string(fwdReply) != "echo:devserver" {
		t.Fatalf("host->guest forward reply = %q, %v, want echo:devserver", fwdReply, err)
	}

	hostSession.Close()
	hostSession2 := dialServer()
	defer hostSession2.Close()

	go func() {
		stream, err := hostSession2.AcceptStream()
		if err != nil {
			return
		}
		conn := newUplinkStream(stream)
		defer conn.Close()
		data, _ := io.ReadAll(conn)
		_, _ = conn.Write(append([]byte("reconnected:"), data...))
	}()

	deadline := time.Now().Add(3 * time.Second)
	for {
		stream2, err := transport.OpenStream(ctx)
		if err != nil {
			if time.Now().After(deadline) {
				t.Fatalf("OpenStream after reconnect: %v", err)
			}
			time.Sleep(20 * time.Millisecond)
			continue
		}
		_, _ = stream2.Write([]byte("ok"))
		_ = halfClose(stream2)
		reply2, err := io.ReadAll(stream2)
		stream2.Close()
		if err == nil && string(reply2) == "reconnected:ok" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("reconnect reply = %q, %v, want reconnected:ok", reply2, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
