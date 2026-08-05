// Command wacky serves the Markdown files of a Git repository as a website.
//
//	wacky                       # serve the repository in the current directory
//	wacky ~/notes               # serve another repository
//	wacky -git-ref v1.0 ~/notes # serve a pinned revision
//
// The server never writes to the repository.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	// The timezone database is embedded so that TZ works on an image that
	// carries no files of its own — a scratch image has no /usr/share/zoneinfo,
	// and neither does Alpine unless the tzdata package is installed.
	_ "time/tzdata"

	"github.com/claesp/wacky/internal/config"
	"github.com/claesp/wacky/internal/git"
	"github.com/claesp/wacky/internal/markdown"
	"github.com/claesp/wacky/internal/server"
	"github.com/claesp/wacky/internal/wacky"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, os.Args[1:], os.Getenv, os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "wacky: %v\n", err)
		os.Exit(1)
	}
}

// run holds the whole start-up sequence so that it can be exercised by tests
// without touching global state.
func run(ctx context.Context, args []string, getenv func(string) string, stderr io.Writer) error {
	cfg, err := config.Load(args, getenv, stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	// Probing a running server is a separate job from being one.
	if cfg.HealthCheck {
		return healthCheck(ctx, cfg)
	}

	log := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: cfg.LogLevel}))

	repo, err := git.Open(ctx, cfg.RepoPath,
		git.WithRef(cfg.Ref),
		git.WithTimeout(cfg.GitTimeout),
		git.WithMaxFileSize(cfg.MaxFileSize),
	)
	if err != nil {
		return err
	}

	store := wacky.NewStore(repo, markdown.New(), log)
	if err := store.Reload(ctx); err != nil {
		return fmt.Errorf("build page index: %w", err)
	}

	srv, err := server.New(cfg, store, log)
	if err != nil {
		return err
	}
	return srv.Run(ctx)
}
