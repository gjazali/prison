// Package machine defines the configuration disk and the agent protocol that
// the aws-firecracker isolator and `prison-guest` share.
package machine

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

const (
	// AgentPort is the vsock port of `prison-guest`.
	AgentPort = 1024
	// ConfigDevice is the second drive of the microVM.
	ConfigDevice = "/dev/vdb"

	configMagic     = "prison-machine-config-1\n"
	configBlockSize = 4096
)

type Config struct {
	Hostname    string   `json:"hostname"`
	Environment []string `json:"environment"`
	Command     []string `json:"command"`
	NFSServer   string   `json:"nfs_server,omitempty"`
	Mounts      []Mount  `json:"mounts,omitempty"`
}

// Mount is an NFS export of the host. `Source` is the path on the host.
type Mount struct {
	Source   string `json:"source"`
	Target   string `json:"target"`
	ReadOnly bool   `json:"read_only,omitempty"`
}

// NFSPort is the port of the NFS server on the gateway.
const NFSPort = 2049

// EncodeConfig returns a raw disk image. The JSON document ends at the first
// NUL byte.
func EncodeConfig(config Config) ([]byte, error) {
	document, err := json.Marshal(config)
	if err != nil {
		return nil, err
	}
	content := append([]byte(configMagic), document...)
	size := (len(content)/configBlockSize + 1) * configBlockSize
	disk := make([]byte, size)
	copy(disk, content)
	return disk, nil
}

func DecodeConfig(disk []byte) (Config, error) {
	var config Config
	content, found := bytes.CutPrefix(disk, []byte(configMagic))
	if !found {
		return config, errors.New("the configuration disk has no prison header")
	}
	if end := bytes.IndexByte(content, 0); end >= 0 {
		content = content[:end]
	}
	if err := json.Unmarshal(content, &config); err != nil {
		return config, fmt.Errorf("the configuration disk is not valid: %w",
			err)
	}
	return config, nil
}

const (
	OperationPing     = "ping"
	OperationShutdown = "shutdown"
)

// Request is one line of JSON from the host. The agent replies with one
// line of JSON `Response`.
type Request struct {
	Operation string       `json:"operation"`
	Exec      *ExecRequest `json:"exec,omitempty"`
}

type Response struct {
	Error string `json:"error,omitempty"`
}

const OperationExec = "exec"

// ExecRequest follows an `exec` request line. After it, both sides send
// frames.
type ExecRequest struct {
	Command     []string `json:"command"`
	TTY         bool     `json:"tty"`
	WorkDir     string   `json:"workdir"`
	UID         int      `json:"uid"`
	GID         int      `json:"gid"`
	Environment []string `json:"environment"`
	Rows        int      `json:"rows"`
	Columns     int      `json:"columns"`
}

// Frame types. The host sends stdin, stdin close, and resize frames. The
// guest sends stdout, stderr, exit, and failure frames. An exit frame
// carries the status as 4 bytes. A failure frame carries a message and
// means that the command did not start.
const (
	FrameStdin byte = iota + 1
	FrameStdinClose
	FrameResize
	FrameStdout
	FrameStderr
	FrameExit
	FrameFailure
)

const maximumFramePayload = 1 << 20

// WriteFrame writes a type byte, a 4-byte big-endian length, and the
// payload.
func WriteFrame(w io.Writer, kind byte, payload []byte) error {
	header := make([]byte, 5, 5+len(payload))
	header[0] = kind
	binary.BigEndian.PutUint32(header[1:], uint32(len(payload)))
	_, err := w.Write(append(header, payload...))
	return err
}

func ReadFrame(r io.Reader) (byte, []byte, error) {
	var header [5]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return 0, nil, err
	}
	size := binary.BigEndian.Uint32(header[1:])
	if size > maximumFramePayload {
		return 0, nil, fmt.Errorf("frame of %d bytes is too large", size)
	}
	payload := make([]byte, size)
	if _, err := io.ReadFull(r, payload); err != nil {
		return 0, nil, err
	}
	return header[0], payload, nil
}

func ResizePayload(rows, columns int) []byte {
	payload := make([]byte, 4)
	binary.BigEndian.PutUint16(payload, uint16(rows))
	binary.BigEndian.PutUint16(payload[2:], uint16(columns))
	return payload
}

func ParseResize(payload []byte) (rows, columns int, valid bool) {
	if len(payload) != 4 {
		return 0, 0, false
	}
	return int(binary.BigEndian.Uint16(payload)),
		int(binary.BigEndian.Uint16(payload[2:])), true
}

func ExitPayload(status int) []byte {
	payload := make([]byte, 4)
	binary.BigEndian.PutUint32(payload, uint32(int32(status)))
	return payload
}

func ParseExit(payload []byte) (int, bool) {
	if len(payload) != 4 {
		return 0, false
	}
	return int(int32(binary.BigEndian.Uint32(payload))), true
}
