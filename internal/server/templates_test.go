package server

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/claesp/wacky/internal/config"
)

func TestHumanizeAge(t *testing.T) {
	const day = 24 * time.Hour

	tests := []struct {
		age  time.Duration
		want string
	}{
		// Under a minute the age is counted in seconds.
		{0, "0 seconds ago"},
		{1 * time.Second, "1 second ago"},
		{45 * time.Second, "45 seconds ago"},
		{59*time.Second + 999*time.Millisecond, "59 seconds ago"},

		{time.Minute, "1 minute ago"},
		{90 * time.Second, "1 minute ago"},
		{2 * time.Minute, "2 minutes ago"},
		{59 * time.Minute, "59 minutes ago"},

		{time.Hour, "1 hour ago"},
		{3*time.Hour + 30*time.Minute, "3 hours ago"},
		{23 * time.Hour, "23 hours ago"},

		{day, "1 day ago"},
		{3 * day, "3 days ago"},
		{29 * day, "29 days ago"},

		{30 * day, "1 month ago"},
		{90 * day, "3 months ago"},

		{365 * day, "1 year ago"},
		{800 * day, "2 years ago"},

		// A clock that moved backwards must not read as a negative age.
		{-5 * time.Second, "0 seconds ago"},
	}

	for _, tt := range tests {
		if got := humanizeAge(tt.age); got != tt.want {
			t.Errorf("humanizeAge(%s) = %q, want %q", tt.age, got, tt.want)
		}
	}
}

func TestRelativeTime(t *testing.T) {
	if got := relativeTime(time.Now().Add(-2 * time.Hour)); got != "2 hours ago" {
		t.Errorf("relativeTime(2h ago) = %q, want %q", got, "2 hours ago")
	}
	if got := relativeTime(time.Time{}); got != "never" {
		t.Errorf("relativeTime(zero) = %q, want %q", got, "never")
	}
}

func TestCopyrightNotice(t *testing.T) {
	tests := []struct {
		name                   string
		owner                  string
		firstYear, currentYear int
		want                   string
	}{
		{"a span of years", "Acme Ltd", 2019, 2026, "Copyright © Acme Ltd, 2019-2026"},
		{"a single year", "Acme Ltd", 2026, 2026, "Copyright © Acme Ltd, 2026"},
		{"no owner, no notice", "", 2019, 2026, ""},
		{"blank owner, no notice", "   ", 2019, 2026, ""},
		{"owner is trimmed", "  Ada  ", 2026, 2026, "Copyright © Ada, 2026"},
		// A repository without history has no year to start from.
		{"no first commit", "Ada", 0, 2026, "Copyright © Ada, 2026"},
		// A commit dated in the future must not invert the range.
		{"first commit in the future", "Ada", 2030, 2026, "Copyright © Ada, 2026"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := copyrightNotice(tt.owner, tt.firstYear, tt.currentYear); got != tt.want {
				t.Errorf("copyrightNotice(%q, %d, %d) = %q, want %q",
					tt.owner, tt.firstYear, tt.currentYear, got, tt.want)
			}
		})
	}
}

// footerOf returns the site footer of a rendered page.
func footerOf(t *testing.T, body string) string {
	t.Helper()
	start := strings.Index(body, `<footer class="footer">`)
	if start < 0 {
		t.Fatalf("no site footer in:\n%s", body)
	}
	return body[start : start+strings.Index(body[start:], "</footer>")]
}

// The footer carries the index age and, when an owner is set, the copyright —
// and nothing else: a repository's commit count and subjects say nothing about
// the pages this server renders.
func TestFooterShowsOnlyIndexAge(t *testing.T) {
	footerHTML := footerOf(t, get(t, newTestServer(t), "/wacky/docs/setup").Body.String())

	if !strings.Contains(footerHTML, "indexed ") || !strings.Contains(footerHTML, " ago") {
		t.Errorf("footer does not show a relative index time:\n%s", footerHTML)
	}
	// The absolute time stays available on hover.
	if !strings.Contains(footerHTML, "title=") {
		t.Errorf("footer lost the exact timestamp:\n%s", footerHTML)
	}
	for _, gone := range []string{"pages", "write the docs", "0123456789"} {
		if strings.Contains(footerHTML, gone) {
			t.Errorf("footer still shows %q:\n%s", gone, footerHTML)
		}
	}
}

// With a commit URL configured, every hash the site shows becomes a link to
// the hosting site; without one they stay plain text.
func TestCommitHashesLinkToTheHost(t *testing.T) {
	const base = "https://github.com/org/repo/commit/"
	const hash = "0123456789abcdef" // the fake source's commit

	t.Run("linked", func(t *testing.T) {
		srv := newTestServer(t)
		srv.cfg.CommitURL = base

		// The page footer and every row of the history table.
		for _, target := range []string{"/wacky/docs/setup", "/history/docs/setup.md"} {
			body := get(t, srv, target).Body.String()
			want := `<a class="commit" href="` + base + hash + `" rel="noopener noreferrer" title="` + hash + `"><code>01234567</code></a>`
			if !strings.Contains(body, want) {
				t.Errorf("GET %s does not link the hash:\n%s", target, body)
			}
		}
	})

	t.Run("not linked", func(t *testing.T) {
		srv := newTestServer(t) // no commit URL
		body := get(t, srv, "/wacky/docs/setup").Body.String()

		if strings.Contains(body, `class="commit"`) {
			t.Error("hash was linked without a commit URL configured")
		}
		if !strings.Contains(body, `<code title="`+hash+`">01234567</code>`) {
			t.Errorf("hash is not shown as plain text:\n%s", body)
		}
	})
}

// The notice spans the first commit's year to this one, and names whoever the
// owner setting says — falling back to the default when none is configured.
func TestFooterCopyright(t *testing.T) {
	thisYear := strconv.Itoa(time.Now().Year())

	tests := []struct {
		name, owner, want string
	}{
		// The fake source dates its first commit to 2019.
		{"configured owner", "Acme Ltd", "Copyright © Acme Ltd, 2019-" + thisYear},
		{"default owner", config.DefaultOwner, "Copyright © The Authors, 2019-" + thisYear},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newTestServer(t)
			srv.cfg.Owner = tt.owner

			footerHTML := footerOf(t, get(t, srv, "/wacky/docs/setup").Body.String())
			if !strings.Contains(footerHTML, tt.want) {
				t.Errorf("footer is missing %q:\n%s", tt.want, footerHTML)
			}
		})
	}
}
