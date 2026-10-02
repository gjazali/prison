//go:build linux

package nfsexport

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

type exportfsRunner func(ctx context.Context, arguments ...string) (string,
	error)

func runExportfs(ctx context.Context, arguments ...string) (string, error) {
	output, err := exec.CommandContext(ctx, "exportfs",
		arguments...).CombinedOutput()
	return strings.TrimSpace(string(output)), err
}

func ActivatedListener() (net.Listener, error) {
	if os.Getenv("LISTEN_PID") != strconv.Itoa(os.Getpid()) ||
		os.Getenv("LISTEN_FDS") != "1" {
		return nil, errors.New("the helper must start from its systemd " +
			"socket")
	}
	return net.FileListener(os.NewFile(3, "prison-nfs.sock"))
}

func Serve(listener net.Listener, policy Policy, logger *log.Logger) error {
	server := helperServer{policy: policy, exportfs: runExportfs,
		ownerOf: ownerOfPath, logger: logger}
	for {
		connection, err := listener.Accept()
		if err != nil {
			return err
		}
		go server.handle(connection)
	}
}

type helperServer struct {
	policy   Policy
	exportfs exportfsRunner
	ownerOf  func(path string) (int, bool, error)
	logger   *log.Logger
}

func (server helperServer) handle(connection net.Conn) {
	defer connection.Close()
	connection.SetDeadline(time.Now().Add(30 * time.Second))
	response := Response{}
	if err := server.checkPeer(connection); err != nil {
		response.Error = err.Error()
	} else {
		line, err := bufio.NewReader(connection).ReadBytes('\n')
		if err != nil {
			return
		}
		var request Request
		if err := json.Unmarshal(line, &request); err != nil {
			response.Error = "the request is not valid JSON"
		} else if err := server.serve(request); err != nil {
			response.Error = err.Error()
		}
	}
	if response.Error != "" {
		server.logger.Printf("refused: %s", response.Error)
	}
	answer, _ := json.Marshal(response)
	connection.Write(append(answer, '\n'))
}

func (server helperServer) checkPeer(connection net.Conn) error {
	unixConnection, isUnix := connection.(*net.UnixConn)
	if !isUnix {
		return errors.New("the connection is not a unix socket")
	}
	raw, err := unixConnection.SyscallConn()
	if err != nil {
		return err
	}
	var credentials *unix.Ucred
	var credentialError error
	raw.Control(func(descriptor uintptr) {
		credentials, credentialError = unix.GetsockoptUcred(int(descriptor),
			unix.SOL_SOCKET, unix.SO_PEERCRED)
	})
	if credentialError != nil {
		return credentialError
	}
	if int(credentials.Uid) != server.policy.UID && credentials.Uid != 0 {
		return fmt.Errorf("user %d cannot use this helper", credentials.Uid)
	}
	return nil
}

func (server helperServer) serve(request Request) error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	switch request.Operation {
	case OperationPing:
		return nil
	case OperationExport:
		target, err := server.policy.exportTarget(request, server.ownerOf)
		if err != nil {
			return err
		}
		output, err := server.exportfs(ctx, "-o",
			server.policy.exportOptions(request.ReadOnly), target)
		if err != nil {
			return fmt.Errorf("exportfs failed: %v: %s", err, output)
		}
		server.logger.Printf("exported %s", target)
		return nil
	case OperationUnexport:
		target, err := server.policy.unexportTarget(request)
		if err != nil {
			return err
		}
		output, err := server.exportfs(ctx, "-u", target)
		if err != nil && !strings.Contains(output, "Could not find") {
			return fmt.Errorf("exportfs failed: %v: %s", err, output)
		}
		server.logger.Printf("unexported %s", target)
		return nil
	}
	return fmt.Errorf("unknown operation %q", request.Operation)
}
