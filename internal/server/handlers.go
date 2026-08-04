package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/claesp/wacky/internal/git"
	"github.com/claesp/wacky/internal/markdown"
	"github.com/claesp/wacky/internal/wacky"
)

// layout carries the data every template needs.
type layout struct {
	Site      string
	PageTitle string
	Slug      string
	Query     string
	Ref       string
	Nav       []*wacky.Node
	Stats     wacky.Stats
	// Path is the request path, used to mark the current navigation entry.
	Path string
	// Assets versions the stylesheet URL so a new binary is picked up at once.
	Assets string
}

func (s *Server) layout(r *http.Request, title, slug string) layout {
	return layout{
		Site:      s.cfg.Title,
		PageTitle: title,
		Slug:      slug,
		Ref:       s.cfg.Ref,
		Nav:       s.store.Tree().Children,
		Stats:     s.store.Stats(),
		Path:      r.URL.Path,
		Assets:    s.assets,
	}
}

// pageView renders a single wiki page.
type pageView struct {
	layout
	Page        *wacky.Page
	Doc         markdown.Document
	Breadcrumbs []wacky.Breadcrumb
	LastCommit  git.Commit
	HasHistory  bool
}

// dirView renders a directory that has no index page.
type dirView struct {
	layout
	Dir         string
	Children    []*wacky.Node
	Breadcrumbs []wacky.Breadcrumb
}

type indexView struct {
	layout
	Pages       []*wacky.Page
	Breadcrumbs []wacky.Breadcrumb
}

type searchView struct {
	layout
	Results     []wacky.Result
	Total       int
	Breadcrumbs []wacky.Breadcrumb
}

type historyView struct {
	layout
	// RepoPath is the file the history belongs to. It is deliberately not
	// called Path, which would shadow layout.Path in templates.
	RepoPath string
	// Heading is the title of the page the history belongs to, or the file
	// name for a file that is not a page.
	Heading     string
	Page        *wacky.Page
	Commits     []git.Commit
	Breadcrumbs []wacky.Breadcrumb
	// LastCommit is Commits[0], carried separately so the footer partial can
	// be shared with the page view.
	LastCommit git.Commit
	HasHistory bool
}

type errorView struct {
	layout
	Status  int
	Message string
}

// handleHome serves the repository's index page, or the page list when the
// repository has none.
func (s *Server) handleHome(w http.ResponseWriter, r *http.Request) {
	page, ok := s.store.Home()
	if !ok {
		http.Redirect(w, r, "/pages", http.StatusSeeOther)
		return
	}
	s.renderPage(w, r, page)
}

// handlePage serves a page, a directory listing, or a redirect to the raw file
// for non-Markdown paths.
func (s *Server) handlePage(w http.ResponseWriter, r *http.Request) {
	raw := r.PathValue("path")
	slug := strings.Trim(path.Clean("/"+raw), "/")

	if slug == "" || slug == "." {
		http.Redirect(w, r, "/", http.StatusMovedPermanently)
		return
	}
	// Send every alias of a page (trailing slash, doubled separators, "./")
	// to its single canonical URL.
	if slug != raw {
		http.Redirect(w, r, "/wacky/"+slug, http.StatusMovedPermanently)
		return
	}

	if page, ok := s.store.Page(slug); ok {
		s.renderPage(w, r, page)
		return
	}
	// "/wacky/notes/setup.md" is a valid way to spell "/wacky/notes/setup".
	if wacky.IsMarkdown(slug) {
		if page, ok := s.store.PageByPath(slug); ok {
			http.Redirect(w, r, page.URL(), http.StatusMovedPermanently)
			return
		}
	}
	if children, ok := s.store.Children(slug); ok && len(children) > 0 {
		s.renderDir(w, r, slug, children)
		return
	}
	if s.store.HasFile(slug) {
		http.Redirect(w, r, "/raw/"+slug, http.StatusMovedPermanently)
		return
	}
	s.renderError(w, r, http.StatusNotFound, "There is no page at "+slug+".")
}

func (s *Server) renderPage(w http.ResponseWriter, r *http.Request, page *wacky.Page) {
	etag := s.store.ETag(page)
	if etag != "" {
		w.Header().Set("ETag", etag)
		w.Header().Set("Cache-Control", "no-cache")
		if matchesETag(r.Header.Get("If-None-Match"), etag) {
			w.WriteHeader(http.StatusNotModified)
			return
		}
	}

	doc, err := s.store.Render(page)
	if err != nil {
		s.log.Error("render page", "path", page.Path, "error", err)
		s.renderError(w, r, http.StatusInternalServerError, "This page could not be rendered.")
		return
	}

	view := pageView{
		layout:      s.layout(r, doc.Title, page.Slug),
		Page:        page,
		Doc:         doc,
		Breadcrumbs: s.store.Breadcrumbs(page.Slug),
	}

	ctx, cancel := s.timeoutFor(r)
	defer cancel()
	if commits, err := s.store.History(ctx, page.Path, 1); err == nil && len(commits) > 0 {
		view.LastCommit = commits[0]
		view.HasHistory = true
	}

	s.write(w, r, http.StatusOK, "page.gohtml", view)
}

func (s *Server) renderDir(w http.ResponseWriter, r *http.Request, slug string, children []*wacky.Node) {
	s.write(w, r, http.StatusOK, "dir.gohtml", dirView{
		layout:      s.layout(r, path.Base(slug), slug),
		Dir:         slug,
		Children:    children,
		Breadcrumbs: s.store.Breadcrumbs(slug),
	})
}

// handleIndex lists every page in the wiki. It has no place in the repository
// tree, so its trail is the one it would have if it sat directly below Home.
func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	s.write(w, r, http.StatusOK, "index.gohtml", indexView{
		layout:      s.layout(r, "All pages", ""),
		Pages:       s.store.Pages(),
		Breadcrumbs: []wacky.Breadcrumb{{Name: "All pages", URL: "/pages"}},
	})
}

// handleSearch runs a full-text search over the current snapshot.
func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	results := s.store.Search(query, 50)

	// The crumb carries the query, so following it repeats the search rather
	// than landing on an empty form.
	crumb := wacky.Breadcrumb{Name: "Search", URL: "/search"}
	if query != "" {
		crumb.URL += "?q=" + url.QueryEscape(query)
	}

	view := searchView{
		layout:      s.layout(r, "Search", ""),
		Results:     results,
		Total:       len(results),
		Breadcrumbs: []wacky.Breadcrumb{crumb},
	}
	view.Query = query
	s.write(w, r, http.StatusOK, "search.gohtml", view)
}

// handleHistory shows the commits that touched a file.
func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	target := strings.Trim(path.Clean("/"+r.PathValue("path")), "/")
	if target == "" || target == "." {
		s.renderError(w, r, http.StatusNotFound, "No file was given.")
		return
	}

	// History is addressed by source file, which is what the page templates
	// link to. A page slug is still accepted, so older links keep working.
	page, isPage := s.store.PageByPath(target)
	if !isPage {
		page, isPage = s.store.Page(target)
	}

	repoPath := target
	switch {
	case isPage:
		repoPath = page.Path
	case !s.store.HasFile(target):
		s.renderError(w, r, http.StatusNotFound, "There is no file at "+target+".")
		return
	}

	// Breadcrumbs and the heading follow the page; a file that is not a page
	// falls back to its path.
	crumbPath, heading := target, path.Base(repoPath)
	if isPage {
		crumbPath, heading = page.Slug, page.Title
	}

	ctx, cancel := s.timeoutFor(r)
	defer cancel()
	commits, err := s.store.History(ctx, repoPath, s.cfg.HistoryLimit)
	if err != nil {
		s.log.Error("read history", "path", repoPath, "error", err)
		s.renderError(w, r, http.StatusInternalServerError, "The history could not be read.")
		return
	}

	view := historyView{
		layout:      s.layout(r, "History of "+heading, crumbPath),
		RepoPath:    repoPath,
		Heading:     heading,
		Page:        page,
		Commits:     commits,
		Breadcrumbs: s.store.Breadcrumbs(crumbPath),
	}
	if len(commits) > 0 {
		view.LastCommit = commits[0]
		view.HasHistory = true
	}
	s.write(w, r, http.StatusOK, "history.gohtml", view)
}

// handleRaw serves the bytes of any file in the repository.
func (s *Server) handleRaw(w http.ResponseWriter, r *http.Request) {
	repoPath := strings.Trim(path.Clean("/"+r.PathValue("path")), "/")
	if repoPath == "" || repoPath == "." {
		s.renderError(w, r, http.StatusNotFound, "No file was given.")
		return
	}

	ctx, cancel := s.timeoutFor(r)
	defer cancel()
	data, err := s.store.Raw(ctx, repoPath)
	if err != nil {
		s.renderError(w, r, http.StatusNotFound, "There is no file at "+repoPath+".")
		return
	}

	ctype, inline := contentType(repoPath)
	w.Header().Set("Content-Type", ctype)
	if !inline {
		w.Header().Set("Content-Disposition", `attachment; filename="`+path.Base(repoPath)+`"`)
	}

	modTime := time.Time{}
	if info, ok := s.store.FileInfo(repoPath); ok {
		modTime = info.ModTime
	}
	http.ServeContent(w, r, path.Base(repoPath), modTime, bytes.NewReader(data))
}

// handleHealth reports what the process currently serves. It is a plain GET so
// probes can call it as often as they like.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	stats := s.store.Stats()
	body := map[string]any{
		"status":    "ok",
		"repo":      stats.Root,
		"ref":       refOrWorkingTree(stats.Ref),
		"commit":    stats.Head.Hash,
		"pages":     stats.Pages,
		"files":     stats.Files,
		"loaded_at": stats.LoadedAt.UTC().Format(time.RFC3339),
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := json.NewEncoder(w).Encode(body); err != nil {
		s.log.Error("write health response", "error", err)
	}
}

func (s *Server) handleNotFound(w http.ResponseWriter, r *http.Request) {
	s.renderError(w, r, http.StatusNotFound, "There is nothing at "+r.URL.Path+".")
}

// write renders a template, falling back to a plain error if that fails.
func (s *Server) write(w http.ResponseWriter, r *http.Request, status int, name string, data any) {
	if err := s.tmpl.render(w, status, name, data); err != nil {
		s.log.Error("render template", "template", name, "path", r.URL.Path, "error", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}
}

func (s *Server) renderError(w http.ResponseWriter, r *http.Request, status int, message string) {
	view := errorView{
		layout:  s.layout(r, http.StatusText(status), ""),
		Status:  status,
		Message: message,
	}
	if err := s.tmpl.render(w, status, "error.gohtml", view); err != nil {
		s.log.Error("render error page", "error", err)
		http.Error(w, message, status)
	}
}

// matchesETag implements the If-None-Match comparison for our strong tags.
func matchesETag(header, etag string) bool {
	if header == "" {
		return false
	}
	for _, candidate := range strings.Split(header, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "*" || candidate == etag || strings.TrimPrefix(candidate, "W/") == etag {
			return true
		}
	}
	return false
}

// inlineTypes are the content types safe to display in the browser. Anything
// else is offered as a download, so the repository cannot serve active content
// such as HTML from the wiki's own origin.
var inlineTypes = map[string]string{
	".md":       "text/plain; charset=utf-8",
	".markdown": "text/plain; charset=utf-8",
	".mdown":    "text/plain; charset=utf-8",
	".mkd":      "text/plain; charset=utf-8",
	".txt":      "text/plain; charset=utf-8",
	".csv":      "text/plain; charset=utf-8",
	".json":     "application/json; charset=utf-8",
	".png":      "image/png",
	".jpg":      "image/jpeg",
	".jpeg":     "image/jpeg",
	".gif":      "image/gif",
	".webp":     "image/webp",
	".avif":     "image/avif",
	".svg":      "image/svg+xml",
	".ico":      "image/x-icon",
	".pdf":      "application/pdf",
}

func contentType(p string) (ctype string, inline bool) {
	if t, ok := inlineTypes[strings.ToLower(path.Ext(p))]; ok {
		return t, true
	}
	return "application/octet-stream", false
}
