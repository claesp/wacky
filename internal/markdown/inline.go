package markdown

import (
	"html"
	"strings"
)

// inline renders the span-level constructs of a single chunk of text. Every
// literal run is HTML-escaped, so source HTML is shown, never executed.
func (p *parser) inline(s string) string {
	var b strings.Builder
	b.Grow(len(s) + len(s)/4)

	for i := 0; i < len(s); {
		switch c := s[i]; c {
		case '\\':
			if i+1 < len(s) && isASCIIPunct(s[i+1]) {
				b.WriteString(html.EscapeString(s[i+1 : i+2]))
				i += 2
				continue
			}
			b.WriteString(`\`)
			i++

		case '`':
			if out, next, ok := p.codeSpan(s, i); ok {
				b.WriteString(out)
				i = next
				continue
			}
			b.WriteString("`")
			i++

		case '!':
			if i+1 < len(s) && s[i+1] == '[' {
				if out, next, ok := p.link(s, i+1, true); ok {
					b.WriteString(out)
					i = next
					continue
				}
			}
			b.WriteString("!")
			i++

		case '[':
			if strings.HasPrefix(s[i:], "[[") {
				if out, next, ok := p.wikiLink(s, i); ok {
					b.WriteString(out)
					i = next
					continue
				}
			}
			if out, next, ok := p.link(s, i, false); ok {
				b.WriteString(out)
				i = next
				continue
			}
			b.WriteString("[")
			i++

		case '<':
			if out, next, ok := p.autolink(s, i); ok {
				b.WriteString(out)
				i = next
				continue
			}
			b.WriteString("&lt;")
			i++

		case '*', '_', '~':
			if out, next, ok := p.emphasis(s, i); ok {
				b.WriteString(out)
				i = next
				continue
			}
			b.WriteString(html.EscapeString(s[i : i+1]))
			i++

		default:
			j := i
			for j < len(s) && !isInlineStart(s[j]) {
				j++
			}
			b.WriteString(html.EscapeString(s[i:j]))
			i = j
		}
	}
	return b.String()
}

func isInlineStart(c byte) bool {
	switch c {
	case '\\', '`', '!', '[', '<', '*', '_', '~':
		return true
	default:
		return false
	}
}

// codeSpan renders a backtick-delimited span, matching the opening run length.
func (p *parser) codeSpan(s string, i int) (string, int, bool) {
	width := runLength(s, i, '`')
	closing := indexRun(s, i+width, '`', width)
	if closing < 0 {
		return "", 0, false
	}
	code := s[i+width : closing]
	if len(code) > 1 && strings.HasPrefix(code, " ") && strings.HasSuffix(code, " ") {
		code = code[1 : len(code)-1]
	}
	return "<code>" + html.EscapeString(code) + "</code>", closing + width, true
}

// emphasis renders *em*, **strong**, ***both*** and ~~strikethrough~~.
func (p *parser) emphasis(s string, i int) (string, int, bool) {
	c := s[i]
	if c == '~' {
		if runLength(s, i, '~') < 2 {
			return "", 0, false
		}
		end := strings.Index(s[i+2:], "~~")
		if end <= 0 {
			return "", 0, false
		}
		return "<del>" + p.inline(s[i+2:i+2+end]) + "</del>", i + 2 + end + 2, true
	}
	// Underscores inside a word (snake_case, file_name_here) are literal.
	if c == '_' && i > 0 && isWordByte(s[i-1]) {
		return "", 0, false
	}

	width := min(runLength(s, i, c), 3)
	closing := -1
	for j := i + width; j < len(s); {
		if s[j] == '\\' {
			j += 2
			continue
		}
		if s[j] != c {
			j++
			continue
		}
		run := runLength(s, j, c)
		if run >= width && !(c == '_' && j+run < len(s) && isWordByte(s[j+run])) {
			closing = j
			break
		}
		j += run
	}
	if closing < 0 {
		return "", 0, false
	}
	content := s[i+width : closing]
	if strings.TrimSpace(content) == "" {
		return "", 0, false
	}

	inner := p.inline(content)
	switch width {
	case 1:
		return "<em>" + inner + "</em>", closing + width, true
	case 2:
		return "<strong>" + inner + "</strong>", closing + width, true
	default:
		return "<strong><em>" + inner + "</em></strong>", closing + width, true
	}
}

// link renders [text](dest "title") and its image form. i points at '['.
func (p *parser) link(s string, i int, image bool) (string, int, bool) {
	textEnd := matchDelim(s, i, '[', ']')
	if textEnd < 0 || textEnd+1 >= len(s) || s[textEnd+1] != '(' {
		return "", 0, false
	}
	destEnd := matchDelim(s, textEnd+1, '(', ')')
	if destEnd < 0 {
		return "", 0, false
	}

	text := s[i+1 : textEnd]
	dest, title := splitDestTitle(s[textEnd+2 : destEnd])
	href := p.resolve(dest)
	next := destEnd + 1

	var b strings.Builder
	if image {
		if href == "" {
			return html.EscapeString(plainText(text)), next, true
		}
		b.WriteString(`<img src="` + html.EscapeString(href) + `" alt="` + html.EscapeString(plainText(text)) + `"`)
		writeTitle(&b, title)
		b.WriteString(" loading=\"lazy\">")
		return b.String(), next, true
	}

	inner := p.inline(text)
	if href == "" {
		return inner, next, true
	}
	b.WriteString(`<a href="` + html.EscapeString(href) + `"`)
	writeTitle(&b, title)
	if isExternal(href) {
		b.WriteString(` rel="noopener noreferrer nofollow"`)
	}
	b.WriteString(">" + inner + "</a>")
	return b.String(), next, true
}

// wikiLink renders [[Target]] and [[Target|Label]].
func (p *parser) wikiLink(s string, i int) (string, int, bool) {
	end := strings.Index(s[i+2:], "]]")
	if end < 0 {
		return "", 0, false
	}
	inner := s[i+2 : i+2+end]
	if strings.TrimSpace(inner) == "" {
		return "", 0, false
	}
	next := i + 2 + end + 2

	target, label := inner, inner
	if bar := strings.IndexByte(inner, '|'); bar >= 0 {
		target, label = strings.TrimSpace(inner[:bar]), strings.TrimSpace(inner[bar+1:])
	}
	if p.opts.ResolveWiki == nil {
		return html.EscapeString(label), next, true
	}

	href, exists := p.opts.ResolveWiki(target)
	href = sanitizeURL(href)
	if href == "" {
		return html.EscapeString(label), next, true
	}
	class := "wikilink"
	if !exists {
		class = "wikilink missing"
	}
	return `<a class="` + class + `" href="` + html.EscapeString(href) + `">` + p.inline(label) + `</a>`, next, true
}

// autolink renders <https://example.com> and <someone@example.com>.
func (p *parser) autolink(s string, i int) (string, int, bool) {
	end := strings.IndexByte(s[i:], '>')
	if end <= 1 {
		return "", 0, false
	}
	inner := s[i+1 : i+end]
	if strings.ContainsAny(inner, " \t<") {
		return "", 0, false
	}

	href := inner
	switch {
	case strings.HasPrefix(inner, "http://"), strings.HasPrefix(inner, "https://"):
	case strings.HasPrefix(inner, "mailto:"):
	case isEmail(inner):
		href = "mailto:" + inner
	default:
		return "", 0, false
	}
	return `<a href="` + html.EscapeString(href) + `" rel="noopener noreferrer nofollow">` + html.EscapeString(inner) + `</a>`, i + end + 1, true
}

// resolve hands a destination to the caller-supplied resolver, then filters the
// result down to schemes that are safe to emit.
func (p *parser) resolve(dest string) string {
	dest = strings.TrimSpace(dest)
	if p.opts.ResolveLink != nil {
		dest = p.opts.ResolveLink(dest)
	}
	return sanitizeURL(dest)
}

func writeTitle(b *strings.Builder, title string) {
	if title != "" {
		b.WriteString(` title="` + html.EscapeString(title) + `"`)
	}
}

// sanitizeURL returns the empty string for anything that is not a plain
// relative reference or an http, https or mailto URL.
func sanitizeURL(raw string) string {
	u := strings.TrimSpace(raw)
	if u == "" {
		return ""
	}
	// Control characters have no place in an attribute value.
	if strings.ContainsFunc(u, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return ""
	}
	if strings.HasPrefix(u, "//") {
		return ""
	}
	if strings.HasPrefix(u, "#") || strings.HasPrefix(u, "/") || strings.HasPrefix(u, "?") {
		return u
	}
	if colon := strings.IndexByte(u, ':'); colon > 0 && isSchemeLike(u[:colon]) {
		switch strings.ToLower(u[:colon]) {
		case "http", "https", "mailto":
			return u
		default:
			return ""
		}
	}
	return u
}

func isExternal(href string) bool {
	return strings.HasPrefix(href, "http://") ||
		strings.HasPrefix(href, "https://") ||
		strings.HasPrefix(href, "mailto:")
}

// splitDestTitle separates `dest "title"` inside a link's parentheses.
func splitDestTitle(s string) (dest, title string) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", ""
	}
	if strings.HasPrefix(s, "<") {
		if end := strings.IndexByte(s, '>'); end > 0 {
			return s[1:end], trimQuotes(strings.TrimSpace(s[end+1:]))
		}
	}
	if i := strings.IndexAny(s, " \t"); i >= 0 {
		return s[:i], trimQuotes(strings.TrimSpace(s[i+1:]))
	}
	return s, ""
}

func trimQuotes(s string) string {
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}

// matchDelim returns the index of the delimiter closing the one at start, or -1.
func matchDelim(s string, start int, open, close byte) int {
	depth := 0
	for i := start; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case open:
			depth++
		case close:
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}
