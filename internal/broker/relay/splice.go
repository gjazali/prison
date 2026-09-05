package relay

import (
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// spliceBufferBytes is the copy buffer size per direction.
const spliceBufferBytes = 32 * 1024

// tunnelActivity tracks the last time bytes moved in either direction
// of a splice.
type tunnelActivity struct {
	lastUnixNano atomic.Int64
}

// touch records activity at the current time.
func (activity *tunnelActivity) touch() {
	activity.lastUnixNano.Store(time.Now().UnixNano())
}

// idleFor returns the duration since the last activity.
func (activity *tunnelActivity) idleFor() time.Duration {
	return time.Since(time.Unix(0, activity.lastUnixNano.Load()))
}

// Splice copies bytes in both directions between client and upstream
// until one side closes or the idle timeout expires. Returns the total
// bytes moved. Both connections are closed before returning.
func Splice(client, upstream net.Conn, idleTimeout time.Duration) int64 {
	activity := &tunnelActivity{}
	activity.touch()
	var moved atomic.Int64
	var waitGroup sync.WaitGroup
	copyDirection := func(destination, source net.Conn) {
		defer waitGroup.Done()
		count, clean := copyWithIdleTimeout(destination, source, idleTimeout,
			activity)
		moved.Add(count)
		if clean {
			closeWrite(destination)
			return
		}
		client.Close()
		upstream.Close()
	}
	waitGroup.Add(2)
	go copyDirection(upstream, client)
	go copyDirection(client, upstream)
	waitGroup.Wait()
	client.Close()
	upstream.Close()
	return moved.Load()
}

// copyWithIdleTimeout copies from source to destination until EOF, an
// error, or the idle timeout. Returns bytes copied and whether it
// ended cleanly at EOF.
func copyWithIdleTimeout(destination io.Writer, source net.Conn,
	idleTimeout time.Duration, activity *tunnelActivity) (int64, bool) {
	buffer := make([]byte, spliceBufferBytes)
	var copied int64
	for {
		source.SetReadDeadline(time.Now().Add(idleTimeout))
		count, err := source.Read(buffer)
		if count > 0 {
			activity.touch()
			if _, writeErr := destination.Write(buffer[:count]); writeErr != nil {
				return copied, false
			}
			copied += int64(count)
		}
		if err == nil {
			continue
		}
		if errors.Is(err, io.EOF) {
			return copied, true
		}
		if isTimeout(err) && activity.idleFor() < idleTimeout {
			continue
		}
		return copied, false
	}
}

// isTimeout returns true if err is a network timeout.
func isTimeout(err error) bool {
	var networkError net.Error
	return errors.As(err, &networkError) && networkError.Timeout()
}
