package netmonitor

import (
	"net"
	"testing"
	"time"
)

// panicConn is a net.Conn whose Read always panics - standing in for
// whatever unexpected state (a malformed CONNECT/HTTP request, an
// unusual net.Conn implementation) might someday make handle panic for
// real; the point of this test isn't that this exact panic is reachable
// today, it's that whatever one is must never escape handleSafely.
type panicConn struct{ net.Conn }

func (panicConn) Read([]byte) (int, error)    { panic("boom: simulated panic reading a connection") }
func (panicConn) Close() error                { return nil }
func (panicConn) SetDeadline(time.Time) error { return nil }

func TestHandleSafelyRecoversFromPanic(t *testing.T) {
	p := &Proxy{conns: map[net.Conn]struct{}{}}

	// If handleSafely didn't recover, this call would panic and crash
	// the whole test binary, not just fail this test - reaching the
	// line after it is the real assertion.
	p.handleSafely(panicConn{})
}
