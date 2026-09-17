package main

import (
	"context"
	"fmt"
	"os"

	"github.com/hpscript/fastci/internal/analyzer"
	"github.com/hpscript/fastci/internal/netmonitor"
)

// runAndRecordWithNetworkReport wraps runAndRecord with fastci guard's
// runtime network-egress guardrail (--network-report): when enabled, it
// points the test/build run at a local logging proxy for the duration of
// the run and prints every host it contacted afterward. With it disabled
// (the default), this is exactly runAndRecord.
func runAndRecordWithNetworkReport(ctx context.Context, repoRoot string, a analyzer.Analyzer, cwd string, targets []string, opts testOpts) error {
	if !opts.networkReport {
		return runAndRecord(ctx, repoRoot, a, cwd, targets, opts.extraArgs)
	}

	proxy, err := netmonitor.Start()
	if err != nil {
		return fmt.Errorf("starting network monitor: %w", err)
	}
	defer proxy.Close()

	restore := setProxyEnv(proxy.Addr())
	defer restore()

	runErr := runAndRecord(ctx, repoRoot, a, cwd, targets, opts.extraArgs)

	hosts := proxy.Hosts()
	if len(hosts) == 0 {
		fmt.Println("fastci: network report: no outbound connections observed")
	} else {
		fmt.Printf("fastci: network report: %d host(s) contacted:\n", len(hosts))
		for _, h := range hosts {
			fmt.Printf("  %s\n", h)
		}
	}

	return runErr
}

// proxyEnvVars are every environment variable name a well-behaved
// HTTP(S) client is expected to honor as its forward-proxy setting; both
// cases are set since different tools disagree on which one they check
// (curl and most Go/Node HTTP clients accept either).
var proxyEnvVars = []string{"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy"}

// setProxyEnv points every proxyEnvVars entry at addr for the current
// process, returning a restore func that puts each one back exactly as it
// was (set to its old value, or unset if it wasn't set at all) - so a
// --network-report run doesn't permanently clobber a proxy the user's own
// shell had already configured (e.g. a real corporate proxy), only
// overrides it for the duration of this one run.
func setProxyEnv(addr string) (restore func()) {
	proxyURL := "http://" + addr
	prev := make(map[string]string, len(proxyEnvVars))
	wasSet := make(map[string]bool, len(proxyEnvVars))
	for _, k := range proxyEnvVars {
		if v, ok := os.LookupEnv(k); ok {
			prev[k] = v
			wasSet[k] = true
		}
		os.Setenv(k, proxyURL)
	}
	return func() {
		for _, k := range proxyEnvVars {
			if wasSet[k] {
				os.Setenv(k, prev[k])
			} else {
				os.Unsetenv(k)
			}
		}
	}
}
