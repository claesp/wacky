// Package markdown renders a widely used subset of Markdown to HTML without
// any third-party dependency.
//
// Supported: YAML front matter, ATX and setext headings, paragraphs, hard line
// breaks, fenced and indented code, blockquotes, ordered/unordered/task lists
// with nesting, GitHub-style tables, thematic breaks, emphasis, strong,
// strikethrough, inline code, links, images, autolinks and [[wiki links]].
//
// Rendering is deliberately closed: raw HTML in the source is escaped rather
// than passed through, and link targets are restricted to a small set of
// schemes. Repository content is therefore treated as untrusted data.
//
// A Renderer is stateless and safe for concurrent use. Rendering the same
// input with the same Options always produces byte-identical output.
package markdown

import (
	"html/template"
	"strings"
)

// Document is the result of rendering one Markdown file.
type Document struct {
	// Title is the front matter title, falling back to the first heading.
	Title string
	// HTML is the rendered body.
	HTML template.HTML
	// Lead is the opening heading when the document starts with one, and Rest
	// is everything after it; otherwise Lead is empty and Rest is the whole
	// body. Concatenated they are HTML. The split lets a caller place
	// something between the title and the text, which is how the page
	// template aligns its table of contents with the first paragraph.
	Lead template.HTML
	Rest template.HTML
	// TOC lists the headings in document order.
	TOC []Heading
	// Meta holds the front matter keys, lowercased.
	Meta map[string]string
}

// Heading is one entry of a document's table of contents.
type Heading struct {
	Level int
	Text  string
	ID    string
}

// Options customise a single Render call. The zero value renders links exactly
// as they appear in the source and turns wiki links into plain text.
type Options struct {
	// ResolveLink maps a link or image destination onto the URL to emit. It is
	// called for every destination, including relative ones.
	ResolveLink func(dest string) string
	// ResolveWiki maps a [[wiki link]] target onto a URL, reporting whether a
	// page with that name exists.
	ResolveWiki func(target string) (href string, exists bool)
}

// Renderer converts Markdown to HTML.
type Renderer struct{}

// New returns a Renderer. The zero Renderer is equally usable.
func New() *Renderer { return &Renderer{} }

// Render converts src to HTML.
func (r *Renderer) Render(src []byte, opts Options) Document {
	body, meta := splitFrontMatter(normalizeNewlines(string(src)))

	p := &parser{opts: opts, state: &state{slugs: make(map[string]int)}, top: true}
	p.blocks(strings.Split(body, "\n"))

	html := p.out.String()
	doc := Document{
		HTML: template.HTML(html), //nolint:gosec // every value is escaped during rendering
		Rest: template.HTML(html), //nolint:gosec // same bytes as HTML
		TOC:  p.state.headings,
		Meta: meta,
	}
	if n := p.state.leadEnd; n > 0 && n <= len(html) {
		doc.Lead = template.HTML(html[:n]) //nolint:gosec // same bytes as HTML
		doc.Rest = template.HTML(html[n:]) //nolint:gosec // same bytes as HTML
	}
	doc.Title = documentTitle(meta, p.state.headings)
	return doc
}

// documentTitle prefers an explicit front matter title over the first heading.
func documentTitle(meta map[string]string, headings []Heading) string {
	if t := strings.TrimSpace(meta["title"]); t != "" {
		return t
	}
	for _, h := range headings {
		if h.Level == 1 {
			return h.Text
		}
	}
	if len(headings) > 0 {
		return headings[0].Text
	}
	return ""
}
