package relay

import (
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

const spliceBufferBytes = 32 * 1024

type tunnelActivity struct {
	lastUnixNano atomic.Int64
}

func (activity *tunnelActivity) touch() {
	activity.lastUnixNano.Store(time.Now().UnixNano())
}

func (activity *tunnelActivity) idleFor() time.Duration {
	return time.Since(time.Unix(0, activity.lastUnixNano.Load()))
}

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

// copyWithIdleTimeout treats the tunnel as idle only when neither
// direction moves bytes.
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

func isTimeout(err error) bool {
	var networkError net.Error
	return errors.As(err, &networkError) && networkError.Timeout()
}
