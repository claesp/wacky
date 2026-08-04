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
	if cfg.Title != filepath.Base(cfg.RepoPath) {
		t.Errorf("Title = %q, want the repository directory name", cfg.Title)
	}
	if cfg.LogLevel != slog.LevelInfo {
		t.Errorf("LogLevel = %v, want info", cfg.LogLevel)
	}
}

func TestFlagsBeatEnvironment(t *testing.T) {
	dir := t.TempDir()
	environment := env(map[string]string{
		"WIKI_ADDR":            ":9999",
		"WIKI_TITLE":           "From Env",
		"WIKI_RELOAD_INTERVAL": "1m",
		"WIKI_LOG_LEVEL":       "debug",
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
	environment := env(map[string]string{"WIKI_LOG_LEVEL": "warn"})

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
		{"bad duration in environment", []string{dir}, map[string]string{"WIKI_READ_TIMEOUT": "soon"}},
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
