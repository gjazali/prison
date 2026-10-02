package machine

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
)

func TestConfigRoundTrip(t *testing.T) {
	config := Config{
		Hostname:    "prison-demo",
		Environment: []string{"PATH=/usr/bin", "PRISON_TOKEN=secret"},
		Command:     []string{"sleep", "infinity"},
	}
	disk, err := EncodeConfig(config)
	if err != nil {
		t.Fatalf("EncodeConfig = %v, want nil", err)
	}
	if len(disk)%configBlockSize != 0 {
		t.Errorf("disk size = %d, want a multiple of %d", len(disk),
			configBlockSize)
	}
	decoded, err := DecodeConfig(disk)
	if err != nil {
		t.Fatalf("DecodeConfig = %v, want nil", err)
	}
	if !reflect.DeepEqual(decoded, config) {
		t.Errorf("DecodeConfig = %+v, want %+v", decoded, config)
	}
}

func TestDecodeConfigRefusesOtherDisks(t *testing.T) {
	_, err := DecodeConfig(make([]byte, configBlockSize))
	if err == nil || !strings.Contains(err.Error(), "no prison header") {
		t.Errorf("DecodeConfig = %v, want a header error", err)
	}
}

func TestFramesRoundTrip(t *testing.T) {
	var stream bytes.Buffer
	frames := []struct {
		kind    byte
		payload []byte
	}{
		{FrameStdout, []byte("hello")},
		{FrameResize, ResizePayload(40, 120)},
		{FrameStdinClose, nil},
		{FrameExit, ExitPayload(-1)},
	}
	for _, frame := range frames {
		if err := WriteFrame(&stream, frame.kind, frame.payload); err != nil {
			t.Fatal(err)
		}
	}
	for _, want := range frames {
		kind, payload, err := ReadFrame(&stream)
		if err != nil || kind != want.kind ||
			!bytes.Equal(payload, want.payload) {
			t.Errorf("ReadFrame = %d, %q, %v, want %d, %q", kind, payload,
				err, want.kind, want.payload)
		}
	}
	rows, columns, valid := ParseResize(ResizePayload(40, 120))
	if !valid || rows != 40 || columns != 120 {
		t.Errorf("ParseResize = %d, %d, %v, want 40, 120, true", rows,
			columns, valid)
	}
	status, valid := ParseExit(ExitPayload(-1))
	if !valid || status != -1 {
		t.Errorf("ParseExit = %d, %v, want -1", status, valid)
	}
}

func TestReadFrameRefusesLargeFrames(t *testing.T) {
	header := []byte{FrameStdout, 0xff, 0xff, 0xff, 0xff}
	if _, _, err := ReadFrame(bytes.NewReader(header)); err == nil {
		t.Error("ReadFrame = nil error, want error")
	}
}
