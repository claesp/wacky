package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/claesp/wacky/internal/config"
)

// A wildcard listen address is not a destination, so the probe has to rewrite
// it before dialling.
func TestDialAddr(t *testing.T) {
	tests := map[string]string{
		"0.0.0.0:8080":   "127.0.0.1:8080",
		":8080":          "127.0.0.1:8080",
		"[::]:8080":      "127.0.0.1:8080",
		"127.0.0.1:8080": "127.0.0.1:8080",
		"localhost:9000": "localhost:9000",
		"[::1]:8080":     "[::1]:8080",
		// Anything unparseable is passed through for the dialler to reject.
		"nonsense": "nonsense",
	}

	for in, want := range tests {
		if got := dialAddr(in); got != want {
			t.Errorf("dialAddr(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHealthCheck(t *testing.T) {
	t.Run("a serving server passes", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/healthz" {
				t.Errorf("probed %q, want /healthz", r.URL.Path)
			}
			w.Write([]byte(`{"status":"ok"}`))
		}))
		defer srv.Close()

		cfg := config.Default()
		cfg.Addr = strings.TrimPrefix(srv.URL, "http://")
		if err := healthCheck(context.Background(), cfg); err != nil {
			t.Errorf("healthCheck() = %v, want nil", err)
		}
	})

	t.Run("an unhealthy server fails", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
		}))
		defer srv.Close()

		cfg := config.Default()
		cfg.Addr = strings.TrimPrefix(srv.URL, "http://")
		if err := healthCheck(context.Background(), cfg); err == nil {
			t.Error("healthCheck() = nil, want an error for 503")
		}
	})

	t.Run("nothing listening fails", func(t *testing.T) {
		// Port 1 on the loopback interface has nothing on it.
		cfg := config.Default()
		cfg.Addr = "127.0.0.1:1"
		if err := healthCheck(context.Background(), cfg); err == nil {
			t.Error("healthCheck() = nil, want a connection error")
		}
	})
}
