package server

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/claesp/wacky/internal/config"
	"github.com/claesp/wacky/internal/markdown"
	"github.com/claesp/wacky/internal/wacky"
)

func ptr(n int) *int { return &n }

func TestClassificationNotice(t *testing.T) {
	rated := func(name, level string) map[string]string {
		return map[string]string{"classification": name, "classification_level": level}
	}

	tests := []struct {
		name         string
		low, high    *int
		meta         map[string]string
		wantText     string
		wantSeverity string
	}{
		{
			name: "no thresholds, no notice even when rated",
			meta: rated("Secret", "9"),
		},
		{
			name: "no thresholds, no notice when unrated",
			meta: map[string]string{},
		},
		{
			name: "between the thresholds", low: ptr(2), high: ptr(5),
			meta:     rated("Internal", "3"),
			wantText: "This document has been classified as Internal", wantSeverity: classLow,
		},
		{
			name: "exactly at the low threshold", low: ptr(2), high: ptr(5),
			meta:     rated("Internal", "2"),
			wantText: "This document has been classified as Internal", wantSeverity: classLow,
		},
		{
			name: "exactly at the high threshold", low: ptr(2), high: ptr(5),
			meta:     rated("Secret", "5"),
			wantText: "This document has been classified as Secret", wantSeverity: classHigh,
		},
		{
			name: "above the high threshold", low: ptr(2), high: ptr(5),
			meta:     rated("Top Secret", "42"),
			wantText: "This document has been classified as Top Secret", wantSeverity: classHigh,
		},
		{
			// Classified, but below the level worth announcing.
			name: "below the low threshold", low: ptr(2), high: ptr(5),
			meta: rated("Public", "1"),
		},
		{
			name: "only the low threshold is set", low: ptr(2),
			meta:     rated("Internal", "99"),
			wantText: "This document has been classified as Internal", wantSeverity: classLow,
		},
		{
			name: "only the high threshold is set", high: ptr(5),
			meta:     rated("Internal", "1"),
			wantText: "This document has been classified as Internal", wantSeverity: classLow,
		},
		{
			name: "only the high threshold is set, and reached", high: ptr(5),
			meta:     rated("Secret", "5"),
			wantText: "This document has been classified as Secret", wantSeverity: classHigh,
		},
		{
			name: "missing both fields", low: ptr(2), high: ptr(5),
			meta:     map[string]string{},
			wantText: "This document is not yet rated", wantSeverity: classUnrated,
		},
		{
			name: "missing the level", low: ptr(2), high: ptr(5),
			meta:     map[string]string{"classification": "Internal"},
			wantText: "This document is not yet rated", wantSeverity: classUnrated,
		},
		{
			name: "missing the name", low: ptr(2), high: ptr(5),
			meta:     map[string]string{"classification_level": "3"},
			wantText: "This document is not yet rated", wantSeverity: classUnrated,
		},
		{
			name: "blank name", low: ptr(2), high: ptr(5),
			meta:     rated("   ", "3"),
			wantText: "This document is not yet rated", wantSeverity: classUnrated,
		},
		{
			name: "level is not a number", low: ptr(2), high: ptr(5),
			meta:     rated("Internal", "high"),
			wantText: "This document is not yet rated", wantSeverity: classUnrated,
		},
		{
			name: "a negative level still counts", low: ptr(-5), high: ptr(5),
			meta:     rated("Internal", "-1"),
			wantText: "This document has been classified as Internal", wantSeverity: classLow,
		},
		{
			name: "zero is a real threshold, not unset", low: ptr(0), high: ptr(5),
			meta:     rated("Internal", "0"),
			wantText: "This document has been classified as Internal", wantSeverity: classLow,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			text, severity := classificationNotice(tt.low, tt.high, tt.meta)
			if text != tt.wantText || severity != tt.wantSeverity {
				t.Errorf("classificationNotice() = (%q, %q), want (%q, %q)",
					text, severity, tt.wantText, tt.wantSeverity)
			}
		})
	}
}

// classifiedServer serves one page with the given front matter.
func classifiedServer(t *testing.T, frontMatter string, low, high *int) *Server {
	t.Helper()

	src := &fakeSource{files: map[string]string{
		"docs/secret.md": frontMatter + "# Secret Plans\n\nThe body text.\n",
	}}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	store := wacky.NewStore(src, markdown.New(), log)
	if err := store.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.ClassificationLow, cfg.ClassificationHigh = low, high

	srv, err := New(cfg, store, log)
	if err != nil {
		t.Fatal(err)
	}
	return srv
}

// banner returns the classification element of a rendered page, or "".
func banner(t *testing.T, body string) string {
	t.Helper()
	start := strings.Index(body, `<div class="classification`)
	if start < 0 {
		return ""
	}
	return body[start : start+strings.Index(body[start:], "</div>")]
}

func TestClassificationBanner(t *testing.T) {
	const rated = "---\nclassification: Internal Use Only\nclassification_level: 3\n---\n\n"

	t.Run("shown between the thresholds", func(t *testing.T) {
		srv := classifiedServer(t, rated, ptr(2), ptr(5))
		body := get(t, srv, "/wacky/docs/secret").Body.String()

		got := banner(t, body)
		if !strings.Contains(got, "classification low") {
			t.Errorf("banner is not the low severity: %q", got)
		}
		if !strings.Contains(got, "This document has been classified as Internal Use Only") {
			t.Errorf("banner text is wrong: %q", got)
		}
		// It belongs below the top bar and above the content.
		header := strings.Index(body, "</header>")
		shell := strings.Index(body, `<div class="shell">`)
		at := strings.Index(body, `<div class="classification`)
		if !(header < at && at < shell) {
			t.Errorf("banner is not between the header and the content: header=%d banner=%d shell=%d", header, at, shell)
		}
	})

	t.Run("severe above the high threshold", func(t *testing.T) {
		srv := classifiedServer(t, "---\nclassification: Top Secret\nclassification_level: 9\n---\n\n", ptr(2), ptr(5))
		got := banner(t, get(t, srv, "/wacky/docs/secret").Body.String())

		if !strings.Contains(got, "classification high") {
			t.Errorf("banner is not the high severity: %q", got)
		}
		if !strings.Contains(got, "classified as Top Secret") {
			t.Errorf("banner text is wrong: %q", got)
		}
	})

	t.Run("unrated when the front matter is absent", func(t *testing.T) {
		srv := classifiedServer(t, "", ptr(2), ptr(5))
		got := banner(t, get(t, srv, "/wacky/docs/secret").Body.String())

		if !strings.Contains(got, "classification unrated") || !strings.Contains(got, "not yet rated") {
			t.Errorf("banner is not the unrated one: %q", got)
		}
	})

	t.Run("silent when no threshold is configured", func(t *testing.T) {
		srv := classifiedServer(t, rated, nil, nil)
		body := get(t, srv, "/wacky/docs/secret").Body.String()

		if got := banner(t, body); got != "" {
			t.Errorf("a banner appeared with no thresholds set: %q", got)
		}
		// The front matter still stays out of the rendered text.
		if strings.Contains(body, "classification_level") {
			t.Error("front matter leaked into the page")
		}
	})

	t.Run("front matter cannot inject markup", func(t *testing.T) {
		srv := classifiedServer(t,
			"---\nclassification: \"<script>alert(1)</script>\"\nclassification_level: 3\n---\n\n",
			ptr(2), ptr(5))
		got := banner(t, get(t, srv, "/wacky/docs/secret").Body.String())

		if strings.Contains(got, "<script") {
			t.Errorf("the banner emitted raw markup: %q", got)
		}
		if !strings.Contains(got, "&lt;script&gt;") {
			t.Errorf("the classification was not escaped: %q", got)
		}
	})

	// The stylesheet has to carry all three severities.
	t.Run("every severity is styled", func(t *testing.T) {
		css := get(t, newTestServer(t), "/static/style.css").Body.String()
		for _, want := range []string{".classification.unrated", ".classification.low", ".classification.high"} {
			if !strings.Contains(css, want) {
				t.Errorf("the stylesheet is missing %q", want)
			}
		}
	})
}
