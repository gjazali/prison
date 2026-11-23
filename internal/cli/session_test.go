package cli

import (
	"errors"
	"io"
	"os"
	"testing"

	"prison/internal/ui"
)

func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	original := os.Stderr
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stderr = writer
	defer func() {
		os.Stderr = original
	}()

	fn()

	if err := writer.Close(); err != nil {
		t.Fatalf("close stderr writer: %v", err)
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read stderr: %v", err)
	}
	_ = reader.Close()
	return string(data)
}

func TestReportInmateSessionExitPrintsNoticeOnCleanAndStatusExits(t *testing.T) {
	want := "==> " + inmateExitNotice + "\n"
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{name: "zero exit", err: nil, want: want},
		{name: "non-zero exit status", err: &ui.ExitError{Status: 130}, want: want},
		{name: "execution error", err: errors.New("connection lost"), want: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := captureStderr(t, func() {
				reportInmateSessionExit(tc.err)
			})
			if got != tc.want {
				t.Errorf("stderr = %q, want %q", got, tc.want)
			}
		})
	}
}
