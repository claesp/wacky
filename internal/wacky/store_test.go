package wacky

import (
	"context"
	"io"
	"log/slog"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/claesp/wacky/internal/git"
	"github.com/claesp/wacky/internal/markdown"
)

// fakeSource is an in-memory stand-in for a repository.
type fakeSource struct {
	files map[string]string
	reads int
}

func (f *fakeSource) Root() string { return "/fake/repo" }
func (f *fakeSource) Ref() string  { return "" }

// The fake never moves, so there is nothing to re-resolve.
func (f *fakeSource) Refresh(context.Context) error { return nil }

func (f *fakeSource) Files(context.Context) ([]git.File, error) {
	paths := make([]string, 0, len(f.files))
	for p := range f.files {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	files := make([]git.File, 0, len(paths))
	// Each file is a minute newer than the one before, so ordering by time is
	// distinguishable from ordering by path.
	for i, p := range paths {
		files = append(files, git.File{
			Path:    p,
			Size:    int64(len(f.files[p])),
			ModTime: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC).Add(time.Duration(i) * time.Minute),
		})
	}
	return files, nil
}

func (f *fakeSource) Read(_ context.Context, rel string) ([]byte, error) {
	content, ok := f.files[rel]
	if !ok {
		return nil, git.ErrNotFound
	}
	f.reads++
	return []byte(content), nil
}

func (f *fakeSource) Log(context.Context, string, int) ([]git.Commit, error) {
	return []git.Commit{{Hash: "abc123def456", Author: "Ada", Subject: "edit"}}, nil
}

func (f *fakeSource) Head(context.Context) (git.Commit, error) {
	return git.Commit{Hash: "abc123def456", Subject: "edit"}, nil
}

func (f *fakeSource) FirstCommit(context.Context) (git.Commit, error) {
	return git.Commit{
		Hash:    "000111222333",
		Subject: "initial import",
		When:    time.Date(2019, 5, 4, 3, 2, 1, 0, time.UTC),
	}, nil
}

func newTestStore(t *testing.T, files map[string]string) *Store {
	t.Helper()
	store := NewStore(&fakeSource{files: files},
		markdown.New(),
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := store.Reload(context.Background()); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	return store
}

func defaultFiles() map[string]string {
	return map[string]string{
		"README.md":         "# Project Home\n\nSee [setup](docs/setup.md) and [[Glossary]].\n",
		"docs/setup.md":     "# Setup Guide\n\nInstall it. See ![diagram](diagram.png).\n",
		"docs/glossary.md":  "---\ntitle: Glossary\n---\n\nWords and their meanings.\n",
		"docs/diagram.png":  "\x89PNG fake",
		"notes/2026/q1.md":  "# Q1 Notes\n\nQuarterly review.\n",
		"LICENSE":           "MIT",
		"docs/nested/a.mdx": "not markdown to us",
	}
}

func TestReloadBuildsIndex(t *testing.T) {
	store := newTestStore(t, defaultFiles())

	tests := []struct {
		slug      string
		wantTitle string
		wantPath  string
	}{
		{"", "Project Home", "README.md"},
		{"docs/setup", "Setup Guide", "docs/setup.md"},
		{"docs/glossary", "Glossary", "docs/glossary.md"},
		{"notes/2026/q1", "Q1 Notes", "notes/2026/q1.md"},
	}
	for _, tt := range tests {
		page, ok := store.Page(tt.slug)
		if !ok {
			t.Errorf("Page(%q) not found", tt.slug)
			continue
		}
		if page.Title != tt.wantTitle {
			t.Errorf("Page(%q).Title = %q, want %q", tt.slug, page.Title, tt.wantTitle)
		}
		if page.Path != tt.wantPath {
			t.Errorf("Page(%q).Path = %q, want %q", tt.slug, page.Path, tt.wantPath)
		}
	}

	if _, ok := store.Page("LICENSE"); ok {
		t.Error("a non-Markdown file was indexed as a page")
	}
	if !store.HasFile("docs/diagram.png") {
		t.Error("HasFile lost a non-Markdown repository file")
	}
	if home, ok := store.Home(); !ok || home.Path != "README.md" {
		t.Errorf("Home = %v, %v; want README.md", home, ok)
	}
	if got := len(store.Pages()); got != 4 {
		t.Errorf("len(Pages) = %d, want 4", got)
	}
}

// Reloading an unchanged repository must leave the index exactly as it was.
func TestReloadIsIdempotent(t *testing.T) {
	files := defaultFiles()
	store := newTestStore(t, files)

	fingerprint := func() []string {
		var out []string
		for _, p := range store.Pages() {
			out = append(out, p.Path+"|"+p.Slug+"|"+p.Title)
		}
		return out
	}
	before := fingerprint()

	for i := 0; i < 3; i++ {
		if err := store.Reload(context.Background()); err != nil {
			t.Fatalf("Reload %d: %v", i, err)
		}
		if got := fingerprint(); !reflect.DeepEqual(before, got) {
			t.Fatalf("index changed after reload %d:\nbefore %v\nafter  %v", i, before, got)
		}
	}
}

func TestReloadPicksUpChanges(t *testing.T) {
	src := &fakeSource{files: defaultFiles()}
	store := NewStore(src, markdown.New(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := store.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}

	src.files["docs/new.md"] = "# Brand New\n"
	delete(src.files, "notes/2026/q1.md")
	if err := store.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}

	if _, ok := store.Page("docs/new"); !ok {
		t.Error("a new page was not picked up")
	}
	if _, ok := store.Page("notes/2026/q1"); ok {
		t.Error("a deleted page is still served")
	}
}

func TestRenderResolvesLinks(t *testing.T) {
	store := newTestStore(t, defaultFiles())

	home, _ := store.Page("")
	doc, err := store.Render(home)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	html := string(doc.HTML)

	if !strings.Contains(html, `href="/wacky/docs/setup"`) {
		t.Errorf("relative Markdown link was not rewritten: %s", html)
	}
	if !strings.Contains(html, `class="wikilink" href="/wacky/docs/glossary"`) {
		t.Errorf("wiki link did not resolve by title: %s", html)
	}

	setup, _ := store.Page("docs/setup")
	doc, err = store.Render(setup)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(string(doc.HTML), `src="/raw/docs/diagram.png"`) {
		t.Errorf("relative image was not rewritten: %s", doc.HTML)
	}
}

func TestRenderIsCachedPerSnapshot(t *testing.T) {
	store := newTestStore(t, defaultFiles())
	home, _ := store.Page("")

	first, err := store.Render(home)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Render(home)
	if err != nil {
		t.Fatal(err)
	}
	if first.HTML != second.HTML {
		t.Error("repeated renders of the same page differ")
	}
}

// The page list is ordered newest first, without disturbing the path order
// the navigation tree relies on.
func TestPagesByModified(t *testing.T) {
	store := newTestStore(t, defaultFiles())

	byPath := store.Pages()
	byTime := store.PagesByModified()

	if len(byTime) != len(byPath) {
		t.Fatalf("PagesByModified has %d pages, Pages has %d", len(byTime), len(byPath))
	}
	for i := 1; i < len(byTime); i++ {
		if byTime[i-1].ModTime.Before(byTime[i].ModTime) {
			t.Errorf("page %d (%s) is older than the one after it (%s)",
				i-1, byTime[i-1].ModTime, byTime[i].ModTime)
		}
	}

	// The fake source dates each file a minute after the previous one in path
	// order, so the newest is the last path.
	if got, want := byTime[0].Path, byPath[len(byPath)-1].Path; got != want {
		t.Errorf("newest page = %q, want %q", got, want)
	}

	// Sorting must not have reordered the shared path-ordered slice.
	for i := 1; i < len(byPath); i++ {
		if byPath[i-1].Path > byPath[i].Path {
			t.Fatalf("Pages() is no longer in path order: %q before %q", byPath[i-1].Path, byPath[i].Path)
		}
	}
}

// untimedSource is a repository whose files carry no modification time, which
// is what a pinned revision looks like: a commit has no per-file mtime.
type untimedSource struct{ *fakeSource }

func (u untimedSource) Files(ctx context.Context) ([]git.File, error) {
	files, err := u.fakeSource.Files(ctx)
	for i := range files {
		files[i].ModTime = time.Time{}
	}
	return files, err
}

// Without timestamps every page ties, so the order has to stay stable rather
// than arbitrary.
func TestPagesByModifiedWithoutTimestamps(t *testing.T) {
	src := untimedSource{&fakeSource{files: map[string]string{
		"a.md": "# A\n", "b.md": "# B\n", "c.md": "# C\n",
	}}}
	store := NewStore(src, markdown.New(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := store.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}

	var order []string
	for _, p := range store.PagesByModified() {
		order = append(order, p.Path)
	}
	if !reflect.DeepEqual(order, []string{"a.md", "b.md", "c.md"}) {
		t.Errorf("order = %v, want path order", order)
	}
}

func TestSearch(t *testing.T) {
	store := newTestStore(t, defaultFiles())

	results := store.Search("quarterly", 10)
	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}
	if results[0].Page.Slug != "notes/2026/q1" {
		t.Errorf("matched %q, want notes/2026/q1", results[0].Page.Slug)
	}
	if !strings.Contains(string(results[0].Snippet), "<mark>Quarterly</mark>") {
		t.Errorf("snippet did not highlight the match: %s", results[0].Snippet)
	}

	// A title match must outrank a body-only match.
	results = store.Search("setup", 10)
	if len(results) == 0 || results[0].Page.Slug != "docs/setup" {
		t.Errorf("ranking put %v first", results)
	}
	if got := store.Search("   ", 10); got != nil {
		t.Errorf("blank query returned %d results", len(got))
	}
}

func TestSearchEscapesSnippets(t *testing.T) {
	store := newTestStore(t, map[string]string{
		"evil.md": "# Evil\n\nfind <script>alert(1)</script> me\n",
	})

	results := store.Search("script", 10)
	if len(results) == 0 {
		t.Fatal("no results")
	}
	if strings.Contains(string(results[0].Snippet), "<script>") {
		t.Errorf("snippet contained raw HTML: %s", results[0].Snippet)
	}
}

func TestTreeAndBreadcrumbs(t *testing.T) {
	store := newTestStore(t, defaultFiles())

	children, ok := store.Children("notes")
	if !ok || len(children) != 1 || children[0].Slug != "notes/2026" {
		t.Errorf("Children(notes) = %v, %v", children, ok)
	}

	trail := store.Breadcrumbs("notes/2026/q1")
	want := []Breadcrumb{
		{Name: "Notes", URL: "/wacky/notes"},
		{Name: "2026", URL: "/wacky/notes/2026"},
		{Name: "Q1 Notes", URL: "/wacky/notes/2026/q1"},
	}
	if !reflect.DeepEqual(trail, want) {
		t.Errorf("Breadcrumbs = %+v, want %+v", trail, want)
	}
}

func TestSlugForIndexPages(t *testing.T) {
	tests := []struct {
		path      string
		wantSlug  string
		wantIndex bool
	}{
		{"README.md", "", true},
		{"index.md", "", true},
		{"docs/README.md", "docs", true},
		{"docs/Home.md", "docs", true},
		{"docs/setup.md", "docs/setup", false},
		{"a/b/c.markdown", "a/b/c", false},
	}
	for _, tt := range tests {
		slug, isIndex := slugFor(tt.path)
		if slug != tt.wantSlug || isIndex != tt.wantIndex {
			t.Errorf("slugFor(%q) = (%q, %v), want (%q, %v)", tt.path, slug, isIndex, tt.wantSlug, tt.wantIndex)
		}
	}
}

func TestEmptyRepository(t *testing.T) {
	store := newTestStore(t, map[string]string{})

	if _, ok := store.Home(); ok {
		t.Error("an empty repository reported a home page")
	}
	if got := len(store.Pages()); got != 0 {
		t.Errorf("len(Pages) = %d, want 0", got)
	}
	if store.Tree() == nil {
		t.Error("Tree returned nil for an empty repository")
	}
}
