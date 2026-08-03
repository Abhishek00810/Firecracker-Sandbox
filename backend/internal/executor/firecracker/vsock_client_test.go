package firecracker

import (
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

type deadlineTrackingConn struct {
	mu       sync.Mutex
	deadline time.Time
}

func (c *deadlineTrackingConn) Read([]byte) (int, error)         { return 0, io.EOF }
func (c *deadlineTrackingConn) Write(data []byte) (int, error)   { return len(data), nil }
func (c *deadlineTrackingConn) Close() error                     { return nil }
func (c *deadlineTrackingConn) LocalAddr() net.Addr              { return nil }
func (c *deadlineTrackingConn) RemoteAddr() net.Addr             { return nil }
func (c *deadlineTrackingConn) SetReadDeadline(time.Time) error  { return nil }
func (c *deadlineTrackingConn) SetWriteDeadline(time.Time) error { return nil }

func (c *deadlineTrackingConn) SetDeadline(deadline time.Time) error {
	c.mu.Lock()
	c.deadline = deadline
	c.mu.Unlock()
	return nil
}

func (c *deadlineTrackingConn) currentDeadline() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.deadline
}

func TestResetRuntimesClearsDeadlineAfterReadFailure(t *testing.T) {
	conn := &deadlineTrackingConn{}

	err := NewVsockClient("unused").ResetRuntimesOnConn(conn)
	if err == nil {
		t.Fatal("expected reset_runtimes read failure")
	}
	if deadline := conn.currentDeadline(); !deadline.IsZero() {
		t.Fatalf("connection deadline was not cleared: %v", deadline)
	}
}
