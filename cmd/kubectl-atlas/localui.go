// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/lithastra/kubeatlas/pkg/aggregator"
	"github.com/lithastra/kubeatlas/pkg/api"
	"github.com/lithastra/kubeatlas/pkg/discovery"
	"github.com/lithastra/kubeatlas/pkg/extractor"
	"github.com/lithastra/kubeatlas/pkg/store/memory"
)

// localUIStartTimeout bounds how long runLocalUI waits for the
// in-process server to start listening before giving up.
const localUIStartTimeout = 10 * time.Second

// runLocalUI is the third offline mode (--local-ui). Rather than
// rendering a static SVG with graphviz, it runs the KubeAtlas server
// in-process: informers watch the cluster through the kubeconfig and
// feed an in-memory graph store, while the API server and the
// embedded Web UI serve it on a loopback port. The browser opens
// there and the graph is laid out and drawn client-side by Cytoscape,
// so no graphviz `dot` binary is needed.
//
// The server is long-lived — unlike the one-shot SVG path it holds
// until the operator interrupts it (Ctrl-C). Informers cover the
// core Kubernetes GVRs; custom resources contributed by rule packs
// are not tracked in this mode.
func (a *app) runLocalUI(ctx context.Context, t target) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	client, err := discovery.NewClient(a.kubeconfig, a.kubeContext)
	if err != nil {
		return fmt.Errorf("load kubeconfig: %w", err)
	}
	disc := discovery.NewDiscoveryFromClient(client)
	gvrs, err := discovery.FilterAvailableGVRs(ctx, disc, discovery.CoreGVRs)
	if err != nil {
		return fmt.Errorf("discover API resources: %w", err)
	}

	bindHost := a.localUIHost
	if bindHost == "" {
		bindHost = "127.0.0.1"
	}
	addr, err := freePort(bindHost)
	if err != nil {
		return err
	}
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("parse local server address %q: %w", addr, err)
	}
	if !isLoopbackHost(bindHost) {
		_, _ = fmt.Fprintln(os.Stderr, "kubectl-atlas: --local-ui is binding "+bindHost+
			" — the cluster graph and unauthenticated API will be reachable from other hosts on the network")
	}

	st := memory.New()
	srv := api.New(addr, st, aggregator.NewRegistry(), api.WithWebFS(webFS))
	mgr := discovery.NewInformerManager(client.Dynamic(), st,
		discovery.WithGVRs(gvrs),
		discovery.WithExtractor(extractor.Default()),
		discovery.WithOnSynced(srv.Readiness().MarkReady),
		discovery.WithBroadcaster(srv.Hub().BroadcastEvent),
	)

	// Each component cancels the shared context when it returns, so a
	// crash (or a port-bind race) in one winds the other down and
	// unblocks the wait below. Both errors land in results.
	results := make(chan error, 2)
	go func() { err := srv.Start(ctx); cancel(); results <- err }()
	go func() { err := mgr.Start(ctx); cancel(); results <- err }()

	// A wildcard bind host (0.0.0.0, ::) is not browsable — dial and
	// open a concrete loopback address on the same port instead.
	browseAddr := net.JoinHostPort(browseHost(bindHost), port)
	if err := waitListening(ctx, browseAddr); err != nil {
		if drained := drainLocalUI(results); drained != nil {
			return drained
		}
		if errors.Is(err, context.Canceled) {
			return nil
		}
		return err
	}

	base := "http://" + browseAddr
	dst := t.onlineURL(base)
	_, _ = fmt.Fprintln(os.Stdout, "KubeAtlas is running locally at "+base)
	switch {
	case runningUnderWSL():
		// WSL2 forwards Windows localhost:<port> to the matching port
		// inside the distro, so that — not a WSL interface IP — is
		// what a browser on the Windows host should open.
		winURL := t.onlineURL("http://" + net.JoinHostPort("localhost", port))
		_, _ = fmt.Fprintln(os.Stdout, "  from a browser on the Windows host: "+winURL)
	case isWildcardHost(bindHost):
		// A wildcard bind is reachable from other hosts on the LAN —
		// list those addresses so the operator knows what to open.
		for _, ip := range nonLoopbackIPv4s() {
			_, _ = fmt.Fprintln(os.Stdout, "  also reachable at http://"+net.JoinHostPort(ip, port))
		}
	}
	a.launch(dst)
	_, _ = fmt.Fprintln(os.Stdout, "Press Ctrl-C to stop.")

	waitForInterrupt(ctx)
	cancel()
	return drainLocalUI(results)
}

// drainLocalUI waits for both local-ui components to stop and returns
// the first non-cancellation error, or nil for a clean shutdown.
func drainLocalUI(results <-chan error) error {
	var first error
	for i := 0; i < 2; i++ {
		if err := <-results; err != nil && !errors.Is(err, context.Canceled) && first == nil {
			first = err
		}
	}
	return first
}

// freePort probes for an unused TCP port on host and returns the
// bind address as host:port. There is a small race between closing
// the probe listener and the API server binding the port; for a
// local, interactive tool that window is acceptable.
func freePort(host string) (string, error) {
	l, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
	if err != nil {
		return "", fmt.Errorf("bind a free port on %s: %w", host, err)
	}
	defer func() { _ = l.Close() }()
	return l.Addr().String(), nil
}

// isWildcardHost reports whether host binds every interface.
func isWildcardHost(host string) bool {
	switch host {
	case "", "0.0.0.0", "::", "[::]":
		return true
	default:
		return false
	}
}

// browseHost maps a bind host to an address a browser can actually
// reach: wildcard binds (0.0.0.0, ::) collapse to loopback, since
// the server listens on every interface including it.
func browseHost(bindHost string) string {
	if isWildcardHost(bindHost) {
		return "127.0.0.1"
	}
	return bindHost
}

// nonLoopbackIPv4s returns the machine's non-loopback IPv4 addresses.
// When --local-ui binds a wildcard host these are the addresses a
// browser on another host can reach — including the Windows side of
// a WSL setup, where the WSL loopback is not the Windows loopback.
func nonLoopbackIPv4s() []string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	var out []string
	for _, a := range addrs {
		ipNet, ok := a.(*net.IPNet)
		if !ok || ipNet.IP.IsLoopback() {
			continue
		}
		if ip4 := ipNet.IP.To4(); ip4 != nil {
			out = append(out, ip4.String())
		}
	}
	return out
}

// runningUnderWSL reports whether the process is inside WSL, where a
// loopback bind is often not reachable from a browser on the Windows
// host.
func runningUnderWSL() bool {
	b, err := os.ReadFile("/proc/sys/kernel/osrelease")
	if err != nil {
		return false
	}
	s := strings.ToLower(string(b))
	return strings.Contains(s, "microsoft") || strings.Contains(s, "wsl")
}

// isLoopbackHost reports whether host keeps the --local-ui server
// private to this machine.
func isLoopbackHost(host string) bool {
	switch host {
	case "127.0.0.1", "localhost", "::1", "[::1]":
		return true
	default:
		return false
	}
}

// waitListening blocks until addr accepts a TCP connection, the
// context is cancelled (a component stopped early), or
// localUIStartTimeout elapses.
func waitListening(ctx context.Context, addr string) error {
	deadline := time.Now().Add(localUIStartTimeout)
	for {
		conn, err := net.DialTimeout("tcp", addr, time.Second)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("local server did not start listening on %s", addr)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}
