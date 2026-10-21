package session

import (
	"bytes"
	"testing"
)

func TestCappedBufferDropsExtraOutput(t *testing.T) {
	buffer := &cappedBuffer{}
	chunk := bytes.Repeat([]byte("x"), guestOutputLimit-1)
	for range 2 {
		count, err := buffer.Write(chunk)
		if count != len(chunk) || err != nil {
			t.Fatalf("Write = %d, %v, want %d, nil", count, err, len(chunk))
		}
	}
	if buffer.Len() != guestOutputLimit {
		t.Errorf("Len = %d, want %d", buffer.Len(), guestOutputLimit)
	}
}
