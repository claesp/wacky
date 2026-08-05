// Package wacky turns the Markdown files of a Git repository into an
// addressable set of pages.
package wacky

import (
	"path"
	"strings"
	"time"
)

// markdownExts are the file extensions treated as wiki pages.
var markdownExts = map[string]bool{
	".md":       true,
	".markdown": true,
	".mdown":    true,
	".mkd":      true,
}

// indexNames are the file names that stand in for their directory.
var indexNames = map[string]bool{
	"readme": true,
	"index":  true,
	"home":   true,
	"_index": true,
}

// Page is one Markdown file in the repository.
type Page struct {
	// Path is the repository-relative, slash-separated file path.
	Path string
	// Slug is the URL path the page is served at, without a leading slash.
	// A directory index page takes the slug of its directory.
	Slug string
	// Title is the front matter title, the first heading, or the file name.
	Title string
	// Dir is the slash-separated directory the page lives in ("" at the root).
	Dir string
	// IsIndex reports whether the page represents its directory.
	IsIndex bool

	// Classification and ClassificationLevel come from the page's front
	// matter. Rated reports that both were present and the level parsed, which
	// is what the classification notice and the page-link dots key off.
	Classification      string
	ClassificationLevel int
	Rated               bool

	Size    int64
	ModTime time.Time
}

// Name returns the page's file name without its extension.
func (p *Page) Name() string {
	return strings.TrimSuffix(path.Base(p.Path), path.Ext(p.Path))
}

// URL returns the path the page is served at.
func (p *Page) URL() string {
	if p.Slug == "" {
		return "/"
	}
	return "/wacky/" + p.Slug
}

// IsMarkdown reports whether a repository path is a wiki page.
func IsMarkdown(p string) bool {
	return markdownExts[strings.ToLower(path.Ext(p))]
}

// slugFor derives the URL slug of a repository path.
func slugFor(repoPath string) (slug string, isIndex bool) {
	dir, file := path.Split(repoPath)
	dir = strings.Trim(dir, "/")
	name := strings.TrimSuffix(file, path.Ext(file))

	if indexNames[strings.ToLower(name)] {
		return dir, true
	}
	if dir == "" {
		return name, false
	}
	return dir + "/" + name, false
}

// titleFor falls back to a readable title when a page has no heading.
func titleFor(name string) string {
	replaced := strings.NewReplacer("-", " ", "_", " ").Replace(name)
	fields := strings.Fields(replaced)
	for i, f := range fields {
		if len(f) > 0 && f[0] >= 'a' && f[0] <= 'z' {
			fields[i] = strings.ToUpper(f[:1]) + f[1:]
		}
	}
	if len(fields) == 0 {
		return name
	}
	return strings.Join(fields, " ")
}
