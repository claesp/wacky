package markdown

import (
	"strings"
	"testing"
)

func render(t *testing.T, src string, opts ...Options) Document {
	t.Helper()
	var o Options
	if len(opts) > 0 {
		o = opts[0]
	}
	return New().Render([]byte(src), o)
}

func TestRenderBlocks(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want []string
	}{
		{
			name: "atx heading with anchor",
			src:  "# Getting Started\n",
			want: []string{`<h1 id="getting-started">Getting Started`},
		},
		{
			name: "closing hashes are decoration",
			src:  "## Setup ##\n",
			want: []string{`<h2 id="setup">Setup<a`},
		},
		{
			name: "setext heading",
			src:  "Title\n=====\n",
			want: []string{`<h1 id="title">Title`},
		},
		{
			name: "paragraph",
			src:  "Hello there.\n",
			want: []string{"<p>Hello there.</p>"},
		},
		{
			name: "hard line break",
			src:  "one  \ntwo\n",
			want: []string{"one<br>\ntwo"},
		},
		{
			name: "fenced code keeps content verbatim",
			src:  "```go\nfmt.Println(\"<hi>\")\n```\n",
			want: []string{`<pre><code class="language-go">`, `fmt.Println(&#34;&lt;hi&gt;&#34;)`},
		},
		{
			name: "indented code",
			src:  "    x := 1\n",
			want: []string{"<pre><code>x := 1\n</code></pre>"},
		},
		{
			name: "unordered list",
			src:  "- one\n- two\n",
			want: []string{"<ul>\n<li>one</li>\n<li>two</li>\n</ul>"},
		},
		{
			name: "ordered list with start",
			src:  "3. three\n4. four\n",
			want: []string{`<ol start="3">`, "<li>three</li>"},
		},
		{
			name: "nested list",
			src:  "- outer\n  - inner\n",
			want: []string{"<li>outer\n<ul>\n<li>inner</li>\n</ul></li>"},
		},
		{
			name: "task list",
			src:  "- [x] done\n- [ ] todo\n",
			want: []string{`<input type="checkbox" checked disabled> done`, `<input type="checkbox" disabled> todo`},
		},
		{
			name: "blockquote",
			src:  "> quoted **text**\n",
			want: []string{"<blockquote>\n<p>quoted <strong>text</strong></p>"},
		},
		{
			name: "thematic break",
			src:  "a\n\n---\n\nb\n",
			want: []string{"<hr>"},
		},
		{
			name: "table with alignment",
			src:  "| a | b |\n|:--|--:|\n| 1 | 2 |\n",
			want: []string{`<th style="text-align:left">a</th>`, `<td style="text-align:right">2</td>`},
		},
		{
			name: "escaped pipe stays inside its cell",
			src:  "| a | b |\n|---|---|\n| `x\\|y` | z |\n",
			want: []string{"<td><code>x|y</code></td>", "<td>z</td>"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := string(render(t, tt.src).HTML)
			for _, want := range tt.want {
				if !strings.Contains(got, want) {
					t.Errorf("missing %q in output:\n%s", want, got)
				}
			}
		})
	}
}

func TestRenderInline(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string
	}{
		{"emphasis", "*hi*", "<em>hi</em>"},
		{"strong", "**hi**", "<strong>hi</strong>"},
		{"strong emphasis", "***hi***", "<strong><em>hi</em></strong>"},
		{"strikethrough", "~~gone~~", "<del>gone</del>"},
		{"code span", "`a < b`", "<code>a &lt; b</code>"},
		{"underscore inside word stays literal", "snake_case_name", "snake_case_name"},
		{"escaped star", `\*not emphasis\*`, "*not emphasis*"},
		{"autolink", "<https://example.com>", `<a href="https://example.com"`},
		{"email autolink", "<a@example.com>", `href="mailto:a@example.com"`},
		{"image", "![alt](pic.png)", `<img src="pic.png" alt="alt"`},
		{"link title", `[x](a.html "Tip")`, `title="Tip"`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := string(render(t, tt.src).HTML)
			if !strings.Contains(got, tt.want) {
				t.Errorf("Render(%q) = %q, want it to contain %q", tt.src, got, tt.want)
			}
		})
	}
}

// TestRenderEscapesUntrustedInput locks in that repository content can never
// inject markup or script into a page.
func TestRenderEscapesUntrustedInput(t *testing.T) {
	sources := []string{
		"<script>alert(1)</script>",
		"<img src=x onerror=alert(1)>",
		"[click](javascript:alert(1))",
		"[click](JaVaScRiPt:alert(1))",
		"![x](javascript:alert(1))",
		"<div onmouseover=\"alert(1)\">hi</div>",
		"[x](vbscript:alert(1))",
		"[x](data:text/html;base64,PHN2Zz4=)",
	}

	// Tags that must never appear unescaped. Escaped text such as
	// "&lt;img onerror=..." is inert and therefore fine.
	rawTags := []string{"<script", "<img src=x", "<div", "<svg", "<iframe"}

	for _, src := range sources {
		got := strings.ToLower(string(render(t, src).HTML))
		for _, tag := range rawTags {
			if strings.Contains(got, tag) {
				t.Errorf("Render(%q) emitted raw HTML %q: %s", src, tag, got)
			}
		}
		if strings.Contains(got, "javascript:") || strings.Contains(got, "vbscript:") || strings.Contains(got, "data:text/html") {
			t.Errorf("Render(%q) kept an unsafe URL scheme: %s", src, got)
		}
	}
}

func TestFrontMatter(t *testing.T) {
	doc := render(t, "---\ntitle: Release Notes\nauthor: ada\n---\n\n# Ignored heading\n")

	if doc.Title != "Release Notes" {
		t.Errorf("Title = %q, want %q", doc.Title, "Release Notes")
	}
	if doc.Meta["author"] != "ada" {
		t.Errorf("Meta[author] = %q, want %q", doc.Meta["author"], "ada")
	}
	if strings.Contains(string(doc.HTML), "title: Release Notes") {
		t.Error("front matter leaked into the rendered body")
	}
}

// A leading --- without a closing delimiter is a thematic break, not metadata.
func TestUnterminatedFrontMatter(t *testing.T) {
	doc := render(t, "---\njust text\n")
	if !strings.Contains(string(doc.HTML), "just text") {
		t.Errorf("body was swallowed: %s", doc.HTML)
	}
}

func TestTableOfContentsIsUnique(t *testing.T) {
	doc := render(t, "# Notes\n\n## Setup\n\n## Setup\n")

	if len(doc.TOC) != 3 {
		t.Fatalf("len(TOC) = %d, want 3", len(doc.TOC))
	}
	if doc.TOC[1].ID == doc.TOC[2].ID {
		t.Errorf("duplicate heading IDs: %q", doc.TOC[1].ID)
	}
	if doc.Title != "Notes" {
		t.Errorf("Title = %q, want %q", doc.Title, "Notes")
	}
}

func TestResolveLinkAndWiki(t *testing.T) {
	opts := Options{
		ResolveLink: func(dest string) string { return "/wiki/" + strings.TrimSuffix(dest, ".md") },
		ResolveWiki: func(target string) (string, bool) {
			if target == "Known" {
				return "/wiki/known", true
			}
			return "/search?q=" + target, false
		},
	}

	got := string(render(t, "[see](other.md) [[Known]] [[Missing|label]]", opts).HTML)
	for _, want := range []string{
		`<a href="/wiki/other"`,
		`class="wikilink" href="/wiki/known"`,
		`class="wikilink missing" href="/search?q=Missing"`,
		`>label</a>`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %s", want, got)
		}
	}
}

// Rendering must be a pure function of its input.
func TestRenderIsDeterministic(t *testing.T) {
	src := "# Title\n\nSome *text* with `code`.\n\n- a\n- b\n\n| x | y |\n|---|---|\n| 1 | 2 |\n"

	first := string(render(t, src).HTML)
	for i := 0; i < 5; i++ {
		if got := string(render(t, src).HTML); got != first {
			t.Fatalf("render %d differed:\n%s\n---\n%s", i, first, got)
		}
	}
}

func TestSlug(t *testing.T) {
	tests := map[string]string{
		"Getting Started":   "getting-started",
		"  Spaced  Out  ":   "spaced-out",
		"Symbols!@#$":       "symbols",
		"Ünïcode Wörks":     "ünïcode-wörks",
		"multiple---dashes": "multiple-dashes",
	}
	for in, want := range tests {
		if got := Slug(in); got != want {
			t.Errorf("Slug(%q) = %q, want %q", in, got, want)
		}
	}
}
