// Package config resolves the runtime configuration of the wiki server from
// command line flags and the environment.
//
// Load is a pure function of its arguments: given the same flags and
// environment it always produces the same Config, which keeps process startup
// idempotent.
package config

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Defaults for every tunable. They are exported so tests and callers can refer
// to them instead of duplicating literals.
const (
	DefaultAddr            = "127.0.0.1:8080"
	DefaultRepoPath        = "."
	DefaultOwner           = "The Authors"
	DefaultReloadInterval  = 15 * time.Second
	DefaultReadTimeout     = 10 * time.Second
	DefaultWriteTimeout    = 30 * time.Second
	DefaultIdleTimeout     = 2 * time.Minute
	DefaultShutdownTimeout = 15 * time.Second
	DefaultGitTimeout      = 20 * time.Second
	DefaultMaxFileSize     = 4 << 20 // 4 MiB
	DefaultHistoryLimit    = 30
)

// Config is the fully resolved configuration of a wiki server.
type Config struct {
	// Addr is the TCP address the HTTP server listens on.
	Addr string
	// RepoPath is an absolute path inside the Git repository to serve.
	RepoPath string
	// Ref pins the wiki to a Git revision (branch, tag or commit). When empty
	// the working tree is served.
	Ref string
	// Title is the site name shown in the header and page titles.
	Title string
	// Owner names the copyright holder in the site footer. Empty means no
	// copyright notice is shown.
	Owner string
	// ReloadInterval controls how often the page index is rebuilt from the
	// repository. Zero disables background reloading.
	ReloadInterval time.Duration

	ReadTimeout     time.Duration
	WriteTimeout    time.Duration
	IdleTimeout     time.Duration
	ShutdownTimeout time.Duration
	GitTimeout      time.Duration

	// MaxFileSize caps the size of a file the wiki will read into memory.
	MaxFileSize int64
	// HistoryLimit caps how many commits the history view shows.
	HistoryLimit int

	LogLevel slog.Level
}

// Default returns the configuration used when neither flags nor environment
// variables are set. The zero value of Config is deliberately not useful; use
// Default or Load instead.
func Default() Config {
	return Config{
		Addr:            DefaultAddr,
		RepoPath:        DefaultRepoPath,
		Owner:           DefaultOwner,
		ReloadInterval:  DefaultReloadInterval,
		ReadTimeout:     DefaultReadTimeout,
		WriteTimeout:    DefaultWriteTimeout,
		IdleTimeout:     DefaultIdleTimeout,
		ShutdownTimeout: DefaultShutdownTimeout,
		GitTimeout:      DefaultGitTimeout,
		MaxFileSize:     DefaultMaxFileSize,
		HistoryLimit:    DefaultHistoryLimit,
		LogLevel:        slog.LevelInfo,
	}
}

// Load parses args (typically os.Args[1:]) on top of environment defaults read
// through getenv, then validates the result. Usage and errors are written to
// output. A request for -h returns flag.ErrHelp, which callers should treat as
// a successful exit.
func Load(args []string, getenv func(string) string, output io.Writer) (Config, error) {
	if getenv == nil {
		getenv = func(string) string { return "" }
	}

	cfg := Default()
	fs := flag.NewFlagSet("wacky", flag.ContinueOnError)
	fs.SetOutput(output)
	fs.Usage = func() {
		fmt.Fprintf(output, "Usage: wacky [flags] [repository-path]\n\nFlags:\n")
		fs.PrintDefaults()
		fmt.Fprintf(output, "\nEvery flag has a WACKY_-prefixed environment variable equivalent,\n"+
			"for example WACKY_ADDR or WACKY_RELOAD_INTERVAL.\n")
	}

	var level string
	fs.StringVar(&cfg.Addr, "addr", envString(getenv, "WACKY_ADDR", cfg.Addr), "address to listen on")
	fs.StringVar(&cfg.RepoPath, "repo", envString(getenv, "WACKY_REPO", cfg.RepoPath), "path to the Git repository to serve")
	fs.StringVar(&cfg.Ref, "ref", envString(getenv, "WACKY_REF", cfg.Ref), "Git revision to serve (default: the working tree)")
	fs.StringVar(&cfg.Title, "title", envString(getenv, "WACKY_TITLE", cfg.Title), "site title (default: repository directory name)")
	fs.StringVar(&cfg.Owner, "owner", envString(getenv, "WACKY_OWNER", cfg.Owner), "copyright holder shown in the footer")
	fs.StringVar(&level, "log-level", envString(getenv, "WACKY_LOG_LEVEL", "info"), "log level: debug, info, warn or error")
	fs.Int64Var(&cfg.MaxFileSize, "max-file-size", envInt64(getenv, "WACKY_MAX_FILE_SIZE", cfg.MaxFileSize), "maximum size in bytes of a file served from the repository")
	fs.IntVar(&cfg.HistoryLimit, "history-limit", int(envInt64(getenv, "WACKY_HISTORY_LIMIT", int64(cfg.HistoryLimit))), "number of commits shown in the history view")

	durations := []struct {
		p     *time.Duration
		name  string
		env   string
		usage string
	}{
		{&cfg.ReloadInterval, "reload-interval", "WACKY_RELOAD_INTERVAL", "how often to rebuild the page index (0 disables)"},
		{&cfg.ReadTimeout, "read-timeout", "WACKY_READ_TIMEOUT", "HTTP read timeout"},
		{&cfg.WriteTimeout, "write-timeout", "WACKY_WRITE_TIMEOUT", "HTTP write timeout"},
		{&cfg.IdleTimeout, "idle-timeout", "WACKY_IDLE_TIMEOUT", "HTTP idle timeout"},
		{&cfg.ShutdownTimeout, "shutdown-timeout", "WACKY_SHUTDOWN_TIMEOUT", "how long to wait for in-flight requests on shutdown"},
		{&cfg.GitTimeout, "git-timeout", "WACKY_GIT_TIMEOUT", "timeout for a single git invocation"},
	}
	for _, d := range durations {
		def, err := envDuration(getenv, d.env, *d.p)
		if err != nil {
			return Config{}, err
		}
		fs.DurationVar(d.p, d.name, def, d.usage)
	}

	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}
	if fs.NArg() > 1 {
		return Config{}, fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args()[1:], " "))
	}
	if fs.NArg() == 1 {
		cfg.RepoPath = fs.Arg(0)
	}

	lvl, err := parseLevel(level)
	if err != nil {
		return Config{}, err
	}
	cfg.LogLevel = lvl

	if err := cfg.normalize(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// normalize resolves derived values and rejects unusable settings.
func (c *Config) normalize() error {
	if strings.TrimSpace(c.Addr) == "" {
		return errors.New("addr must not be empty")
	}

	abs, err := filepath.Abs(c.RepoPath)
	if err != nil {
		return fmt.Errorf("resolve repo path %q: %w", c.RepoPath, err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return fmt.Errorf("repo path %q: %w", abs, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("repo path %q is not a directory", abs)
	}
	c.RepoPath = abs

	if strings.TrimSpace(c.Title) == "" {
		c.Title = filepath.Base(abs)
	}
	// An explicitly blank owner falls back to the default rather than
	// dropping the copyright notice.
	if c.Owner = strings.TrimSpace(c.Owner); c.Owner == "" {
		c.Owner = DefaultOwner
	}
	if c.MaxFileSize <= 0 {
		return fmt.Errorf("max-file-size must be positive, got %d", c.MaxFileSize)
	}
	if c.HistoryLimit <= 0 {
		return fmt.Errorf("history-limit must be positive, got %d", c.HistoryLimit)
	}
	if c.ReloadInterval < 0 {
		return fmt.Errorf("reload-interval must not be negative, got %s", c.ReloadInterval)
	}
	if c.ShutdownTimeout <= 0 {
		return fmt.Errorf("shutdown-timeout must be positive, got %s", c.ShutdownTimeout)
	}
	if c.GitTimeout <= 0 {
		return fmt.Errorf("git-timeout must be positive, got %s", c.GitTimeout)
	}
	return nil
}

func envString(getenv func(string) string, key, def string) string {
	if v := strings.TrimSpace(getenv(key)); v != "" {
		return v
	}
	return def
}

func envInt64(getenv func(string) string, key string, def int64) int64 {
	v := strings.TrimSpace(getenv(key))
	if v == "" {
		return def
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return def
	}
	return n
}

func envDuration(getenv func(string) string, key string, def time.Duration) (time.Duration, error) {
	v := strings.TrimSpace(getenv(key))
	if v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("parse %s=%q: %w", key, v, err)
	}
	return d, nil
}

func parseLevel(s string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug, nil
	case "", "info":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("unknown log level %q", s)
	}
}
