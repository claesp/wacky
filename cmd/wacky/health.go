package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/claesp/wacky/internal/config"
)

// healthCheckTimeout bounds the probe. It is short because a container health
// check runs on a timer of its own and a hung probe is a failed probe.
const healthCheckTimeout = 3 * time.Second

// healthCheck asks a running server whether it is serving, and reports the
// answer as an error so the process exits non-zero when it is not.
//
// This exists for images that carry no shell or HTTP client: the binary is the
// only executable present, so it has to be able to probe itself. Where the
// probe runs outside the container — a Kubernetes httpGet probe is performed by
// the kubelet — this is unnecessary.
func healthCheck(ctx context.Context, cfg config.Config) error {
	ctx, cancel := context.WithTimeout(ctx, healthCheckTimeout)
	defer cancel()

	url := "http://" + dialAddr(cfg.Addr) + "/healthz"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("health check: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("health check: %w", err)
	}
	defer resp.Body.Close()
	// Draining lets the connection be reused and keeps the server's log tidy.
	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("health check: %s returned %s", url, resp.Status)
	}
	return nil
}

// dialAddr turns a listen address into one a client can connect to. A server
// listening on every interface is configured as "0.0.0.0:8080" or ":8080",
// neither of which is a sensible destination.
func dialAddr(listen string) string {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return listen
	}
	switch host {
	case "", "0.0.0.0", "::", "[::]":
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}
