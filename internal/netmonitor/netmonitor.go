// Package netmonitor implements fastci guard's runtime network-egress
// guardrail: a minimal local forward proxy that a test/build run can be
// pointed at (via HTTP_PROXY/HTTPS_PROXY) so fastci can report which hosts
// it contacted afterward.
//
// It never decrypts HTTPS traffic. For a CONNECT request (how an
// HTTP_PROXY-aware client tunnels TLS through a proxy), Proxy only ever
// reads the plaintext CONNECT line itself - "CONNECT host:port HTTP/1.1" -
// to learn the destination, then splices the two raw TCP connections
// together unmodified; it never holds a TLS certificate/key that would let
// it terminate or inspect the encrypted bytes flowing through. Plain HTTP
// requests (rare in practice - virtually every package registry and API
// fastci-driven tooling talks to is HTTPS) are forwarded the same way an
// ordinary HTTP forward proxy does, which does mean their request line and
// headers pass through Proxy in the clear.
package netmonitor

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"sync"
)

// Proxy is a running local forward proxy. Create one with Start and stop
// it with Close.
type Proxy struct {
	ln net.Listener
	wg sync.WaitGroup

	mu     sync.Mutex
	hosts  map[string]struct{}
	conns  map[net.Conn]struct{}
	closed bool
}

// Start begins listening on an ephemeral localhost port and accepting
// connections in the background. Call Close when the monitored run is
// done.
func Start() (*Proxy, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("netmonitor: listen: %w", err)
	}
	p := &Proxy{
		ln:    ln,
		hosts: make(map[string]struct{}),
		conns: make(map[net.Conn]struct{}),
	}
	p.wg.Add(1)
	go p.acceptLoop()
	return p, nil
}

// Addr returns the "host:port" a client should be pointed at, e.g. via
// the HTTP_PROXY/HTTPS_PROXY environment variables.
func (p *Proxy) Addr() string {
	return p.ln.Addr().String()
}

// Hosts returns every distinct "host:port" a client tunneled or forwarded
// a request to, sorted, deduplicated.
func (p *Proxy) Hosts() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]string, 0, len(p.hosts))
	for h := range p.hosts {
		out = append(out, h)
	}
	sort.Strings(out)
	return out
}

// Close stops accepting new connections, closes every connection currently
// being proxied (the monitored process has already exited by the time
// Close is normally called, so anything still open at that point is a
// leftover, not live traffic worth waiting on), and waits for their
// handling goroutines to finish.
func (p *Proxy) Close() error {
	p.mu.Lock()
	p.closed = true
	for c := range p.conns {
		c.Close()
	}
	p.mu.Unlock()

	err := p.ln.Close()
	p.wg.Wait()
	return err
}

func (p *Proxy) acceptLoop() {
	defer p.wg.Done()
	for {
		conn, err := p.ln.Accept()
		if err != nil {
			return
		}
		if !p.track(conn) {
			conn.Close()
			continue
		}
		p.wg.Add(1)
		go func() {
			defer p.wg.Done()
			defer p.untrack(conn)
			p.handle(conn)
		}()
	}
}

func (p *Proxy) track(c net.Conn) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return false
	}
	p.conns[c] = struct{}{}
	return true
}

func (p *Proxy) untrack(c net.Conn) {
	p.mu.Lock()
	delete(p.conns, c)
	p.mu.Unlock()
	c.Close()
}

func (p *Proxy) recordHost(hostport string) {
	p.mu.Lock()
	p.hosts[hostport] = struct{}{}
	p.mu.Unlock()
}

func (p *Proxy) handle(client net.Conn) {
	br := bufio.NewReader(client)
	req, err := http.ReadRequest(br)
	if err != nil {
		return
	}

	target := req.Host
	if target == "" {
		target = req.URL.Host
	}
	if _, _, err := net.SplitHostPort(target); err != nil {
		// No explicit port in the request (e.g. a plain "GET
		// http://host/path" with no port) - default to the scheme's
		// usual port, same as a normal HTTP client would.
		port := "80"
		if req.Method == http.MethodConnect {
			port = "443"
		}
		target = net.JoinHostPort(target, port)
	}

	upstream, err := net.Dial("tcp", target)
	if err != nil {
		if req.Method == http.MethodConnect {
			fmt.Fprint(client, "HTTP/1.1 502 Bad Gateway\r\n\r\n")
		}
		return
	}
	defer upstream.Close()

	p.recordHost(target)

	if req.Method == http.MethodConnect {
		if _, err := fmt.Fprint(client, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
			return
		}
		splice(client, upstream, br)
		return
	}

	// A plain (non-TLS) forward-proxy request: re-serialize it onto the
	// upstream connection - it was already fully parsed off of br above,
	// so nothing is lost even though br itself is discarded here - then
	// read back exactly one well-framed response and re-serialize that to
	// the client. This can't be a raw io.Copy until EOF: a keep-alive
	// connection (the common case) never closes on its own, so copying
	// until EOF would just hang forever waiting for more bytes that are
	// never coming; parsing the response's actual Content-Length/chunked
	// framing is what lets this return once that one response is done.
	if err := req.Write(upstream); err != nil {
		return
	}
	resp, err := http.ReadResponse(bufio.NewReader(upstream), req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	resp.Write(client)
}

// splice copies bytes in both directions between client and upstream,
// returning once either side is done. buffered is whatever http.ReadRequest
// already consumed past the CONNECT request's terminating CRLF (normally
// nothing, but a pipelining client could have sent the start of its tunneled
// traffic in the same TCP segment as the CONNECT request) - it has to be
// drained to upstream before any more of the raw client socket is read, or
// that leading slice of the tunneled traffic would be silently dropped.
func splice(client net.Conn, upstream net.Conn, buffered *bufio.Reader) {
	if n := buffered.Buffered(); n > 0 {
		if _, err := io.CopyN(upstream, buffered, int64(n)); err != nil {
			return
		}
	}

	done := make(chan struct{}, 2)
	go func() {
		io.Copy(upstream, client)
		done <- struct{}{}
	}()
	go func() {
		io.Copy(client, upstream)
		done <- struct{}{}
	}()
	<-done
}
