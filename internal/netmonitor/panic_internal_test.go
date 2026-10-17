package netmonitor

import (
	"bufio"
	"net"
	"strings"
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

// TestSpliceRecoversFromPanicInEitherDirection is a regression test for a
// gap in handleSafely's own recover: splice spawns its own two goroutines
// (one per copy direction), each outside the call stack handleSafely's
// defer covers, so a panic in either needs its own recover too. client's
// Read panics directly (panicConn), and its Write panics via its
// embedded, never-overridden net.Conn being nil - both copy directions
// panic here, not just one.
func TestSpliceRecoversFromPanicInEitherDirection(t *testing.T) {
	upstreamA, upstreamB := net.Pipe()
	defer upstreamB.Close()

	// If either of splice's own goroutines didn't recover, this call
	// would panic and crash the whole test binary, not just fail this
	// test - returning at all (rather than hanging or crashing) is the
	// real assertion.
	splice(panicConn{}, upstreamA, bufio.NewReader(strings.NewReader("")))
}
