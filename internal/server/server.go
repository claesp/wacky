// Package server exposes the wiki over HTTP.
//
// The whole surface is read-only: every route is a safe, idempotent GET, so
// requests can be repeated, cached, prefetched or retried without changing
// anything in the repository.
package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/claesp/wacky/internal/config"
	"github.com/claesp/wacky/internal/wiki"
	"github.com/claesp/wacky/web"
)

// Server wires the page store to an HTTP handler.
type Server struct {
	cfg   config.Config
	store *wiki.Store
	log   *slog.Logger
	tmpl  *templates
	mux   http.Handler
	// assets is a content hash of the embedded static files, appended to
	// their URLs so a new binary invalidates the browser's copy.
	assets string
}

// New builds a Server. Templates are parsed once, up front, so a broken
// template fails at start-up rather than on a request.
func New(cfg config.Config, store *wiki.Store, log *slog.Logger) (*Server, error) {
	if log == nil {
		log = slog.Default()
	}
	tmpl, err := newTemplates(web.FS())
	if err != nil {
		return nil, fmt.Errorf("parse templates: %w", err)
	}

	static, err := web.Static()
	if err != nil {
		return nil, fmt.Errorf("static assets: %w", err)
	}

	s := &Server{cfg: cfg, store: store, log: log, tmpl: tmpl, assets: assetVersion(static)}
	handler, err := s.routes(static)
	if err != nil {
		return nil, err
	}
	s.mux = handler
	return s, nil
}

// assetVersion hashes the embedded static files. The value only changes when
// the binary does, which is exactly when a cached copy must be discarded.
func assetVersion(fsys fs.FS) string {
	sum := sha256.New()
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		sum.Write([]byte(p))
		sum.Write(data)
		return nil
	})
	if err != nil {
		// A hash we cannot compute must not pin a stale stylesheet, so fall
		// back to a value that changes every run.
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(sum.Sum(nil))[:12]
}

// ServeHTTP makes Server an http.Handler, which keeps it directly testable
// with httptest.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

// routes registers every endpoint and wraps the mux in the middleware chain.
func (s *Server) routes(static fs.FS) (http.Handler, error) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.handleHome)
	mux.HandleFunc("GET /wiki/{path...}", s.handlePage)
	mux.HandleFunc("GET /raw/{path...}", s.handleRaw)
	mux.HandleFunc("GET /history/{path...}", s.handleHistory)
	mux.HandleFunc("GET /pages", s.handleIndex)
	mux.HandleFunc("GET /search", s.handleSearch)
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.Handle("GET /static/", http.StripPrefix("/static/", cacheStatic(http.FileServerFS(static))))
	// Registering the catch-all for GET only lets the mux answer any other
	// method with 405: this server never accepts a write.
	mux.HandleFunc("GET /", s.handleNotFound)

	return chain(mux,
		recoverPanic(s.log),
		logRequests(s.log),
		securityHeaders,
	), nil
}

// Run serves until ctx is cancelled, then drains in-flight requests.
func (s *Server) Run(ctx context.Context) error {
	listener, err := net.Listen("tcp", s.cfg.Addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", s.cfg.Addr, err)
	}

	httpSrv := &http.Server{
		Handler:           s,
		ReadHeaderTimeout: s.cfg.ReadTimeout,
		ReadTimeout:       s.cfg.ReadTimeout,
		WriteTimeout:      s.cfg.WriteTimeout,
		IdleTimeout:       s.cfg.IdleTimeout,
		ErrorLog:          slog.NewLogLogger(s.log.Handler(), slog.LevelWarn),
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}

	// Background index rebuilds stop as soon as the server is shutting down.
	watchCtx, stopWatch := context.WithCancel(ctx)
	defer stopWatch()
	go s.store.Watch(watchCtx, s.cfg.ReloadInterval)

	serveErr := make(chan error, 1)
	go func() { serveErr <- httpSrv.Serve(listener) }()

	stats := s.store.Stats()
	s.log.Info("wiki listening",
		"addr", listener.Addr().String(),
		"repo", stats.Root,
		"ref", refOrWorkingTree(stats.Ref),
		"pages", stats.Pages,
	)

	select {
	case err := <-serveErr:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve: %w", err)
	case <-ctx.Done():
	}

	s.log.Info("shutting down", "timeout", s.cfg.ShutdownTimeout)
	stopWatch()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), s.cfg.ShutdownTimeout)
	defer cancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	s.log.Info("stopped")
	return nil
}

func refOrWorkingTree(ref string) string {
	if ref == "" {
		return "working tree"
	}
	return ref
}

// cacheStatic caches an asset forever when the URL carries a content version,
// and only briefly otherwise. Caching an unversioned URL for long would keep a
// stylesheet fix from ever reaching a browser that already has the old one.
func cacheStatic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("v") != "" {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "public, max-age=300")
		}
		next.ServeHTTP(w, r)
	})
}

// timeoutFor is the per-request budget for repository access.
func (s *Server) timeoutFor(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), s.cfg.GitTimeout+2*time.Second)
}
