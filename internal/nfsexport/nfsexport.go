// Package nfsexport lets an unprivileged prison export folders to boxes.
// A root helper owns the exports and makes sure that each request is
// allowed.
package nfsexport

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
)

const (
	OperationExport   = "export"
	OperationUnexport = "unexport"
	OperationPing     = "ping"
)

type Request struct {
	Operation string `json:"operation"`
	Path      string `json:"path,omitempty"`
	Client    string `json:"client,omitempty"`
	ReadOnly  bool   `json:"read_only,omitempty"`
}

type Response struct {
	Error string `json:"error,omitempty"`
}

// SocketPath is the helper socket for one user. Only that user can open it.
func SocketPath(uid int) string {
	return "/run/prison/nfs-" + strconv.Itoa(uid) + ".sock"
}

func Ask(ctx context.Context, socket string, request Request) error {
	var dialer net.Dialer
	connection, err := dialer.DialContext(ctx, "unix", socket)
	if err != nil {
		return fmt.Errorf("cannot reach the NFS helper: %w", err)
	}
	defer connection.Close()
	if deadline, found := ctx.Deadline(); found {
		connection.SetDeadline(deadline)
	}
	line, _ := json.Marshal(request)
	if _, err := connection.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("cannot reach the NFS helper: %w", err)
	}
	answer, err := bufio.NewReader(connection).ReadBytes('\n')
	if err != nil {
		return fmt.Errorf("the NFS helper did not answer: %w", err)
	}
	var response Response
	if err := json.Unmarshal(answer, &response); err != nil {
		return fmt.Errorf("the NFS helper answer is not valid: %w", err)
	}
	if response.Error != "" {
		return errors.New(response.Error)
	}
	return nil
}

type Policy struct {
	UID         int
	GID         int
	AllowClient func(netip.Addr) bool
}

// exportTarget makes sure that an export request is allowed. It returns the
// resolved path because the export must not follow a symlink that changes
// later.
func (policy Policy) exportTarget(request Request,
	ownerOf func(path string) (int, bool, error)) (string, error) {
	client, err := policy.client(request.Client)
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(request.Path) {
		return "", fmt.Errorf("export path %q is not absolute", request.Path)
	}
	resolved, err := filepath.EvalSymlinks(request.Path)
	if err != nil {
		return "", fmt.Errorf("cannot resolve %s: %w", request.Path, err)
	}
	if resolved == "/" {
		return "", errors.New("cannot export the root folder")
	}
	owner, isDirectory, err := ownerOf(resolved)
	if err != nil {
		return "", err
	}
	if !isDirectory {
		return "", fmt.Errorf("%s is not a folder", resolved)
	}
	if owner != policy.UID {
		return "", fmt.Errorf("%s does not belong to user %d", resolved,
			policy.UID)
	}
	return client.String() + ":" + resolved, nil
}

func (policy Policy) unexportTarget(request Request) (string, error) {
	client, err := policy.client(request.Client)
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(request.Path) {
		return "", fmt.Errorf("export path %q is not absolute", request.Path)
	}
	resolved, err := filepath.EvalSymlinks(request.Path)
	if err != nil {
		resolved = filepath.Clean(request.Path)
	}
	return client.String() + ":" + resolved, nil
}

func (policy Policy) client(text string) (netip.Addr, error) {
	client, err := netip.ParseAddr(text)
	if err != nil || !policy.AllowClient(client) {
		return netip.Addr{}, fmt.Errorf("%q is not a box address", text)
	}
	return client, nil
}

// exportOptions uses `all_squash` so that a box cannot create a file that
// root owns on the host.
func (policy Policy) exportOptions(readOnly bool) string {
	mode := "rw"
	if readOnly {
		mode = "ro"
	}
	return fmt.Sprintf("%s,sync,no_subtree_check,all_squash,anonuid=%d,"+
		"anongid=%d", mode, policy.UID, policy.GID)
}

func ownerOfPath(path string) (int, bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, false, fmt.Errorf("cannot read %s: %w", path, err)
	}
	owner, found := fileOwner(info)
	if !found {
		return 0, false, fmt.Errorf("cannot read the owner of %s", path)
	}
	return owner, info.IsDir(), nil
}
