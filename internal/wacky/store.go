package wacky

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/claesp/wacky/internal/git"
	"github.com/claesp/wacky/internal/markdown"
)

// ErrNotFound reports a slug or path that is not part of the wiki.
var ErrNotFound = errors.New("wacky: page not found")

// Source is the read-only view of a repository that a Store needs. It is
// declared here, in the consumer, so the Store can be tested with a fake.
type Source interface {
	Root() string
	Ref() string
	Files(ctx context.Context) ([]git.File, error)
	Read(ctx context.Context, rel string) ([]byte, error)
	Log(ctx context.Context, rel string, limit int) ([]git.Commit, error)
	Head(ctx context.Context) (git.Commit, error)
	FirstCommit(ctx context.Context) (git.Commit, error)
}

// Store holds the page index built from a repository.
//
// Reload is idempotent: it rebuilds a complete snapshot and swaps it in, so
// running it once or a hundred times over an unchanged repository leaves the
// Store in exactly the same state. Readers never observe a partial index.
type Store struct {
	src      Source
	renderer *markdown.Renderer
	log      *slog.Logger

	mu   sync.RWMutex
	snap *snapshot
}

// snapshot is an immutable index of the repository at one point in time. Its
// only mutable part is the memoised render cache.
type snapshot struct {
	pages  map[string]*Page // slug -> page
	byPath map[string]*Page // repository path -> page
	titles map[string]*Page // lowercased title -> page
	files  map[string]git.File
	source map[string][]byte // repository path -> raw Markdown

	sorted   []*Page
	byTime   []*Page
	tree     *Node
	home     *Page
	head     git.Commit
	first    git.Commit
	loadedAt time.Time

	mu       sync.Mutex
	rendered map[string]markdown.Document
}

// NewStore returns an empty Store. Call Reload before serving.
func NewStore(src Source, renderer *markdown.Renderer, log *slog.Logger) *Store {
	if renderer == nil {
		renderer = markdown.New()
	}
	if log == nil {
		log = slog.Default()
	}
	return &Store{src: src, renderer: renderer, log: log, snap: emptySnapshot()}
}

func emptySnapshot() *snapshot {
	return &snapshot{
		pages:    map[string]*Page{},
		byPath:   map[string]*Page{},
		titles:   map[string]*Page{},
		files:    map[string]git.File{},
		source:   map[string][]byte{},
		rendered: map[string]markdown.Document{},
	}
}

// Reload rebuilds the page index from the repository.
func (s *Store) Reload(ctx context.Context) error {
	files, err := s.src.Files(ctx)
	if err != nil {
		return fmt.Errorf("list repository: %w", err)
	}
	head, err := s.src.Head(ctx)
	if err != nil {
		return fmt.Errorf("read head commit: %w", err)
	}

	next := emptySnapshot()
	next.head = head
	next.loadedAt = time.Now()

	// The first commit cannot change while the process runs, so it is fetched
	// once and carried forward across reloads.
	next.first = s.current().first
	if next.first.IsZero() {
		first, err := s.src.FirstCommit(ctx)
		if err != nil {
			s.log.Warn("could not read the first commit", "error", err)
		} else {
			next.first = first
		}
	}

	for _, f := range files {
		next.files[f.Path] = f
		if !IsMarkdown(f.Path) {
			continue
		}
		src, err := s.src.Read(ctx, f.Path)
		if err != nil {
			// One unreadable file must not take down the whole wiki.
			s.log.Warn("skipping file", "path", f.Path, "error", err)
			continue
		}

		slug, isIndex := slugFor(f.Path)
		page := &Page{
			Path:    f.Path,
			Slug:    slug,
			Dir:     path.Dir("/" + f.Path)[1:],
			IsIndex: isIndex,
			Size:    f.Size,
			ModTime: f.ModTime,
		}
		if page.Dir == "." {
			page.Dir = ""
		}
		if page.Size == 0 {
			page.Size = int64(len(src))
		}
		page.Title = firstNonEmpty(headingTitle(src), titleFor(page.Name()))

		// A directory index wins over a same-slug sibling.
		if existing, clash := next.pages[slug]; clash && !isIndex {
			s.log.Debug("slug already taken", "slug", slug, "keeping", existing.Path, "skipping", f.Path)
		} else {
			next.pages[slug] = page
		}
		next.byPath[f.Path] = page
		next.source[f.Path] = src

		key := strings.ToLower(page.Title)
		if _, dup := next.titles[key]; !dup {
			next.titles[key] = page
		}
	}

	next.sorted = sortedPages(next.byPath)
	next.byTime = pagesByModified(next.sorted)
	next.tree = buildTree(next.sorted)
	next.home = pickHome(next.pages)

	s.mu.Lock()
	s.snap = next
	s.mu.Unlock()

	s.log.Info("index rebuilt",
		"pages", len(next.byPath),
		"files", len(next.files),
		"commit", head.Short(),
		"ref", s.src.Ref(),
	)
	return nil
}

// Watch reloads the index every interval until ctx is cancelled. A non-positive
// interval disables reloading and returns immediately.
func (s *Store) Watch(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.Reload(ctx); err != nil && ctx.Err() == nil {
				s.log.Error("reload failed", "error", err)
			}
		}
	}
}

// Stats describes the current index.
type Stats struct {
	Pages int
	Files int
	Head  git.Commit
	// First is the oldest commit in the served revision. It is the zero
	// Commit in a repository without history.
	First    git.Commit
	Ref      string
	Root     string
	LoadedAt time.Time
}

// Stats returns a summary of the current snapshot.
func (s *Store) Stats() Stats {
	snap := s.current()
	return Stats{
		Pages:    len(snap.byPath),
		Files:    len(snap.files),
		Head:     snap.head,
		First:    snap.first,
		Ref:      s.src.Ref(),
		Root:     s.src.Root(),
		LoadedAt: snap.loadedAt,
	}
}

func (s *Store) current() *snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snap
}

// Page returns the page served at slug.
func (s *Store) Page(slug string) (*Page, bool) {
	page, ok := s.current().pages[normalizeSlug(slug)]
	return page, ok
}

// PageByPath returns the page stored at a repository path.
func (s *Store) PageByPath(repoPath string) (*Page, bool) {
	page, ok := s.current().byPath[normalizeSlug(repoPath)]
	return page, ok
}

// HasFile reports whether a repository path is part of the served revision.
func (s *Store) HasFile(repoPath string) bool {
	_, ok := s.current().files[normalizeSlug(repoPath)]
	return ok
}

// FileInfo returns the repository listing entry for a path.
func (s *Store) FileInfo(repoPath string) (git.File, bool) {
	f, ok := s.current().files[normalizeSlug(repoPath)]
	return f, ok
}

// Home returns the page shown at the site root.
func (s *Store) Home() (*Page, bool) {
	home := s.current().home
	return home, home != nil
}

// Pages returns every page, ordered by path.
func (s *Store) Pages() []*Page { return s.current().sorted }

// PagesByModified returns every page, most recently changed first.
func (s *Store) PagesByModified() []*Page { return s.current().byTime }

// Tree returns the directory tree of pages for navigation.
func (s *Store) Tree() *Node { return s.current().tree }

// Children returns the pages and sub-directories directly below a directory
// slug, which is what a directory listing shows.
func (s *Store) Children(dir string) ([]*Node, bool) {
	node := s.current().tree.Find(normalizeSlug(dir))
	if node == nil {
		return nil, false
	}
	return node.Children, true
}

// Source returns the raw Markdown of a page.
func (s *Store) Source(p *Page) ([]byte, bool) {
	src, ok := s.current().source[p.Path]
	return src, ok
}

// Render converts a page to HTML, memoising the result for the lifetime of the
// current snapshot.
func (s *Store) Render(p *Page) (markdown.Document, error) {
	snap := s.current()
	src, ok := snap.source[p.Path]
	if !ok {
		return markdown.Document{}, fmt.Errorf("render %q: %w", p.Path, ErrNotFound)
	}

	sum := sha256.Sum256(src)
	key := p.Path + ":" + hex.EncodeToString(sum[:8])

	snap.mu.Lock()
	doc, cached := snap.rendered[key]
	snap.mu.Unlock()
	if cached {
		return doc, nil
	}

	doc = s.renderer.Render(src, snap.options(p))
	if doc.Title == "" {
		doc.Title = p.Title
	}

	snap.mu.Lock()
	snap.rendered[key] = doc
	snap.mu.Unlock()
	return doc, nil
}

// ETag returns a strong validator for a page's rendered form.
func (s *Store) ETag(p *Page) string {
	snap := s.current()
	src, ok := snap.source[p.Path]
	if !ok {
		return ""
	}
	sum := sha256.Sum256(src)
	return `"` + hex.EncodeToString(sum[:16]) + `"`
}

// Raw returns the bytes of any file in the repository, page or not.
func (s *Store) Raw(ctx context.Context, repoPath string) ([]byte, error) {
	snap := s.current()
	clean := normalizeSlug(repoPath)
	if _, ok := snap.files[clean]; !ok {
		return nil, fmt.Errorf("raw %q: %w", repoPath, ErrNotFound)
	}
	if src, ok := snap.source[clean]; ok {
		return src, nil
	}
	return s.src.Read(ctx, clean)
}

// History returns the commits that touched a repository path, newest first.
func (s *Store) History(ctx context.Context, repoPath string, limit int) ([]git.Commit, error) {
	return s.src.Log(ctx, normalizeSlug(repoPath), limit)
}

// options builds the link resolution rules for one page.
func (snap *snapshot) options(p *Page) markdown.Options {
	return markdown.Options{
		ResolveLink: func(dest string) string { return snap.resolveLink(p, dest) },
		ResolveWiki: snap.resolveWiki,
	}
}

// resolveLink rewrites a destination written relative to the source file into
// a URL this server can serve.
func (snap *snapshot) resolveLink(p *Page, dest string) string {
	if dest == "" || strings.HasPrefix(dest, "#") || strings.HasPrefix(dest, "//") {
		return dest
	}
	if colon := strings.IndexByte(dest, ':'); colon > 0 && !strings.ContainsAny(dest[:colon], "/?") {
		return dest // absolute URL: leave it alone
	}

	target, suffix := splitSuffix(dest)
	if target == "" {
		return dest
	}

	var repoPath string
	if strings.HasPrefix(target, "/") {
		repoPath = path.Clean(target)[1:]
	} else {
		repoPath = path.Join(p.Dir, target)
	}
	if repoPath == "." || strings.HasPrefix(repoPath, "..") {
		return dest
	}

	if page, ok := snap.byPath[repoPath]; ok {
		return page.URL() + suffix
	}
	if _, ok := snap.files[repoPath]; ok {
		return "/raw/" + repoPath + suffix
	}
	// An extension-less link to a sibling page, e.g. [setup](setup).
	if page, ok := snap.pages[repoPath]; ok {
		return page.URL() + suffix
	}
	return dest
}

// resolveWiki maps a [[wiki link]] target onto a page.
func (snap *snapshot) resolveWiki(target string) (string, bool) {
	target = strings.TrimSpace(target)
	if target == "" {
		return "", false
	}

	candidates := []string{
		normalizeSlug(target),
		strings.ToLower(normalizeSlug(target)),
		markdown.Slug(target),
	}
	for _, c := range candidates {
		if page, ok := snap.pages[c]; ok {
			return page.URL(), true
		}
	}
	if page, ok := snap.titles[strings.ToLower(target)]; ok {
		return page.URL(), true
	}
	for _, page := range snap.sorted {
		if strings.EqualFold(page.Name(), target) {
			return page.URL(), true
		}
	}
	return "/search?q=" + url.QueryEscape(target), false
}

// splitSuffix separates a query string or fragment from a link destination.
func splitSuffix(dest string) (target, suffix string) {
	if i := strings.IndexAny(dest, "#?"); i >= 0 {
		return dest[:i], dest[i:]
	}
	return dest, ""
}

func normalizeSlug(s string) string {
	s = strings.Trim(strings.TrimSpace(s), "/")
	if s == "" {
		return ""
	}
	return path.Clean(s)
}

// pagesByModified copies the page list into modification order, newest first.
// Pages sharing a timestamp — which is every page when a revision is pinned,
// since a commit has no per-file mtime — keep their path order.
func pagesByModified(pages []*Page) []*Page {
	out := make([]*Page, len(pages))
	copy(out, pages)

	sort.SliceStable(out, func(i, j int) bool {
		return out[i].ModTime.After(out[j].ModTime)
	})
	return out
}

func sortedPages(byPath map[string]*Page) []*Page {
	pages := make([]*Page, 0, len(byPath))
	for _, p := range byPath {
		pages = append(pages, p)
	}
	sort.Slice(pages, func(i, j int) bool { return pages[i].Path < pages[j].Path })
	return pages
}

// pickHome chooses the page served at "/".
func pickHome(pages map[string]*Page) *Page {
	if page, ok := pages[""]; ok {
		return page
	}
	for _, name := range []string{"home", "index", "readme"} {
		if page, ok := pages[name]; ok {
			return page
		}
	}
	return nil
}

// headingTitle extracts a title from front matter or the first heading without
// rendering the whole document.
func headingTitle(src []byte) string {
	body, meta := splitFrontMatterForTitle(string(src))
	if t := strings.TrimSpace(meta); t != "" {
		return t
	}
	lines := strings.Split(body, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "#") {
			if title := strings.TrimSpace(strings.Trim(trimmed, "#")); title != "" {
				return plainTitle(title)
			}
			continue
		}
		// Setext heading: text underlined with === on the next line.
		if i+1 < len(lines) && strings.Trim(strings.TrimSpace(lines[i+1]), "=") == "" && strings.TrimSpace(lines[i+1]) != "" {
			return plainTitle(trimmed)
		}
		return ""
	}
	return ""
}

// splitFrontMatterForTitle returns the body and the front matter title, if any.
func splitFrontMatterForTitle(s string) (body, title string) {
	lines := strings.Split(s, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return s, ""
	}
	for i := 1; i < len(lines); i++ {
		trimmed := strings.TrimSpace(lines[i])
		if trimmed == "---" || trimmed == "..." {
			return strings.Join(lines[i+1:], "\n"), title
		}
		if key, value, found := strings.Cut(trimmed, ":"); found && strings.EqualFold(strings.TrimSpace(key), "title") {
			title = strings.Trim(strings.TrimSpace(value), `"'`)
		}
	}
	return s, ""
}

func plainTitle(s string) string {
	return strings.TrimSpace(strings.NewReplacer("`", "", "*", "", "_", "").Replace(s))
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
