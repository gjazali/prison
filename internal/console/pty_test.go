//go:build darwin || linux

package console

import (
	"io"
	"os"
	"sync/atomic"
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

func TestOpenOutputLendsATerminal(t *testing.T) {
	held, err := OpenOutput()
	if err != nil {
		t.Fatalf("OpenOutput: %v", err)
	}
	defer held.Close()
	if !term.IsTerminal(int(held.Stdin().Fd())) {
		t.Errorf("IsTerminal(%s) = false, want true", held.Stdin().Name())
	}
	if err := held.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestResizeUpdatesLentTerminalAndNotifies(t *testing.T) {
	var rows, columns atomic.Int32
	rows.Store(24)
	columns.Store(80)
	held, err := openLentWithSize(func() (int, int, bool) {
		return int(rows.Load()), int(columns.Load()), true
	})
	if err != nil {
		t.Fatalf("openLentWithSize: %v", err)
	}
	defer held.Close()

	// Simulate a child process (like Apple `container`) putting its inherited
	// stdin into non-blocking mode, and verify resizing preserves O_NONBLOCK.
	raw, err := held.Stdin().SyscallConn()
	if err != nil {
		t.Fatalf("SyscallConn: %v", err)
	}
	var nonblockErr error
	if err := raw.Control(func(fd uintptr) {
		nonblockErr = unix.SetNonblock(int(fd), true)
	}); err != nil || nonblockErr != nil {
		t.Fatalf("SetNonblock: %v / %v", err, nonblockErr)
	}

	for _, want := range []struct{ rows, columns int }{
		{42, 132},
		{55, 180},
	} {
		rows.Store(int32(want.rows))
		columns.Store(int32(want.columns))
		if err := unix.Kill(os.Getpid(), unix.SIGWINCH); err != nil {
			t.Fatalf("sending SIGWINCH: %v", err)
		}
		select {
		case <-held.Resizes():
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for resize %dx%d",
				want.rows, want.columns)
		}
		var (
			size     *unix.Winsize
			flags    int
			sizeErr  error
			flagsErr error
		)
		if err := raw.Control(func(fd uintptr) {
			size, sizeErr = unix.IoctlGetWinsize(int(fd), unix.TIOCGWINSZ)
			flags, flagsErr = unix.FcntlInt(fd, unix.F_GETFL, 0)
		}); err != nil {
			t.Fatalf("Control: %v", err)
		}
		if sizeErr != nil {
			t.Fatalf("reading lent terminal size: %v", sizeErr)
		}
		if int(size.Row) != want.rows || int(size.Col) != want.columns {
			t.Errorf("size = %dx%d, want %dx%d",
				size.Row, size.Col, want.rows, want.columns)
		}
		if flagsErr != nil {
			t.Fatalf("reading lent terminal flags: %v", flagsErr)
		}
		if flags&unix.O_NONBLOCK == 0 {
			t.Errorf("O_NONBLOCK was cleared on lent terminal after resize")
		}
	}
}

func TestCloseStopsResizing(t *testing.T) {
	held, err := openLentWithSize(func() (int, int, bool) {
		return 30, 100, true
	})
	if err != nil {
		t.Fatalf("openLentWithSize: %v", err)
	}
	if err := held.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := unix.Kill(os.Getpid(), unix.SIGWINCH); err != nil {
		t.Fatalf("sending SIGWINCH: %v", err)
	}
	select {
	case <-held.Resizes():
		t.Error("received resize notification after Close")
	case <-time.After(50 * time.Millisecond):
	}
}
