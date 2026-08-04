package server

import (
	"context"
	"encoding/json"
	"html"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/claesp/wacky/internal/config"
	"github.com/claesp/wacky/internal/git"
	"github.com/claesp/wacky/internal/markdown"
	"github.com/claesp/wacky/internal/wiki"
)

// fakeSource satisfies wiki.Source without touching a real repository.
type fakeSource struct{ files map[string]string }

func (f *fakeSource) Root() string { return "/fake/repo" }
func (f *fakeSource) Ref() string  { return "" }

func (f *fakeSource) Files(context.Context) ([]git.File, error) {
	paths := make([]string, 0, len(f.files))
	for p := range f.files {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	out := make([]git.File, 0, len(paths))
	for _, p := range paths {
		out = append(out, git.File{
			Path:    p,
			Size:    int64(len(f.files[p])),
			ModTime: time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC),
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

func (f *fakeSource) Log(context.Context, string, int) ([]git.Commit, error) {
	return []git.Commit{{
		Hash: "0123456789abcdef", Author: "Ada", Email: "ada@example.com",
		When: time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC), Subject: "write the docs",
	}}, nil
}

func (f *fakeSource) Head(context.Context) (git.Commit, error) {
	return git.Commit{Hash: "0123456789abcdef", Subject: "write the docs"}, nil
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
	store := wiki.NewStore(src, markdown.New(), log)
	if err := store.Reload(context.Background()); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	cfg := config.Default()
	cfg.Title = "Test Wiki"
	cfg.RepoPath = "/fake/repo"

	srv, err := New(cfg, store, log)
	if err != nil {
		t.Fatalf("New: %v", err)
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
		{"home", "/", http.StatusOK, []string{"Handbook", `href="/wiki/docs/setup"`}},
		{"page", "/wiki/docs/setup", http.StatusOK, []string{"<h1 id=\"setup\">Setup", "Run the installer"}},
		{"nested page", "/wiki/guides/deep/adv", http.StatusOK, []string{"Advanced"}},
		{"page list", "/pages", http.StatusOK, []string{"All pages", "docs/setup.md"}},
		{"search hit", "/search?q=installer", http.StatusOK, []string{"<mark>installer</mark>", "Setup"}},
		{"search miss", "/search?q=zzzznothing", http.StatusOK, []string{"Nothing matched"}},
		{"history", "/history/docs/setup", http.StatusOK, []string{"write the docs", "Ada"}},
		{"directory listing", "/wiki/guides", http.StatusOK, []string{"no index page", "Advanced"}},
		{"missing page", "/wiki/nope", http.StatusNotFound, []string{"There is no page at nope"}},
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

func TestPageRedirects(t *testing.T) {
	srv := newTestServer(t)

	tests := []struct{ target, location string }{
		{"/wiki/docs/setup/", "/wiki/docs/setup"},
		{"/wiki/docs/setup.md", "/wiki/docs/setup"},
		{"/wiki/docs/diagram.png", "/raw/docs/diagram.png"},
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

	first := get(t, srv, "/wiki/docs/setup")
	etag := first.Header().Get("ETag")
	if etag == "" {
		t.Fatal("no ETag on a page response")
	}

	second := get(t, srv, "/wiki/docs/setup", [2]string{"If-None-Match", etag})
	if second.Code != http.StatusNotModified {
		t.Errorf("conditional GET = %d, want 304", second.Code)
	}
	if second.Body.Len() != 0 {
		t.Errorf("304 response had a body of %d bytes", second.Body.Len())
	}

	// Same request, same answer.
	third := get(t, srv, "/wiki/docs/setup")
	if third.Body.String() != first.Body.String() {
		t.Error("repeating a GET produced a different page")
	}
}

// The sidebar carries a Home link, and the menu that reveals it on a phone is
// a checkbox: the Content-Security-Policy forbids JavaScript, so the toggle
// has to work in CSS alone.
func TestSidebarNavigation(t *testing.T) {
	srv := newTestServer(t)

	body := get(t, srv, "/wiki/docs/setup").Body.String()
	for _, want := range []string{
		`<li class="nav-top"><a href="/">Home</a></li>`,
		`<li class="nav-bottom"><a href="/pages">All pages</a></li>`,
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

	// Home first, then the repository tree, then the page list.
	home := strings.Index(body, `class="nav-top"`)
	tree := strings.Index(body, `<span class="nav-dir">Docs</span>`)
	pages := strings.Index(body, `class="nav-bottom"`)
	if !(home < tree && tree < pages) {
		t.Errorf("sidebar order is home=%d tree=%d pages=%d, want that order", home, tree, pages)
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

// The current entry is marked for assistive technology, not just visually.
func TestNavigationMarksCurrentPage(t *testing.T) {
	tests := []struct {
		target, current string
	}{
		{"/", `<a href="/" class="active" aria-current="page">Home</a>`},
		{"/pages", `<a href="/pages" class="active" aria-current="page">All pages</a>`},
	}
	srv := newTestServer(t)
	for _, tt := range tests {
		body := get(t, srv, tt.target).Body.String()
		if !strings.Contains(body, tt.current) {
			t.Errorf("GET %s did not mark its own entry as current", tt.target)
		}
	}

	// A wiki page must not mark Home as current.
	body := get(t, srv, "/wiki/docs/setup").Body.String()
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
				`<a href="/pages" aria-current="page">All pages</a>`,
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
			target: "/wiki/docs/setup",
			want: []string{
				`<a href="/">Home</a>`,
				`<a href="/wiki/docs">Docs</a>`,
				`<a href="/wiki/docs/setup" aria-current="page">Setup</a>`,
			},
		},
		{
			name:   "directory listing",
			target: "/wiki/guides",
			want:   []string{`<a href="/">Home</a>`, `<a href="/wiki/guides" aria-current="page">`},
		},
		{
			name:   "history is not one of its own crumbs",
			target: "/history/docs/setup",
			want:   []string{`<a href="/">Home</a>`, `<a href="/wiki/docs/setup">Setup</a>`},
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

// The history view carries the same footer as a page: the last change, a link
// to the source, a self-referencing history link and the repository path. The
// history view's own RepoPath field must not be mistaken for the request path
// the breadcrumb trail compares against.
func TestHistoryFooterMatchesAPage(t *testing.T) {
	srv := newTestServer(t)

	pageFooter := footer(t, get(t, srv, "/wiki/docs/setup").Body.String())
	histFooter := footer(t, get(t, srv, "/history/docs/setup.md").Body.String())

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

	// The old in-article file name and back link are gone.
	body := get(t, srv, "/history/docs/setup.md").Body.String()
	article := body[strings.Index(body, "<article"):strings.Index(body, "</article>")]
	if strings.Contains(article, "back to the page") {
		t.Error("history article still holds the back link")
	}
	if strings.Contains(article, "docs/setup.md") {
		t.Errorf("history article still repeats the file name:\n%s", article)
	}
}

// footer returns the page-meta block of a rendered page.
func footer(t *testing.T, body string) string {
	t.Helper()
	start := strings.Index(body, `<div class="page-meta">`)
	if start < 0 {
		t.Fatalf("no page-meta footer in:\n%s", body)
	}
	end := strings.Index(body[start:], "</div>\n</div>")
	if end < 0 {
		t.Fatal("page-meta footer is not closed as expected")
	}
	return body[start : start+end]
}

// A history link names the source file, so the home page — whose slug is empty
// — links to its own file rather than to a bare "/history/".
func TestHistoryLinksNameTheSourceFile(t *testing.T) {
	srv := newTestServer(t)

	tests := []struct{ page, wantLink string }{
		{"/", `<a href="/history/README.md">History</a>`},
		{"/wiki/docs/setup", `<a href="/history/docs/setup.md">History</a>`},
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
	if !strings.Contains(body, `href="/static/style.css?v=`+srv.assets+`"`) {
		t.Errorf("stylesheet link is not versioned:\n%s", body)
	}
	if srv.assets == "" {
		t.Fatal("asset version is empty")
	}

	versioned := get(t, srv, "/static/style.css?v="+srv.assets).Header().Get("Cache-Control")
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
		req := httptest.NewRequest(method, "/wiki/docs/setup", nil)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)

		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s /wiki/docs/setup = %d, want 405", method, rec.Code)
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
