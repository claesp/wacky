package config

import (
	"errors"
	"flag"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func env(pairs map[string]string) func(string) string {
	return func(key string) string { return pairs[key] }
}

func TestLoadDefaults(t *testing.T) {
	dir := t.TempDir()

	cfg, err := Load([]string{"-repo", dir}, env(nil), io.Discard)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.Addr != DefaultAddr {
		t.Errorf("Addr = %q, want %q", cfg.Addr, DefaultAddr)
	}
	if !filepath.IsAbs(cfg.RepoPath) {
		t.Errorf("RepoPath = %q, want an absolute path", cfg.RepoPath)
	}
	if cfg.Title != DefaultTitle {
		t.Errorf("Title = %q, want %q", cfg.Title, DefaultTitle)
	}
	if cfg.BrandColor != DefaultBrandColor {
		t.Errorf("BrandColor = %q, want %q", cfg.BrandColor, DefaultBrandColor)
	}
	if cfg.LogLevel != slog.LevelInfo {
		t.Errorf("LogLevel = %v, want info", cfg.LogLevel)
	}
	if cfg.Owner != DefaultOwner {
		t.Errorf("Owner = %q, want %q", cfg.Owner, DefaultOwner)
	}
}

// A commit hash is appended to the base URL, so it must end in a separator,
// and it must never be able to carry a dangerous scheme into an href.
func TestCommitURL(t *testing.T) {
	dir := t.TempDir()

	valid := []struct{ in, want string }{
		{"", ""},
		{"https://github.com/org/repo/commit/", "https://github.com/org/repo/commit/"},
		{"https://gitlab.com/group/proj/-/commit/", "https://gitlab.com/group/proj/-/commit/"},
		// A missing trailing separator is added rather than silently producing
		// URLs like ".../commitabc123".
		{"https://github.com/org/repo/commit", "https://github.com/org/repo/commit/"},
		// A query-style base already ends in its separator.
		{"https://git.example.org/repo/commit/?id=", "https://git.example.org/repo/commit/?id="},
		{"  https://example.com/c/  ", "https://example.com/c/"},
		{"http://internal.example/c/", "http://internal.example/c/"},
	}
	for _, tt := range valid {
		cfg, err := Load([]string{"-commit-url", tt.in, dir}, env(nil), io.Discard)
		if err != nil {
			t.Errorf("Load(-commit-url %q): %v", tt.in, err)
			continue
		}
		if cfg.CommitURL != tt.want {
			t.Errorf("CommitURL for %q = %q, want %q", tt.in, cfg.CommitURL, tt.want)
		}
	}

	invalid := []string{
		"javascript:alert(1)",
		"data:text/html,<script>alert(1)</script>",
		"ftp://example.com/commit/",
		"/just/a/path/",
		"https:///no-host/",
	}
	for _, in := range invalid {
		if cfg, err := Load([]string{"-commit-url", in, dir}, env(nil), io.Discard); err == nil {
			t.Errorf("Load(-commit-url %q) succeeded with %q, want an error", in, cfg.CommitURL)
		}
	}

	// The environment supplies it just like every other setting.
	cfg, err := Load([]string{dir}, env(map[string]string{"WACKY_COMMIT_URL": "https://example.com/c/"}), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CommitURL != "https://example.com/c/" {
		t.Errorf("CommitURL from the environment = %q", cfg.CommitURL)
	}
}

func TestBrandColor(t *testing.T) {
	dir := t.TempDir()

	valid := map[string]string{
		"#1f5fa8": "#1f5fa8",
		"1f5fa8":  "#1f5fa8",
		"#1F5FA8": "#1f5fa8",
		"#abc":    "#aabbcc",
		"abc":     "#aabbcc",
		"  #fff ": "#ffffff",
		"":        DefaultBrandColor,
	}
	for in, want := range valid {
		cfg, err := Load([]string{"-brand-color", in, dir}, env(nil), io.Discard)
		if err != nil {
			t.Errorf("Load(-brand-color %q): %v", in, err)
			continue
		}
		if cfg.BrandColor != want {
			t.Errorf("BrandColor for %q = %q, want %q", in, cfg.BrandColor, want)
		}
	}

	for _, bad := range []string{"#12345", "#gggggg", "blue", "#1f5fa8ff", "##fff"} {
		if _, err := Load([]string{"-brand-color", bad, dir}, env(nil), io.Discard); err == nil {
			t.Errorf("Load(-brand-color %q) succeeded, want an error", bad)
		}
	}

	cfg, err := Load([]string{dir}, env(map[string]string{"WACKY_BRAND_COLOR": "#c8102e"}), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BrandColor != "#c8102e" {
		t.Errorf("BrandColor from the environment = %q", cfg.BrandColor)
	}
}

// Both thresholds are genuinely optional, and zero is a real value — so they
// cannot be plain integers with a zero default.
func TestClassificationThresholds(t *testing.T) {
	dir := t.TempDir()

	load := func(t *testing.T, args []string, environment map[string]string) Config {
		t.Helper()
		cfg, err := Load(append(args, dir), env(environment), io.Discard)
		if err != nil {
			t.Fatalf("Load(%v): %v", args, err)
		}
		return cfg
	}

	t.Run("unset by default", func(t *testing.T) {
		cfg := load(t, nil, nil)
		if cfg.ClassificationLow != nil || cfg.ClassificationHigh != nil {
			t.Errorf("thresholds = %v, %v; want both nil", cfg.ClassificationLow, cfg.ClassificationHigh)
		}
	})

	t.Run("from flags", func(t *testing.T) {
		cfg := load(t, []string{"-classification-threshold-low", "2", "-classification-threshold-high", "5"}, nil)
		if cfg.ClassificationLow == nil || *cfg.ClassificationLow != 2 {
			t.Errorf("low = %v, want 2", cfg.ClassificationLow)
		}
		if cfg.ClassificationHigh == nil || *cfg.ClassificationHigh != 5 {
			t.Errorf("high = %v, want 5", cfg.ClassificationHigh)
		}
	})

	t.Run("from the environment", func(t *testing.T) {
		cfg := load(t, nil, map[string]string{
			"WACKY_CLASSIFICATION_THRESHOLD_LOW":  "1",
			"WACKY_CLASSIFICATION_THRESHOLD_HIGH": "4",
		})
		if cfg.ClassificationLow == nil || *cfg.ClassificationLow != 1 {
			t.Errorf("low = %v, want 1", cfg.ClassificationLow)
		}
		if cfg.ClassificationHigh == nil || *cfg.ClassificationHigh != 4 {
			t.Errorf("high = %v, want 4", cfg.ClassificationHigh)
		}
	})

	t.Run("zero is set, not unset", func(t *testing.T) {
		cfg := load(t, []string{"-classification-threshold-low", "0"}, nil)
		if cfg.ClassificationLow == nil || *cfg.ClassificationLow != 0 {
			t.Errorf("low = %v, want a set zero", cfg.ClassificationLow)
		}
	})

	t.Run("one may be set alone", func(t *testing.T) {
		cfg := load(t, []string{"-classification-threshold-high", "5"}, nil)
		if cfg.ClassificationLow != nil {
			t.Errorf("low = %v, want nil", cfg.ClassificationLow)
		}
		if cfg.ClassificationHigh == nil {
			t.Error("high is nil, want 5")
		}
	})

	t.Run("negative values are allowed", func(t *testing.T) {
		cfg := load(t, []string{"-classification-threshold-low", "-3"}, nil)
		if cfg.ClassificationLow == nil || *cfg.ClassificationLow != -3 {
			t.Errorf("low = %v, want -3", cfg.ClassificationLow)
		}
	})

	rejected := [][]string{
		{"-classification-threshold-low", "high"},
		{"-classification-threshold-high", "3.5"},
		// An inverted range would make the low band unreachable.
		{"-classification-threshold-low", "9", "-classification-threshold-high", "2"},
	}
	for _, args := range rejected {
		if _, err := Load(append(args, dir), env(nil), io.Discard); err == nil {
			t.Errorf("Load(%v) succeeded, want an error", args)
		}
	}
}

// An owner that is unset, blank or whitespace falls back to the default; a
// real one is kept and trimmed.
func TestOwnerDefaults(t *testing.T) {
	dir := t.TempDir()

	tests := []struct {
		name string
		args []string
		env  map[string]string
		want string
	}{
		{"unset", []string{dir}, nil, DefaultOwner},
		{"blank flag", []string{"-owner", "", dir}, nil, DefaultOwner},
		{"whitespace flag", []string{"-owner", "   ", dir}, nil, DefaultOwner},
		{"blank environment", []string{dir}, map[string]string{"WACKY_OWNER": ""}, DefaultOwner},
		{"flag wins", []string{"-owner", "Acme Ltd", dir}, map[string]string{"WACKY_OWNER": "Env"}, "Acme Ltd"},
		{"from environment", []string{dir}, map[string]string{"WACKY_OWNER": "Env Owner"}, "Env Owner"},
		{"trimmed", []string{"-owner", "  Ada  ", dir}, nil, "Ada"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := Load(tt.args, env(tt.env), io.Discard)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if cfg.Owner != tt.want {
				t.Errorf("Owner = %q, want %q", cfg.Owner, tt.want)
			}
		})
	}
}

func TestFlagsBeatEnvironment(t *testing.T) {
	dir := t.TempDir()
	environment := env(map[string]string{
		"WACKY_ADDR":            ":9999",
		"WACKY_TITLE":           "From Env",
		"WACKY_RELOAD_INTERVAL": "1m",
		"WACKY_LOG_LEVEL":       "debug",
	})

	cfg, err := Load([]string{"-addr", ":7000", dir}, environment, io.Discard)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.Addr != ":7000" {
		t.Errorf("Addr = %q, want the flag value", cfg.Addr)
	}
	if cfg.Title != "From Env" {
		t.Errorf("Title = %q, want the environment value", cfg.Title)
	}
	if cfg.ReloadInterval != time.Minute {
		t.Errorf("ReloadInterval = %v, want 1m", cfg.ReloadInterval)
	}
	if cfg.LogLevel != slog.LevelDebug {
		t.Errorf("LogLevel = %v, want debug", cfg.LogLevel)
	}
}

// The same inputs must always produce the same configuration.
func TestLoadIsDeterministic(t *testing.T) {
	dir := t.TempDir()
	args := []string{"-addr", ":8081", "-title", "Docs", dir}
	environment := env(map[string]string{"WACKY_LOG_LEVEL": "warn"})

	first, err := Load(args, environment, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		got, err := Load(args, environment, io.Discard)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(first, got) {
			t.Fatalf("Load %d differed:\n%+v\n%+v", i, first, got)
		}
	}
}

func TestLoadRejectsBadInput(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "not-a-dir")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		args []string
		env  map[string]string
	}{
		{"missing directory", []string{filepath.Join(dir, "nope")}, nil},
		{"file instead of directory", []string{file}, nil},
		{"unknown log level", []string{"-log-level", "chatty", dir}, nil},
		{"empty address", []string{"-addr", "", dir}, nil},
		{"negative reload interval", []string{"-reload-interval", "-5s", dir}, nil},
		{"zero max file size", []string{"-max-file-size", "0", dir}, nil},
		{"bad duration in environment", []string{dir}, map[string]string{"WACKY_READ_TIMEOUT": "soon"}},
		{"too many arguments", []string{dir, dir}, nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Load(tt.args, env(tt.env), io.Discard); err == nil {
				t.Errorf("Load(%v) succeeded, want an error", tt.args)
			}
		})
	}
}

func TestHelpIsNotAnError(t *testing.T) {
	_, err := Load([]string{"-h"}, env(nil), io.Discard)
	if !errors.Is(err, flag.ErrHelp) {
		t.Errorf("Load(-h) = %v, want flag.ErrHelp", err)
	}
}
