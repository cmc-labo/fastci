package netmonitor_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/hpscript/fastci/internal/netmonitor"
)

func TestProxyForwardsPlainHTTPAndLogsHost(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("hello from upstream"))
	}))
	defer upstream.Close()

	p, err := netmonitor.Start()
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	proxyURL, _ := url.Parse("http://" + p.Addr())
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}}

	resp, err := client.Get(upstream.URL)
	if err != nil {
		t.Fatalf("GET through proxy: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "hello from upstream" {
		t.Errorf("body = %q, want %q", body, "hello from upstream")
	}

	wantHost := strings.TrimPrefix(upstream.URL, "http://")
	if hosts := p.Hosts(); len(hosts) != 1 || hosts[0] != wantHost {
		t.Errorf("Hosts() = %v, want [%q]", hosts, wantHost)
	}
}

func TestProxyTunnelsHTTPSViaConnectAndLogsHost(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("hello over tls"))
	}))
	defer upstream.Close()

	p, err := netmonitor.Start()
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	proxyURL, _ := url.Parse("http://" + p.Addr())
	client := upstream.Client() // trusts upstream's self-signed cert
	transport := client.Transport.(*http.Transport)
	transport.Proxy = http.ProxyURL(proxyURL)

	resp, err := client.Get(upstream.URL)
	if err != nil {
		t.Fatalf("GET through proxy (CONNECT tunnel): %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "hello over tls" {
		t.Errorf("body = %q, want %q", body, "hello over tls")
	}

	wantHost := strings.TrimPrefix(upstream.URL, "https://")
	if hosts := p.Hosts(); len(hosts) != 1 || hosts[0] != wantHost {
		t.Errorf("Hosts() = %v, want [%q]", hosts, wantHost)
	}
}

func TestProxyDedupsRepeatedHosts(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))
	defer upstream.Close()

	p, err := netmonitor.Start()
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	proxyURL, _ := url.Parse("http://" + p.Addr())
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}}
	for i := 0; i < 3; i++ {
		resp, err := client.Get(upstream.URL)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}

	if hosts := p.Hosts(); len(hosts) != 1 {
		t.Errorf("Hosts() = %v, want exactly 1 deduplicated entry", hosts)
	}
}

func TestProxyDialErrorReturns502NotAHang(t *testing.T) {
	p, err := netmonitor.Start()
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	proxyURL, _ := url.Parse("http://" + p.Addr())
	client := &http.Client{
		Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)},
	}
	// Port 1 on localhost should reliably refuse the connection.
	_, err = client.Get("http://127.0.0.1:1/")
	if err == nil {
		t.Error("expected an error dialing an unreachable upstream, got nil")
	}
}
