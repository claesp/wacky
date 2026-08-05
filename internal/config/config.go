// Package config resolves the runtime configuration of the wiki server from
// command line flags and the environment.
//
// Load is a pure function of its arguments: given the same flags and
// environment it always produces the same Config, which keeps process startup
// idempotent.
package config

import (
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
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
	DefaultTitle           = "Wacky"
	DefaultBrandColor      = "#1f5fa8"
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
	// BrandTitle is the site name shown in the header and page titles.
	BrandTitle string
	// BrandColor is the "#rrggbb" the header gradient is built from.
	BrandColor string
	// BrandImageURL replaces the header text with an image. It must be
	// relative or https, because the Content-Security-Policy allows nothing
	// else.
	BrandImageURL string
	// BrandImageData is a canonical "data:image/...;base64,..." URI. It wins
	// over BrandImageURL when both are set.
	BrandImageData string
	// Owner names the copyright holder in the site footer. Empty means no
	// copyright notice is shown.
	Owner string
	// CommitURL is the base URL a commit hash is appended to, for example
	// "https://github.com/org/repo/commit/". Empty leaves hashes unlinked.
	CommitURL string
	// ClassificationLow and ClassificationHigh bound the classification
	// notice shown above a page. Nil means unset; with both unset no notice
	// is ever shown, whatever the front matter says.
	ClassificationLow  *int
	ClassificationHigh *int
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

	// HealthCheck asks the process to probe a running server and exit, rather
	// than start one. It exists so an image with no shell can still declare a
	// container health check.
	HealthCheck bool
}

// Default returns the configuration used when neither flags nor environment
// variables are set. The zero value of Config is deliberately not useful; use
// Default or Load instead.
func Default() Config {
	return Config{
		Addr:            DefaultAddr,
		RepoPath:        DefaultRepoPath,
		Owner:           DefaultOwner,
		BrandTitle:      DefaultTitle,
		BrandColor:      DefaultBrandColor,
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

	var level, classLow, classHigh string
	fs.StringVar(&classLow, "classification-threshold-low",
		envString(getenv, "WACKY_CLASSIFICATION_THRESHOLD_LOW", ""),
		"classification_level at which a page carries a notice (unset: no notice)")
	fs.StringVar(&classHigh, "classification-threshold-high",
		envString(getenv, "WACKY_CLASSIFICATION_THRESHOLD_HIGH", ""),
		"classification_level at which that notice becomes a severe one (unset: no notice)")
	fs.StringVar(&cfg.Addr, "addr", envString(getenv, "WACKY_ADDR", cfg.Addr), "address to listen on")
	fs.StringVar(&cfg.RepoPath, "git-repo", envString(getenv, "WACKY_GIT_REPO", cfg.RepoPath), "path to the Git repository to serve")
	fs.StringVar(&cfg.Ref, "git-ref", envString(getenv, "WACKY_GIT_REF", cfg.Ref), "Git revision to serve (default: the working tree)")
	fs.StringVar(&cfg.BrandTitle, "brand-title", envString(getenv, "WACKY_BRAND_TITLE", cfg.BrandTitle), "site title")
	fs.StringVar(&cfg.BrandColor, "brand-color", envString(getenv, "WACKY_BRAND_COLOR", cfg.BrandColor),
		"header colour as an RGB hex string, e.g. #1f5fa8")
	fs.StringVar(&cfg.BrandImageURL, "brand-image-url", envString(getenv, "WACKY_BRAND_IMAGE_URL", ""),
		"header logo, as a relative or https URL (replaces the title text)")
	fs.StringVar(&cfg.BrandImageData, "brand-image-data", envString(getenv, "WACKY_BRAND_IMAGE_DATA", ""),
		"header logo as base64 image data (wins over brand-image-url)")
	fs.StringVar(&cfg.Owner, "owner", envString(getenv, "WACKY_OWNER", cfg.Owner), "copyright holder shown in the footer")
	fs.StringVar(&cfg.CommitURL, "git-commit-url", envString(getenv, "WACKY_GIT_COMMIT_URL", cfg.CommitURL),
		"base URL a commit hash is appended to, e.g. https://github.com/org/repo/commit/ (default: hashes are not linked)")
	fs.BoolVar(&cfg.HealthCheck, "health-check", envBool(getenv, "WACKY_HEALTH_CHECK", false),
		"probe a running server's /healthz at -addr and exit; 0 when it is serving")
	fs.StringVar(&level, "log-level", envString(getenv, "WACKY_LOG_LEVEL", "info"), "log level: debug, info, warn or error")
	fs.Int64Var(&cfg.MaxFileSize, "max-file-size", envInt64(getenv, "WACKY_MAX_FILE_SIZE", cfg.MaxFileSize), "maximum size in bytes of a file served from the repository")
	fs.IntVar(&cfg.HistoryLimit, "git-history-limit", int(envInt64(getenv, "WACKY_GIT_HISTORY_LIMIT", int64(cfg.HistoryLimit))), "number of commits shown in the history view")

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

	if cfg.ClassificationLow, err = optionalInt(classLow, "classification-threshold-low"); err != nil {
		return Config{}, err
	}
	if cfg.ClassificationHigh, err = optionalInt(classHigh, "classification-threshold-high"); err != nil {
		return Config{}, err
	}

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

	if strings.TrimSpace(c.BrandTitle) == "" {
		c.BrandTitle = DefaultTitle
	}
	if err := c.normalizeBrandImage(); err != nil {
		return err
	}
	brand, err := normalizeHexColor(c.BrandColor)
	if err != nil {
		return err
	}
	c.BrandColor = brand
	// An explicitly blank owner falls back to the default rather than
	// dropping the copyright notice.
	if c.Owner = strings.TrimSpace(c.Owner); c.Owner == "" {
		c.Owner = DefaultOwner
	}
	if err := c.normalizeCommitURL(); err != nil {
		return err
	}
	if c.ClassificationLow != nil && c.ClassificationHigh != nil &&
		*c.ClassificationLow > *c.ClassificationHigh {
		return fmt.Errorf("classification-threshold-low (%d) must not exceed classification-threshold-high (%d)",
			*c.ClassificationLow, *c.ClassificationHigh)
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

// normalizeCommitURL validates the commit base URL and makes sure a hash can
// simply be appended to it.
func (c *Config) normalizeCommitURL() error {
	c.CommitURL = strings.TrimSpace(c.CommitURL)
	if c.CommitURL == "" {
		return nil
	}

	u, err := url.Parse(c.CommitURL)
	if err != nil {
		return fmt.Errorf("parse commit-url %q: %w", c.CommitURL, err)
	}
	// A bad scheme here would end up in an href, so it is refused at start-up
	// rather than rendered into every page.
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("commit-url %q must be an http or https URL", c.CommitURL)
	}
	if u.Host == "" {
		return fmt.Errorf("commit-url %q has no host", c.CommitURL)
	}

	// "…/commit" needs a separator before the hash; "…?id=" already ends in one.
	if !strings.HasSuffix(c.CommitURL, "/") && !strings.HasSuffix(c.CommitURL, "=") {
		c.CommitURL += "/"
	}
	return nil
}

// MaxBrandImageBytes caps the decoded size of an inline header logo. It is
// sent on every page, so a large one would cost every request.
const MaxBrandImageBytes = 256 << 10

// normalizeBrandImage validates both header logo settings. They are mutually
// exclusive; when both are given the inline data wins, matching the documented
// precedence.
func (c *Config) normalizeBrandImage() error {
	c.BrandImageURL = strings.TrimSpace(c.BrandImageURL)
	if c.BrandImageURL != "" {
		if err := checkBrandImageURL(c.BrandImageURL); err != nil {
			return err
		}
	}

	data, err := normalizeImageData(c.BrandImageData)
	if err != nil {
		return err
	}
	c.BrandImageData = data

	// The URL is redundant once inline data is present.
	if c.BrandImageData != "" {
		c.BrandImageURL = ""
	}
	return nil
}

// checkBrandImageURL rejects anything the page's Content-Security-Policy would
// refuse to load, so a mistake fails at start-up instead of silently leaving a
// broken image in the header.
func checkBrandImageURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("parse brand-image-url %q: %w", raw, err)
	}
	switch u.Scheme {
	case "":
		if strings.HasPrefix(raw, "//") {
			return fmt.Errorf("brand-image-url %q must name its scheme", raw)
		}
		return nil
	case "https":
		if u.Host == "" {
			return fmt.Errorf("brand-image-url %q has no host", raw)
		}
		return nil
	default:
		return fmt.Errorf("brand-image-url %q must be relative or https: "+
			"the Content-Security-Policy blocks %s images", raw, u.Scheme)
	}
}

// normalizeImageData accepts a full data URI or bare base64, and returns a
// canonical "data:<type>;base64,<data>" URI.
func normalizeImageData(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}

	payload := raw
	if strings.HasPrefix(raw, "data:") {
		_, after, found := strings.Cut(raw, ";base64,")
		if !found {
			return "", fmt.Errorf("brand-image-data must be base64; %q is not", raw)
		}
		payload = after
	}
	payload = strings.Join(strings.Fields(payload), "")

	decoded, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return "", fmt.Errorf("decode brand-image-data: %w", err)
	}
	if len(decoded) == 0 {
		return "", errors.New("brand-image-data decoded to nothing")
	}
	if len(decoded) > MaxBrandImageBytes {
		return "", fmt.Errorf("brand-image-data is %d bytes, over the %d-byte limit",
			len(decoded), MaxBrandImageBytes)
	}

	mediaType := detectImageType(decoded)
	if mediaType == "" {
		return "", errors.New("brand-image-data does not decode to a known image format")
	}
	return "data:" + mediaType + ";base64," + base64.StdEncoding.EncodeToString(decoded), nil
}

// detectImageType names the image format of some bytes, or returns "".
func detectImageType(data []byte) string {
	// SVG is XML, which content sniffing reports as text rather than an image.
	if head := strings.ToLower(string(data[:min(len(data), 1024)])); strings.Contains(head, "<svg") {
		return "image/svg+xml"
	}
	if t := http.DetectContentType(data); strings.HasPrefix(t, "image/") {
		return t
	}
	return ""
}

// normalizeHexColor accepts "#rgb" or "#rrggbb", with or without the hash,
// and returns the canonical "#rrggbb" form.
func normalizeHexColor(raw string) (string, error) {
	digits := strings.TrimPrefix(strings.TrimSpace(raw), "#")
	if digits == "" {
		return DefaultBrandColor, nil
	}

	for _, r := range digits {
		if !strings.ContainsRune("0123456789abcdefABCDEF", r) {
			return "", fmt.Errorf("brand-color %q: %q is not a hex digit", raw, r)
		}
	}
	if len(digits) == 3 {
		digits = string([]byte{digits[0], digits[0], digits[1], digits[1], digits[2], digits[2]})
	}
	if len(digits) != 6 {
		return "", fmt.Errorf("brand-color %q must be 3 or 6 hex digits, e.g. #1f5fa8", raw)
	}
	return "#" + strings.ToLower(digits), nil
}

// optionalInt parses a setting that may be left unset, which a plain integer
// flag cannot express: zero is a legitimate threshold.
func optionalInt(raw, name string) (*int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return nil, fmt.Errorf("parse %s=%q: must be a whole number", name, raw)
	}
	return &n, nil
}

func envString(getenv func(string) string, key, def string) string {
	if v := strings.TrimSpace(getenv(key)); v != "" {
		return v
	}
	return def
}

func envBool(getenv func(string) string, key string, def bool) bool {
	v := strings.TrimSpace(getenv(key))
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
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
