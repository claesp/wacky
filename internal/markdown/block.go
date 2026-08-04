package markdown

import (
	"fmt"
	"html"
	"strconv"
	"strings"
)

// state is shared by a parser and every child parser it spawns for nested
// content, so that heading anchors stay unique across the whole document.
type state struct {
	slugs    map[string]int
	headings []Heading
}

// parser renders a sequence of lines into out.
type parser struct {
	opts  Options
	state *state
	out   strings.Builder
}

// child returns a parser that writes to its own buffer but shares document
// state with p.
func (p *parser) child() *parser {
	return &parser{opts: p.opts, state: p.state}
}

// blocks is the block-level dispatcher. Each helper consumes one block and
// returns the index of the first line it did not consume.
func (p *parser) blocks(lines []string) {
	for i := 0; i < len(lines); {
		line := lines[i]
		trimmed := strings.TrimSpace(line)
		indent := countIndent(line)

		switch {
		case trimmed == "":
			i++
		case indent >= 4:
			i = p.indentedCode(lines, i)
		case isFence(trimmed):
			i = p.fencedCode(lines, i)
		case isThematicBreak(trimmed):
			p.out.WriteString("<hr>\n")
			i++
		case headingLevel(trimmed) > 0:
			p.atxHeading(trimmed)
			i++
		case strings.HasPrefix(trimmed, ">"):
			i = p.blockquote(lines, i)
		case isTableStart(lines, i):
			i = p.table(lines, i)
		case isListStart(line):
			i = p.list(lines, i)
		default:
			i = p.paragraph(lines, i)
		}
	}
}

// paragraph consumes lines until a blank line or the start of another block,
// handling setext headings and hard line breaks.
func (p *parser) paragraph(lines []string, start int) int {
	var buf []string
	i := start
	for ; i < len(lines); i++ {
		line := lines[i]
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			break
		}
		// A setext underline turns everything collected so far into a heading.
		if len(buf) > 0 {
			if level := setextLevel(trimmed); level > 0 {
				p.emitHeading(level, strings.Join(buf, " "))
				return i + 1
			}
		}
		if len(buf) > 0 && p.interruptsParagraph(lines, i) {
			break
		}
		buf = append(buf, line)
	}
	if len(buf) == 0 {
		return start + 1
	}
	p.out.WriteString("<p>")
	p.out.WriteString(p.inlineLines(buf))
	p.out.WriteString("</p>\n")
	return i
}

// interruptsParagraph reports whether the line at i starts a new block even
// though the previous line was paragraph text.
func (p *parser) interruptsParagraph(lines []string, i int) bool {
	line := lines[i]
	trimmed := strings.TrimSpace(line)
	switch {
	case isFence(trimmed), isThematicBreak(trimmed), headingLevel(trimmed) > 0:
		return true
	case strings.HasPrefix(trimmed, ">"):
		return true
	case isTableStart(lines, i):
		return true
	case isListStart(line):
		// Only a list that starts a new item interrupts; "1) x" mid-sentence
		// is rare enough that this simple rule is a good trade.
		m, _ := parseMarker(line)
		return !m.ordered || m.start == 1
	default:
		return false
	}
}

// inlineLines renders paragraph lines, turning a trailing double space or
// backslash into a hard break.
func (p *parser) inlineLines(lines []string) string {
	var b strings.Builder
	for i, line := range lines {
		hard := strings.HasSuffix(line, "  ") || strings.HasSuffix(line, "\\")
		text := strings.TrimRight(line, " ")
		text = strings.TrimSuffix(text, "\\")
		b.WriteString(p.inline(strings.TrimSpace(text)))
		if i == len(lines)-1 {
			continue
		}
		if hard {
			b.WriteString("<br>\n")
		} else {
			b.WriteString("\n")
		}
	}
	return b.String()
}

func (p *parser) atxHeading(line string) {
	level := headingLevel(line)
	text := strings.TrimSpace(strings.TrimLeft(line, "#"))
	// A trailing run of hashes is decoration: "## Title ##".
	if last := strings.LastIndexFunc(text, func(r rune) bool { return r != '#' }); last < 0 {
		text = ""
	} else if last < len(text)-1 && text[last] == ' ' {
		text = strings.TrimSpace(text[:last])
	}
	p.emitHeading(level, text)
}

func (p *parser) emitHeading(level int, text string) {
	plain := plainText(text)
	id := p.slug(plain)
	p.state.headings = append(p.state.headings, Heading{Level: level, Text: plain, ID: id})

	fmt.Fprintf(&p.out, "<h%d id=%q>%s<a class=\"anchor\" href=\"#%s\" aria-label=\"Link to this section\">#</a></h%d>\n",
		level, id, p.inline(text), html.EscapeString(id), level)
}

// slug derives a stable, unique anchor from heading text.
func (p *parser) slug(text string) string {
	base := Slug(text)
	if base == "" {
		base = "section"
	}
	n := p.state.slugs[base]
	p.state.slugs[base] = n + 1
	if n == 0 {
		return base
	}
	return base + "-" + strconv.Itoa(n)
}

func (p *parser) fencedCode(lines []string, start int) int {
	open := strings.TrimSpace(lines[start])
	marker := open[0]
	width := runLength(open, 0, marker)
	lang := firstWord(strings.TrimSpace(open[width:]))

	var body []string
	i := start + 1
	for ; i < len(lines); i++ {
		trimmed := strings.TrimSpace(lines[i])
		if runLength(trimmed, 0, marker) >= width && strings.Trim(trimmed, string(marker)) == "" {
			i++
			break
		}
		body = append(body, lines[i])
	}

	p.out.WriteString("<pre><code")
	if lang != "" {
		fmt.Fprintf(&p.out, " class=%q", "language-"+Slug(lang))
	}
	p.out.WriteString(">")
	p.out.WriteString(html.EscapeString(strings.Join(body, "\n")))
	if len(body) > 0 {
		p.out.WriteString("\n")
	}
	p.out.WriteString("</code></pre>\n")
	return i
}

func (p *parser) indentedCode(lines []string, start int) int {
	var body []string
	i := start
	for i < len(lines) {
		line := lines[i]
		if strings.TrimSpace(line) == "" {
			// A blank line belongs to the block only if indented code follows.
			j := i
			for j < len(lines) && strings.TrimSpace(lines[j]) == "" {
				j++
			}
			if j >= len(lines) || countIndent(lines[j]) < 4 {
				break
			}
			for ; i < j; i++ {
				body = append(body, "")
			}
			continue
		}
		if countIndent(line) < 4 {
			break
		}
		body = append(body, dedent(line, 4))
		i++
	}

	p.out.WriteString("<pre><code>")
	p.out.WriteString(html.EscapeString(strings.Join(body, "\n")))
	if len(body) > 0 {
		p.out.WriteString("\n")
	}
	p.out.WriteString("</code></pre>\n")
	return i
}

func (p *parser) blockquote(lines []string, start int) int {
	var body []string
	i := start
	for ; i < len(lines); i++ {
		line := lines[i]
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, ">") {
			body = append(body, strings.TrimPrefix(strings.TrimPrefix(trimmed, ">"), " "))
			continue
		}
		if trimmed == "" {
			break
		}
		// Lazy continuation: an unmarked line still belongs to the quote.
		body = append(body, line)
	}

	child := p.child()
	child.blocks(body)
	p.out.WriteString("<blockquote>\n")
	p.out.WriteString(child.out.String())
	p.out.WriteString("</blockquote>\n")
	return i
}

// list consumes one list, including nested content, and returns the first
// unconsumed line.
func (p *parser) list(lines []string, start int) int {
	first, _ := parseMarker(lines[start])

	var (
		items  [][]string
		cur    []string
		loose  bool
		blanks int
	)
	flush := func() {
		if cur != nil {
			items = append(items, cur)
			cur = nil
		}
	}

	i := start
scan:
	for i < len(lines) {
		line := lines[i]
		if strings.TrimSpace(line) == "" {
			blanks++
			i++
			continue
		}
		marker, isItem := parseMarker(line)
		switch {
		case isItem && marker.indent <= first.indent+1 && marker.ordered == first.ordered:
			if blanks > 0 && len(items) > 0 {
				loose = true
			}
			flush()
			cur = []string{strings.TrimPrefix(line[marker.width:], " ")}
			blanks = 0
		case cur != nil && (countIndent(line) >= first.contentIndent || blanks == 0):
			// Indented continuation, or a lazy paragraph continuation line.
			if blanks > 0 {
				loose = true
				cur = append(cur, "")
			}
			cur = append(cur, dedent(line, first.contentIndent))
			blanks = 0
		default:
			break scan
		}
		i++
	}
	flush()

	tag := "ul"
	openTag := "<ul>"
	if first.ordered {
		tag = "ol"
		openTag = "<ol>"
		if first.start != 1 {
			openTag = `<ol start="` + strconv.Itoa(first.start) + `">`
		}
	}

	p.out.WriteString(openTag + "\n")
	for _, item := range items {
		p.writeItem(item, loose)
	}
	p.out.WriteString("</" + tag + ">\n")
	return i
}

func (p *parser) writeItem(item []string, loose bool) {
	prefix := ""
	if len(item) > 0 {
		if box, rest, ok := taskBox(item[0]); ok {
			prefix = box
			item[0] = rest
		}
	}

	child := p.child()
	child.blocks(item)
	content := child.out.String()
	if !loose {
		content = unwrapParagraph(content)
	}

	p.out.WriteString("<li")
	if prefix != "" {
		p.out.WriteString(` class="task"`)
	}
	p.out.WriteString(">")
	p.out.WriteString(prefix)
	p.out.WriteString(strings.TrimRight(content, "\n"))
	p.out.WriteString("</li>\n")
}

// taskBox recognises a "- [ ]" / "- [x]" item and returns the checkbox HTML.
func taskBox(line string) (box, rest string, ok bool) {
	switch {
	case strings.HasPrefix(line, "[ ] "):
		return `<input type="checkbox" disabled> `, line[4:], true
	case strings.HasPrefix(line, "[x] "), strings.HasPrefix(line, "[X] "):
		return `<input type="checkbox" checked disabled> `, line[4:], true
	default:
		return "", line, false
	}
}

// unwrapParagraph removes the <p> wrapper from the first paragraph of a tight
// list item, leaving any nested blocks that follow it untouched.
func unwrapParagraph(s string) string {
	t := strings.TrimLeft(s, " \t\n")
	if !strings.HasPrefix(t, "<p>") {
		return s
	}
	end := strings.Index(t, "</p>")
	if end < 0 {
		return s
	}
	if strings.Contains(t[len("<p>"):end], "<p>") {
		return s
	}
	return t[len("<p>"):end] + t[end+len("</p>"):]
}

func (p *parser) table(lines []string, start int) int {
	header := splitRow(lines[start])
	aligns := parseAlignments(lines[start+1])

	p.out.WriteString("<div class=\"table-wrap\">\n<table>\n<thead>\n<tr>")
	for i, cell := range header {
		p.out.WriteString("<th" + alignAttr(aligns, i) + ">" + p.inline(cell) + "</th>")
	}
	p.out.WriteString("</tr>\n</thead>\n")

	i := start + 2
	var body strings.Builder
	for ; i < len(lines); i++ {
		line := lines[i]
		if strings.TrimSpace(line) == "" || !strings.Contains(line, "|") {
			break
		}
		cells := splitRow(line)
		body.WriteString("<tr>")
		for c := 0; c < len(header); c++ {
			cell := ""
			if c < len(cells) {
				cell = cells[c]
			}
			body.WriteString("<td" + alignAttr(aligns, c) + ">" + p.inline(cell) + "</td>")
		}
		body.WriteString("</tr>\n")
	}
	if body.Len() > 0 {
		p.out.WriteString("<tbody>\n" + body.String() + "</tbody>\n")
	}
	p.out.WriteString("</table>\n</div>\n")
	return i
}

func alignAttr(aligns []string, i int) string {
	if i >= len(aligns) || aligns[i] == "" {
		return ""
	}
	return ` style="text-align:` + aligns[i] + `"`
}
