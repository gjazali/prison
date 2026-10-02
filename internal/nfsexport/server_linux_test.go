//go:build linux

package nfsexport

import (
	"context"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestServerExportsForTheUser(t *testing.T) {
	folder := t.TempDir()
	socket := filepath.Join(t.TempDir(), "helper.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	policy := testPolicy()
	policy.UID = os.Getuid()
	var calls []string
	server := helperServer{
		policy: policy,
		exportfs: func(ctx context.Context, arguments ...string) (string,
			error) {
			calls = append(calls, strings.Join(arguments, " "))
			return "", nil
		},
		ownerOf: ownerOfPath,
		logger:  log.New(io.Discard, "", 0),
	}
	go func() {
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			server.handle(connection)
		}
	}()
	ctx := context.Background()
	err = Ask(ctx, socket, Request{Operation: OperationExport, Path: folder,
		Client: "192.168.150.10", ReadOnly: true})
	if err != nil {
		t.Fatalf("Ask(export) = %v, want nil", err)
	}
	err = Ask(ctx, socket, Request{Operation: OperationExport, Path: "/etc",
		Client: "192.168.150.10"})
	if err == nil || !strings.Contains(err.Error(), "does not belong") {
		t.Errorf("Ask(export /etc) = %v, want \"does not belong\"", err)
	}
	if len(calls) != 1 || !strings.HasPrefix(calls[0], "-o ro,") ||
		!strings.HasSuffix(calls[0], "192.168.150.10:"+folder) {
		t.Errorf("exportfs calls = %q, want one read-only export", calls)
	}
}
