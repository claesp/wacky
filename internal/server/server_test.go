package server

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/claesp/wacky/internal/config"
	"github.com/claesp/wacky/internal/git"
	"github.com/claesp/wacky/internal/markdown"
	"github.com/claesp/wacky/internal/wacky"
)

// fakeSource satisfies wacky.Source without touching a real repository.
type fakeSource struct {
	files map[string]string
	// commits is how much history every file has. Zero means one commit.
	commits int
}

func (f *fakeSource) Root() string { return "/fake/repo" }
func (f *fakeSource) Ref() string  { return "" }

func (f *fakeSource) Files(context.Context) ([]git.File, error) {
	paths := make([]string, 0, len(f.files))
	for p := range f.files {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	out := make([]git.File, 0, len(paths))
	// Each file is a minute newer than the one before, so ordering by time is
	// distinguishable from ordering by path.
	for i, p := range paths {
		out = append(out, git.File{
			Path:    p,
			Size:    int64(len(f.files[p])),
			ModTime: time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC).Add(time.Duration(i) * time.Minute),
		})
	}
	return out, nil
}

func (f *fakeSource) Read(_ context.Context, rel string) ([]byte, error) {
	content, ok := f.files[rel]
	if !ok {
		return nil, git.ErrNotFound
	}
	return []byte(content), nil
}

// Log honours the limit the caller asks for, so the truncation notice can be
// exercised the same way the real repository would drive it.
func (f *fakeSource) Log(_ context.Context, _ string, limit int) ([]git.Commit, error) {
	total := f.commits
	if total < 1 {
		total = 1
	}
	if limit > 0 && limit < total {
		total = limit
	}

	out := make([]git.Commit, 0, total)
	for i := 0; i < total; i++ {
		c := git.Commit{
			Hash:    fmt.Sprintf("%016d", i),
			Author:  "Ada",
			Email:   "ada@example.com",
			When:    time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC),
			Subject: fmt.Sprintf("edit number %d", i),
		}
		// The newest commit keeps fixed values the other tests assert on.
		if i == 0 {
			c.Hash, c.Subject = "0123456789abcdef", "write the docs"
		}
		out = append(out, c)
	}
	return out, nil
}

func (f *fakeSource) Head(context.Context) (git.Commit, error) {
	return git.Commit{Hash: "0123456789abcdef", Subject: "write the docs"}, nil
}

func (f *fakeSource) FirstCommit(context.Context) (git.Commit, error) {
	return git.Commit{
		Hash:    "fedcba9876543210",
		Subject: "initial import",
		When:    time.Date(2019, 5, 4, 3, 2, 1, 0, time.UTC),
	}, nil
}

func newTestServer(t *testing.T) *Server {
	t.Helper()

	src := &fakeSource{files: map[string]string{
		"README.md":          "# Handbook\n\nStart at [setup](docs/setup.md).\n",
		"docs/setup.md":      "# Setup\n\nRun the installer. Contact <a@example.com>.\n",
		"docs/diagram.png":   "\x89PNG fake",
		"guides/deep/adv.md": "# Advanced\n\nDetails about scaling.\n",
		"LICENSE":            "MIT",
	}}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	store := wacky.NewStore(src, markdown.New(), log)
	if err := store.Reload(context.Background()); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	cfg := config.Default()
	cfg.BrandTitle = "Test Wiki"
	cfg.RepoPath = "/fake/repo"

	srv, err := New(cfg, store, log)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return srv
}

// The header shows a logo when one is configured, and the title text when not.
func TestBrandHeader(t *testing.T) {
	const png = "data:image/png;base64,iVBORw0KGgo="

	t.Run("title text by default", func(t *testing.T) {
		body := get(t, newTestServer(t), "/").Body.String()

		if !strings.Contains(body, `<a class="brand" href="/">Test Wiki</a>`) {
			t.Errorf("header does not show the title text:\n%s", header(t, body))
		}
	})

	t.Run("an image replaces the text", func(t *testing.T) {
		srv := newTestServer(t)
		srv.cfg.BrandImageURL = "/static/logo.png"

		head := header(t, get(t, srv, "/").Body.String())
		if !strings.Contains(head, `<img src="/static/logo.png" alt="Test Wiki">`) {
			t.Errorf("header does not show the logo:\n%s", head)
		}
		// The title survives as the alt text, not as visible text.
		if strings.Contains(head, `href="/">Test Wiki`) {
			t.Errorf("the title text is still rendered alongside the logo:\n%s", head)
		}
	})

	t.Run("inline data wins over a URL", func(t *testing.T) {
		srv := newTestServer(t)
		srv.cfg.BrandImageURL = "/static/logo.png"
		srv.cfg.BrandImageData = png

		head := header(t, get(t, srv, "/").Body.String())
		if !strings.Contains(head, `src="`+png+`"`) {
			t.Errorf("header does not use the inline image:\n%s", head)
		}
		if strings.Contains(head, "/static/logo.png") {
			t.Errorf("header still references the URL:\n%s", head)
		}
	})
}

// header returns the page's <header> element.
func header(t *testing.T, body string) string {
	t.Helper()
	start := strings.Index(body, "<header")
	if start < 0 {
		t.Fatal("no header in the page")
	}
	return body[start : start+strings.Index(body[start:], "</header>")]
}

// newTestServerWithBrand builds a server whose assets were generated from the
// given brand colour.
func newTestServerWithBrand(t *testing.T, color string) *Server {
	t.Helper()

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	store := wacky.NewStore(&fakeSource{files: map[string]string{"README.md": "# Handbook\n"}}, markdown.New(), log)
	if err := store.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.BrandColor = color

	srv, err := New(cfg, store, log)
	if err != nil {
		t.Fatal(err)
	}
	return srv
}

func get(t *testing.T, srv *Server, target string, headers ...[2]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	for _, h := range headers {
		req.Header.Set(h[0], h[1])
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

func TestRoutes(t *testing.T) {
	srv := newTestServer(t)

	tests := []struct {
		name       string
		target     string
		wantStatus int
		wantBody   []string
	}{
		{"home", "/", http.StatusOK, []string{"Handbook", `href="/wacky/docs/setup"`}},
		{"page", "/wacky/docs/setup", http.StatusOK, []string{"<h1 id=\"setup\">Setup", "Run the installer"}},
		{"nested page", "/wacky/guides/deep/adv", http.StatusOK, []string{"Advanced"}},
		{"page list", "/pages", http.StatusOK, []string{"Pages", "docs/setup.md"}},
		{"search hit", "/search?q=installer", http.StatusOK, []string{"<mark>installer</mark>", "Setup"}},
		{"search miss", "/search?q=zzzznothing", http.StatusOK, []string{"Nothing matched"}},
		{"history", "/history/docs/setup", http.StatusOK, []string{"write the docs", "Ada"}},
		{"directory listing", "/wacky/guides", http.StatusOK, []string{"no index page", "Advanced"}},
		{"missing page", "/wacky/nope", http.StatusNotFound, []string{"There is no page at nope"}},
		{"unknown route", "/nothing/here", http.StatusNotFound, []string{"404"}},
		{"stylesheet", "/static/style.css", http.StatusOK, []string{"--accent"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := get(t, srv, tt.target)
			if rec.Code != tt.wantStatus {
				t.Fatalf("GET %s = %d, want %d\n%s", tt.target, rec.Code, tt.wantStatus, rec.Body.String())
			}
			body := rec.Body.String()
			for _, want := range tt.wantBody {
				if !strings.Contains(body, want) {
					t.Errorf("GET %s: body missing %q\n%s", tt.target, want, body)
				}
			}
		})
	}
}

// The page list is Title / Path / Modified, newest first, with the relative
// times used everywhere else on the site.
func TestPageListTable(t *testing.T) {
	body := get(t, newTestServer(t), "/pages").Body.String()

	if !strings.Contains(body, "<tr><th>Title</th><th>Path</th><th>Modified</th></tr>") {
		t.Errorf("unexpected table header:\n%s", body)
	}
	for _, gone := range []string{"<th>Size</th>", "Markdown files in"} {
		if strings.Contains(body, gone) {
			t.Errorf("the page list still shows %q", gone)
		}
	}
	if !strings.Contains(body, " ago</td>") {
		t.Errorf("Modified is not a relative time:\n%s", body)
	}
	// The exact time stays on hover.
	if !strings.Contains(body, `<td title="20`) {
		t.Errorf("Modified lost its exact timestamp:\n%s", body)
	}

	// The fake source dates each file a minute after the previous one in path
	// order, so the last path must be listed first.
	rows := strings.Split(body, "<tr>")
	var order []string
	for _, row := range rows {
		if i := strings.Index(row, "<code>"); i >= 0 {
			order = append(order, row[i+len("<code>"):i+len("<code>")+strings.Index(row[i+len("<code>"):], "</code>")])
		}
	}
	want := []string{"guides/deep/adv.md", "docs/setup.md", "README.md"}
	if !reflect.DeepEqual(order, want) {
		t.Errorf("row order = %v, want %v", order, want)
	}
}

func TestPageRedirects(t *testing.T) {
	srv := newTestServer(t)

	tests := []struct{ target, location string }{
		{"/wacky/docs/setup/", "/wacky/docs/setup"},
		{"/wacky/docs/setup.md", "/wacky/docs/setup"},
		{"/wacky/docs/diagram.png", "/raw/docs/diagram.png"},
	}
	for _, tt := range tests {
		rec := get(t, srv, tt.target)
		if rec.Code != http.StatusMovedPermanently {
			t.Errorf("GET %s = %d, want 301", tt.target, rec.Code)
			continue
		}
		if got := rec.Header().Get("Location"); got != tt.location {
			t.Errorf("GET %s redirected to %q, want %q", tt.target, got, tt.location)
		}
	}
}

func TestRawServing(t *testing.T) {
	srv := newTestServer(t)

	rec := get(t, srv, "/raw/docs/diagram.png")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/png" {
		t.Errorf("Content-Type = %q, want image/png", ct)
	}
	if cd := rec.Header().Get("Content-Disposition"); cd != "" {
		t.Errorf("image was offered as a download: %q", cd)
	}

	// An unknown type must never be rendered inline by the browser.
	rec = get(t, srv, "/raw/LICENSE")
	if ct := rec.Header().Get("Content-Type"); ct != "application/octet-stream" {
		t.Errorf("Content-Type = %q, want application/octet-stream", ct)
	}
	if !strings.HasPrefix(rec.Header().Get("Content-Disposition"), "attachment") {
		t.Error("unknown file type was not sent as an attachment")
	}

	rec = get(t, srv, "/raw/docs/setup.md")
	if !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/plain") {
		t.Errorf("Markdown source served as %q", rec.Header().Get("Content-Type"))
	}
	if !strings.Contains(rec.Body.String(), "# Setup") {
		t.Error("raw Markdown was not returned verbatim")
	}
}

// Repeating a GET must be free of side effects and cheap: the second request
// with the returned ETag is answered with 304.
func TestConditionalGet(t *testing.T) {
	srv := newTestServer(t)

	first := get(t, srv, "/wacky/docs/setup")
	etag := first.Header().Get("ETag")
	if etag == "" {
		t.Fatal("no ETag on a page response")
	}

	second := get(t, srv, "/wacky/docs/setup", [2]string{"If-None-Match", etag})
	if second.Code != http.StatusNotModified {
		t.Errorf("conditional GET = %d, want 304", second.Code)
	}
	if second.Body.Len() != 0 {
		t.Errorf("304 response had a body of %d bytes", second.Body.Len())
	}

	// Same request, same answer.
	third := get(t, srv, "/wacky/docs/setup")
	if third.Body.String() != first.Body.String() {
		t.Error("repeating a GET produced a different page")
	}
}

// The sidebar carries a Home link, and the menu that reveals it on a phone is
// a checkbox: the Content-Security-Policy forbids JavaScript, so the toggle
// has to work in CSS alone.
func TestSidebarNavigation(t *testing.T) {
	srv := newTestServer(t)

	body := get(t, srv, "/wacky/docs/setup").Body.String()
	for _, want := range []string{
		`<li><a href="/">Home</a></li>`,
		`<li class="nav-divide"><a href="/pages">Pages</a></li>`,
		`<input class="menu-toggle visually-hidden" type="checkbox" id="menu-toggle">`,
		`<label class="menu-button" for="menu-toggle">`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page is missing %q", want)
		}
	}

	// The page list is reached from the sidebar only; the header carries the
	// brand and the search form.
	headerHTML := body[strings.Index(body, "<header"):strings.Index(body, "</header>")]
	if strings.Contains(headerHTML, "/pages") {
		t.Errorf("header still links to the page list:\n%s", headerHTML)
	}

	// The search form has no submit button, so it relies on implicit
	// submission: that only works while it holds exactly one field.
	if strings.Contains(headerHTML, "<button") {
		t.Errorf("header regained a button:\n%s", headerHTML)
	}
	if n := strings.Count(headerHTML, "<input"); n != 1 {
		t.Errorf("search form has %d inputs, want exactly 1 so Enter submits it:\n%s", n, headerHTML)
	}

	// Home, then Pages, then the repository tree below the rule.
	home := strings.Index(body, `>Home</a>`)
	pages := strings.Index(body, `class="nav-divide"`)
	tree := strings.Index(body, `<span class="nav-dir">Docs</span>`)
	if !(home < pages && pages < tree) {
		t.Errorf("sidebar order is home=%d pages=%d tree=%d, want that order", home, pages, tree)
	}
	if strings.Contains(body, "<script") {
		t.Error("the menu introduced a script tag, which the CSP forbids")
	}

	// The toggle must precede the header and the shell, or the sibling
	// combinator that opens the sidebar cannot reach them.
	toggle := strings.Index(body, `id="menu-toggle"`)
	header := strings.Index(body, `<header class="topbar">`)
	shell := strings.Index(body, `<div class="shell">`)
	if !(toggle < header && header < shell) {
		t.Errorf("markup order is toggle=%d header=%d shell=%d, want toggle first", toggle, header, shell)
	}
}

// The "no Markdown files" note belongs to a repository with no pages at all.
// A root README leaves the navigation tree empty without leaving the wiki
// empty, and must not trigger it.
func TestEmptyNavigationNote(t *testing.T) {
	const note = "This repository contains no Markdown files"

	serve := func(t *testing.T, files map[string]string) *Server {
		t.Helper()
		log := slog.New(slog.NewTextHandler(io.Discard, nil))
		store := wacky.NewStore(&fakeSource{files: files}, markdown.New(), log)
		if err := store.Reload(context.Background()); err != nil {
			t.Fatal(err)
		}
		srv, err := New(config.Default(), store, log)
		if err != nil {
			t.Fatal(err)
		}
		return srv
	}

	t.Run("only a root README", func(t *testing.T) {
		body := get(t, serve(t, map[string]string{"README.md": "# Handbook\n"}), "/").Body.String()

		if strings.Contains(body, note) {
			t.Errorf("a repository with a page claims to have none:\n%s", body)
		}
		if !strings.Contains(body, "Handbook") {
			t.Error("the page was not rendered")
		}
		// With no tree below it, the rule under Pages would separate nothing.
		if strings.Contains(body, "nav-divide") {
			t.Errorf("the sidebar draws a rule with nothing below it:\n%s", body)
		}
	})

	t.Run("no Markdown at all", func(t *testing.T) {
		// No home page, so "/" redirects to the page list.
		body := get(t, serve(t, map[string]string{"LICENSE": "MIT"}), "/pages").Body.String()

		if !strings.Contains(body, note) {
			t.Errorf("an empty repository does not say so:\n%s", body)
		}
	})
}

// The current entry is marked for assistive technology, not just visually.
func TestNavigationMarksCurrentPage(t *testing.T) {
	tests := []struct {
		target, current string
	}{
		{"/", `<a href="/" class="active" aria-current="page">Home</a>`},
		{"/pages", `<a href="/pages" class="active" aria-current="page">Pages</a>`},
	}
	srv := newTestServer(t)
	for _, tt := range tests {
		body := get(t, srv, tt.target).Body.String()
		if !strings.Contains(body, tt.current) {
			t.Errorf("GET %s did not mark its own entry as current", tt.target)
		}
	}

	// A wiki page must not mark Home as current.
	body := get(t, srv, "/wacky/docs/setup").Body.String()
	if strings.Contains(body, `<a href="/" class="active"`) {
		t.Error("Home is marked current while a wiki page is shown")
	}
}

// Every page starts its trail with Home, including Home itself and the page
// list, which has no place in the repository tree.
func TestBreadcrumbsOnEveryPage(t *testing.T) {
	srv := newTestServer(t)

	tests := []struct {
		name, target string
		want         []string
	}{
		{
			name:   "home points at itself",
			target: "/",
			want:   []string{`<a href="/" aria-current="page">Home</a>`},
		},
		{
			name:   "page list sits below home",
			target: "/pages",
			want: []string{
				`<a href="/">Home</a>`,
				`<a href="/pages" aria-current="page">Pages</a>`,
			},
		},
		{
			name:   "search results sit below home and repeat the query",
			target: "/search?q=installer",
			want: []string{
				`<a href="/">Home</a>`,
				`<a href="/search?q=installer" aria-current="page">Search</a>`,
			},
		},
		{
			name:   "search without a query keeps a bare crumb",
			target: "/search",
			want:   []string{`<a href="/search" aria-current="page">Search</a>`},
		},
		{
			name:   "wiki page marks its last crumb",
			target: "/wacky/docs/setup",
			want: []string{
				`<a href="/">Home</a>`,
				`<a href="/wacky/docs">Docs</a>`,
				`<a href="/wacky/docs/setup" aria-current="page">Setup</a>`,
			},
		},
		{
			name:   "directory listing",
			target: "/wacky/guides",
			want:   []string{`<a href="/">Home</a>`, `<a href="/wacky/guides" aria-current="page">`},
		},
		{
			name:   "history is not one of its own crumbs",
			target: "/history/docs/setup",
			want:   []string{`<a href="/">Home</a>`, `<a href="/wacky/docs/setup">Setup</a>`},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := get(t, srv, tt.target).Body.String()
			if !strings.Contains(body, `<nav class="breadcrumbs" aria-label="Breadcrumb">`) {
				t.Fatalf("GET %s has no breadcrumb trail", tt.target)
			}
			for _, want := range tt.want {
				if !strings.Contains(body, want) {
					t.Errorf("GET %s: trail missing %q", tt.target, want)
				}
			}
			if n := strings.Count(body, `aria-current="page"`); n > 2 {
				t.Errorf("GET %s marks %d elements as current, want at most 2 (nav entry + crumb)", tt.target, n)
			}
		})
	}
}

// Following the search crumb must reproduce the same results, which means the
// query has to survive escaping intact.
func TestSearchCrumbRepeatsTheQuery(t *testing.T) {
	srv := newTestServer(t)

	queries := []string{"installer", "run the installer", "a&b", "100% sure", "a+b", "<script>"}
	for _, query := range queries {
		target := "/search?q=" + url.QueryEscape(query)
		body := get(t, srv, target).Body.String()

		href := crumbHref(t, body, "Search")
		if href == "" {
			t.Errorf("q=%q: no search crumb", query)
			continue
		}
		// The crumb is a link a browser will follow, so parse it the way one
		// would and check the query survived the round trip.
		parsed, err := url.Parse(html.UnescapeString(href))
		if err != nil {
			t.Errorf("q=%q: crumb href %q does not parse: %v", query, href, err)
			continue
		}
		if got := parsed.Query().Get("q"); got != query {
			t.Errorf("crumb href %q carries q=%q, want %q", href, got, query)
			continue
		}
		// And following it returns the same page.
		if repeat := get(t, srv, parsed.String()).Body.String(); repeat != body {
			t.Errorf("q=%q: following the crumb produced a different page", query)
		}
	}
}

// crumbHref extracts the href of the breadcrumb whose label is name.
func crumbHref(t *testing.T, body, name string) string {
	t.Helper()
	nav := body[strings.Index(body, `<nav class="breadcrumbs"`):]
	nav = nav[:strings.Index(nav, "</nav>")]

	marker := `>` + name + `</a>`
	end := strings.Index(nav, marker)
	if end < 0 {
		return ""
	}
	tag := nav[:end]
	open := strings.LastIndex(tag, `<a href="`)
	if open < 0 {
		return ""
	}
	tag = tag[open+len(`<a href="`):]
	quote := strings.IndexByte(tag, '"')
	if quote < 0 {
		return ""
	}
	return tag[:quote]
}

// A page and its history share one site footer: the last change, a link to the
// source, a self-referencing history link and the repository path. The history
// view's own RepoPath field must not be mistaken for the request path the
// breadcrumb trail compares against.
func TestHistoryFooterMatchesAPage(t *testing.T) {
	srv := newTestServer(t)

	pageFooter := footerOf(t, get(t, srv, "/wacky/docs/setup").Body.String())
	histFooter := footerOf(t, get(t, srv, "/history/docs/setup.md").Body.String())

	if pageFooter != histFooter {
		t.Errorf("footers differ:\npage:    %s\nhistory: %s", pageFooter, histFooter)
	}
	for _, want := range []string{
		`<a href="/raw/docs/setup.md">Source</a>`,
		`<a href="/history/docs/setup.md">History</a>`,
		`<span class="muted" title="Repository path">docs/setup.md</span>`,
		"Last changed 2026-03-04 by Ada",
	} {
		if !strings.Contains(histFooter, want) {
			t.Errorf("history footer is missing %q:\n%s", want, histFooter)
		}
	}

	// None of it is left in the article body.
	body := get(t, srv, "/history/docs/setup.md").Body.String()
	article := body[strings.Index(body, "<article"):strings.Index(body, "</article>")]
	for _, gone := range []string{"back to the page", "docs/setup.md", "Last changed"} {
		if strings.Contains(article, gone) {
			t.Errorf("the article still holds %q:\n%s", gone, article)
		}
	}
}

// A history link names the source file, so the home page — whose slug is empty
// — links to its own file rather than to a bare "/history/".
func TestHistoryLinksNameTheSourceFile(t *testing.T) {
	srv := newTestServer(t)

	tests := []struct{ page, wantLink string }{
		{"/", `<a href="/history/README.md">History</a>`},
		{"/wacky/docs/setup", `<a href="/history/docs/setup.md">History</a>`},
	}
	for _, tt := range tests {
		body := get(t, srv, tt.page).Body.String()
		if !strings.Contains(body, tt.wantLink) {
			t.Errorf("GET %s does not link to %s", tt.page, tt.wantLink)
		}
	}
}

// Following the home page's history link shows the index file's commits.
func TestHomePageHistory(t *testing.T) {
	rec := get(t, newTestServer(t), "/history/README.md")

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /history/README.md = %d, want 200\n%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		"write the docs", // the commit subject from the fake source
		// The footer names the file and links back to its source.
		`<a href="/raw/README.md">Source</a>`,
		`<span class="muted" title="Repository path">README.md</span>`,
		// Breadcrumbs follow the page's slug, not the file name, and the
		// history view is not itself one of its crumbs.
		`<a href="/">Home</a>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("GET /history/README.md is missing %q", want)
		}
	}
}

// A history URL with no file names nothing to show. ("/history/." never
// reaches the handler: the mux normalises the path and redirects first.)
func TestHistoryWithoutAFile(t *testing.T) {
	rec := get(t, newTestServer(t), "/history/")

	if rec.Code != http.StatusNotFound {
		t.Errorf("GET /history/ = %d, want 404", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "No file was given") {
		t.Errorf("GET /history/: unexpected message:\n%s", rec.Body.String())
	}
}

// The history page is headed by the page's own title, with History below it.
func TestHistoryHeadings(t *testing.T) {
	srv := newTestServer(t)

	tests := []struct{ target, wantH1 string }{
		{"/history/README.md", "<h1>Handbook</h1>"},           // the page's title
		{"/history/docs/setup.md", "<h1>Setup</h1>"},          // ditto
		{"/history/docs/diagram.png", "<h1>diagram.png</h1>"}, // not a page: file name
	}
	for _, tt := range tests {
		body := get(t, srv, tt.target).Body.String()
		if !strings.Contains(body, tt.wantH1) {
			t.Errorf("GET %s: missing %q", tt.target, tt.wantH1)
		}
		if !strings.Contains(body, "<h2>History</h2>") {
			t.Errorf("GET %s: missing the History subheading", tt.target)
		}
		if strings.Contains(body, "<h1>History</h1>") {
			t.Errorf("GET %s: History is still the top-level heading", tt.target)
		}
	}
}

// The subject carries the commit link, so the separate Commit column is gone
// and the full hash lives in the title either way.
func TestHistoryTableLinksTheSubject(t *testing.T) {
	const base = "https://github.com/org/repo/commit/"
	const hash = "0123456789abcdef"

	srv := newTestServer(t)
	srv.cfg.CommitURL = base
	body := get(t, srv, "/history/docs/setup.md").Body.String()

	want := `<a class="commit" href="` + base + hash + `" rel="noopener noreferrer" title="` + hash + `">write the docs</a>`
	if !strings.Contains(body, want) {
		t.Errorf("the subject is not a commit link:\n%s", body)
	}
	if strings.Contains(body, "<th>Commit</th>") {
		t.Error("the Commit column is still there")
	}
	if !strings.Contains(body, "<tr><th>When</th><th>Author</th><th>Subject</th></tr>") {
		t.Errorf("unexpected table header:\n%s", body)
	}

	// Without a commit URL the subject stays plain, but keeps the hash.
	plain := get(t, newTestServer(t), "/history/docs/setup.md").Body.String()
	if !strings.Contains(plain, `<span title="`+hash+`">write the docs</span>`) {
		t.Errorf("the unlinked subject lost its hash:\n%s", plain)
	}
}

// A file with more history than the limit lists the limit and says so.
func TestHistoryTruncationNotice(t *testing.T) {
	newServer := func(t *testing.T, commits, limit int) *Server {
		t.Helper()
		src := &fakeSource{
			files:   map[string]string{"README.md": "# Handbook\n"},
			commits: commits,
		}
		log := slog.New(slog.NewTextHandler(io.Discard, nil))
		store := wacky.NewStore(src, markdown.New(), log)
		if err := store.Reload(context.Background()); err != nil {
			t.Fatal(err)
		}
		cfg := config.Default()
		cfg.HistoryLimit = limit

		srv, err := New(cfg, store, log)
		if err != nil {
			t.Fatal(err)
		}
		return srv
	}

	t.Run("more commits than the limit", func(t *testing.T) {
		body := get(t, newServer(t, 50, 5), "/history/README.md").Body.String()

		if !strings.Contains(body, "Only showing the latest 5 commits") {
			t.Errorf("no truncation notice:\n%s", body)
		}
		if got := strings.Count(body, "<tr>") - 2; got != 5 { // header and notice rows
			t.Errorf("listed %d commits, want 5", got)
		}
		// The extra commit fetched to detect truncation must not be listed.
		if strings.Contains(body, "edit number 5") {
			t.Error("the look-ahead commit was rendered")
		}
	})

	t.Run("exactly the limit", func(t *testing.T) {
		body := get(t, newServer(t, 5, 5), "/history/README.md").Body.String()

		if strings.Contains(body, "Only showing") {
			t.Errorf("truncation notice on a complete history:\n%s", body)
		}
		if !strings.Contains(body, "edit number 4") {
			t.Error("the oldest commit is missing")
		}
	})

	t.Run("fewer commits than the limit", func(t *testing.T) {
		body := get(t, newServer(t, 2, 5), "/history/README.md").Body.String()
		if strings.Contains(body, "Only showing") {
			t.Errorf("truncation notice on a short history:\n%s", body)
		}
	})
}

// Slug-form history URLs predate the file-form ones and must keep working.
func TestHistoryAcceptsSlugForm(t *testing.T) {
	srv := newTestServer(t)

	fromSlug := get(t, srv, "/history/docs/setup")
	fromPath := get(t, srv, "/history/docs/setup.md")
	if fromSlug.Code != http.StatusOK || fromPath.Code != http.StatusOK {
		t.Fatalf("slug form = %d, file form = %d, want 200 for both", fromSlug.Code, fromPath.Code)
	}
	if fromSlug.Body.String() != fromPath.Body.String() {
		t.Error("the slug and file forms of a history URL render differently")
	}
}

// A stylesheet fix must reach a browser that already cached the old one, so
// the link carries a content version and only versioned URLs cache forever.
func TestStylesheetIsVersioned(t *testing.T) {
	srv := newTestServer(t)

	body := get(t, srv, "/").Body.String()
	if !strings.Contains(body, `href="/static/style.css?v=`+srv.assets.version+`"`) {
		t.Errorf("stylesheet link is not versioned:\n%s", body)
	}
	if srv.assets.version == "" {
		t.Fatal("asset version is empty")
	}

	versioned := get(t, srv, "/static/style.css?v="+srv.assets.version).Header().Get("Cache-Control")
	if !strings.Contains(versioned, "immutable") {
		t.Errorf("versioned asset Cache-Control = %q, want it to be immutable", versioned)
	}

	plain := get(t, srv, "/static/style.css").Header().Get("Cache-Control")
	if strings.Contains(plain, "immutable") || strings.Contains(plain, "max-age=86400") {
		t.Errorf("unversioned asset Cache-Control = %q, want a short lifetime", plain)
	}
}

func TestSecurityHeaders(t *testing.T) {
	srv := newTestServer(t)
	rec := get(t, srv, "/")

	want := map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
		"Referrer-Policy":        "no-referrer",
	}
	for header, value := range want {
		if got := rec.Header().Get(header); got != value {
			t.Errorf("%s = %q, want %q", header, got, value)
		}
	}
	if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "script-src 'none'") {
		t.Errorf("CSP = %q, want it to forbid scripts", csp)
	}
}

// Every route is read-only, so anything but GET or HEAD must be refused.
func TestWriteMethodsAreRejected(t *testing.T) {
	srv := newTestServer(t)

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		req := httptest.NewRequest(method, "/wacky/docs/setup", nil)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)

		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s /wacky/docs/setup = %d, want 405", method, rec.Code)
		}
	}
}

func TestHealth(t *testing.T) {
	srv := newTestServer(t)
	rec := get(t, srv, "/healthz")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var body struct {
		Status string `json:"status"`
		Pages  int    `json:"pages"`
		Ref    string `json:"ref"`
		Commit string `json:"commit"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}
	if body.Status != "ok" || body.Pages != 3 || body.Ref != "working tree" {
		t.Errorf("health = %+v", body)
	}
}

func TestTemplatesParse(t *testing.T) {
	// newTestServer already fails on a broken template; this asserts every
	// page template is present in the set.
	srv := newTestServer(t)
	for _, name := range []string{"page.gohtml", "dir.gohtml", "index.gohtml", "search.gohtml", "history.gohtml", "error.gohtml"} {
		if _, ok := srv.tmpl.set[name]; !ok {
			t.Errorf("template %q was not parsed", name)
		}
	}
}
