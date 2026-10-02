//go:build darwin || linux

package console

import (
	"io"
	"os"
	"testing"
	"time"

	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

func openPair(t *testing.T) (*os.File, *os.File) {
	t.Helper()
	multiplexer, device, err := OpenTerminalPair()
	if err != nil {
		t.Fatalf("OpenTerminalPair: %v", err)
	}
	t.Cleanup(func() {
		device.Close()
		multiplexer.Close()
	})
	return multiplexer, device
}

func TestOpenTerminalPairLendsATerminal(t *testing.T) {
	_, device := openPair(t)
	if !term.IsTerminal(int(device.Fd())) {
		t.Errorf("IsTerminal(%s) = false, want true", device.Name())
	}
}

func TestOpenTerminalPairCarriesKeystrokes(t *testing.T) {
	multiplexer, device := openPair(t)
	if _, err := term.MakeRaw(int(device.Fd())); err != nil {
		t.Fatalf("MakeRaw: %v", err)
	}
	typed := "prison\r"
	if _, err := multiplexer.WriteString(typed); err != nil {
		t.Fatalf("writing to the multiplexer: %v", err)
	}
	// The deadline stops a broken pair from hanging the test.
	_ = device.SetReadDeadline(time.Now().Add(5 * time.Second))
	received := make([]byte, len(typed))
	if _, err := io.ReadFull(device, received); err != nil {
		t.Fatalf("reading from the device: %v", err)
	}
	if string(received) != typed {
		t.Errorf("read %q, want %q", received, typed)
	}
}

func TestSetSizeReachesTheTerminal(t *testing.T) {
	const wantRows, wantColumns = 37, 133
	_, device := openPair(t)
	if err := SetSize(int(device.Fd()), wantRows, wantColumns); err != nil {
		t.Fatalf("SetSize: %v", err)
	}
	size, err := unix.IoctlGetWinsize(int(device.Fd()), unix.TIOCGWINSZ)
	if err != nil {
		t.Fatalf("reading the size back: %v", err)
	}
	if int(size.Row) != wantRows || int(size.Col) != wantColumns {
		t.Errorf("size = %dx%d, want %dx%d",
			size.Row, size.Col, wantRows, wantColumns)
	}
}

func TestCloseIsIdempotent(t *testing.T) {
	multiplexer, device, err := OpenTerminalPair()
	if err != nil {
		t.Fatalf("OpenTerminalPair: %v", err)
	}
	console := &Console{multiplexer: multiplexer, lentTerminal: device}
	if err := console.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := console.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}
