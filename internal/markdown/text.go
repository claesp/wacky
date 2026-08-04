package markdown

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// --- line classification -----------------------------------------------------

// countIndent returns the indentation width of a line, counting a tab as four
// columns.
func countIndent(line string) int {
	n := 0
	for i := 0; i < len(line); i++ {
		switch line[i] {
		case ' ':
			n++
		case '\t':
			n += 4
		default:
			return n
		}
	}
	return n
}

// dedent removes up to n columns of leading whitespace.
func dedent(line string, n int) string {
	i, col := 0, 0
	for i < len(line) && col < n {
		switch line[i] {
		case ' ':
			col++
		case '\t':
			col += 4
		default:
			return line[i:]
		}
		i++
	}
	return line[i:]
}

func isFence(trimmed string) bool {
	return runLength(trimmed, 0, '`') >= 3 || runLength(trimmed, 0, '~') >= 3
}

// isThematicBreak matches ---, ***, ___ and spaced variants.
func isThematicBreak(trimmed string) bool {
	if len(trimmed) < 3 {
		return false
	}
	c := trimmed[0]
	if c != '-' && c != '*' && c != '_' {
		return false
	}
	count := 0
	for i := 0; i < len(trimmed); i++ {
		switch trimmed[i] {
		case c:
			count++
		case ' ', '\t':
		default:
			return false
		}
	}
	return count >= 3
}

// headingLevel returns 1-6 for an ATX heading, or 0.
func headingLevel(trimmed string) int {
	n := runLength(trimmed, 0, '#')
	if n == 0 || n > 6 {
		return 0
	}
	if n == len(trimmed) {
		return n
	}
	if trimmed[n] == ' ' || trimmed[n] == '\t' {
		return n
	}
	return 0
}

// setextLevel returns 1 for "===" and 2 for "---" underlines, or 0.
func setextLevel(trimmed string) int {
	if trimmed == "" {
		return 0
	}
	if strings.Trim(trimmed, "=") == "" {
		return 1
	}
	if strings.Trim(trimmed, "-") == "" && len(trimmed) >= 2 {
		return 2
	}
	return 0
}

// --- lists -------------------------------------------------------------------

// marker describes the bullet or number that opens a list item.
type marker struct {
	ordered       bool
	start         int
	indent        int // columns of leading whitespace
	contentIndent int // column at which the item's content starts
	width         int // bytes of the line consumed by whitespace and marker
}

func isListStart(line string) bool {
	if isThematicBreak(strings.TrimSpace(line)) {
		return false
	}
	_, ok := parseMarker(line)
	return ok
}

// parseMarker recognises "- ", "* ", "+ ", "1. " and "1) " item openers.
func parseMarker(line string) (marker, bool) {
	var m marker
	i := 0
	for i < len(line) && (line[i] == ' ' || line[i] == '\t') {
		if line[i] == '\t' {
			m.indent += 4
		} else {
			m.indent++
		}
		i++
	}
	if m.indent >= 4 || i >= len(line) {
		return m, false
	}

	if c := line[i]; c == '-' || c == '*' || c == '+' {
		if i+1 < len(line) && line[i+1] != ' ' && line[i+1] != '\t' {
			return m, false
		}
		m.start = 1
		m.width = i + 1
		m.contentIndent = m.indent + 2
		return m, true
	}

	j := i
	for j < len(line) && line[j] >= '0' && line[j] <= '9' {
		j++
	}
	if j == i || j-i > 9 || j >= len(line) || (line[j] != '.' && line[j] != ')') {
		return m, false
	}
	if j+1 < len(line) && line[j+1] != ' ' && line[j+1] != '\t' {
		return m, false
	}
	n, err := strconv.Atoi(line[i:j])
	if err != nil {
		return m, false
	}
	m.ordered = true
	m.start = n
	m.width = j + 1
	m.contentIndent = m.indent + (j - i) + 2
	return m, true
}

// --- tables ------------------------------------------------------------------

// isTableStart reports whether lines[i] is a table header followed by a
// delimiter row with the same number of columns.
func isTableStart(lines []string, i int) bool {
	if i+1 >= len(lines) {
		return false
	}
	head, delim := lines[i], lines[i+1]
	if !strings.Contains(head, "|") || !strings.Contains(delim, "|") {
		return false
	}
	cells := splitRow(delim)
	if len(cells) == 0 || len(cells) != len(splitRow(head)) {
		return false
	}
	for _, cell := range cells {
		if !isDelimCell(cell) {
			return false
		}
	}
	return true
}

func isDelimCell(cell string) bool {
	c := strings.TrimSpace(cell)
	c = strings.TrimPrefix(c, ":")
	c = strings.TrimSuffix(c, ":")
	return c != "" && strings.Trim(c, "-") == ""
}

// splitRow splits a table row into trimmed cells, honouring \| escapes.
func splitRow(line string) []string {
	s := strings.TrimSpace(line)
	s = strings.TrimPrefix(s, "|")
	s = strings.TrimSuffix(s, "|")
	if s == "" {
		return nil
	}

	var (
		cells []string
		cur   strings.Builder
	)
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			if i+1 < len(s) && s[i+1] == '|' {
				cur.WriteByte('|')
				i++
				continue
			}
			cur.WriteByte('\\')
		case '|':
			cells = append(cells, strings.TrimSpace(cur.String()))
			cur.Reset()
		default:
			cur.WriteByte(s[i])
		}
	}
	return append(cells, strings.TrimSpace(cur.String()))
}

func parseAlignments(delim string) []string {
	cells := splitRow(delim)
	aligns := make([]string, len(cells))
	for i, cell := range cells {
		c := strings.TrimSpace(cell)
		left, right := strings.HasPrefix(c, ":"), strings.HasSuffix(c, ":")
		switch {
		case left && right:
			aligns[i] = "center"
		case right:
			aligns[i] = "right"
		case left:
			aligns[i] = "left"
		}
	}
	return aligns
}

// --- front matter ------------------------------------------------------------

func normalizeNewlines(s string) string {
	if !strings.ContainsRune(s, '\r') {
		return s
	}
	return strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n")
}

// splitFrontMatter peels a leading "---" delimited block of key: value pairs
// off the document. Only flat scalar keys are understood; anything else is
// ignored rather than rejected.
func splitFrontMatter(s string) (body string, meta map[string]string) {
	lines := strings.Split(s, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return s, map[string]string{}
	}

	meta = map[string]string{}
	for i := 1; i < len(lines); i++ {
		trimmed := strings.TrimSpace(lines[i])
		if trimmed == "---" || trimmed == "..." {
			return strings.Join(lines[i+1:], "\n"), meta
		}
		key, value, found := strings.Cut(trimmed, ":")
		if !found {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		if key != "" {
			meta[key] = trimQuotes(strings.TrimSpace(value))
		}
	}
	// No closing delimiter: the "---" was a thematic break after all.
	return s, map[string]string{}
}

// --- small helpers -----------------------------------------------------------

// runLength counts consecutive occurrences of c starting at i.
func runLength(s string, i int, c byte) int {
	n := 0
	for i+n < len(s) && s[i+n] == c {
		n++
	}
	return n
}

// indexRun finds the next run of exactly width occurrences of c at or after i.
func indexRun(s string, i int, c byte, width int) int {
	for j := i; j < len(s); {
		if s[j] != c {
			j++
			continue
		}
		n := runLength(s, j, c)
		if n == width {
			return j
		}
		j += n
	}
	return -1
}

func firstWord(s string) string {
	if i := strings.IndexAny(s, " \t"); i >= 0 {
		return s[:i]
	}
	return s
}

func isASCIIPunct(c byte) bool {
	return strings.IndexByte("!\"#$%&'()*+,-./:;<=>?@[\\]^_`{|}~", c) >= 0
}

func isWordByte(c byte) bool {
	return c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

func isSchemeLike(s string) bool {
	if s == "" || !(s[0] >= 'a' && s[0] <= 'z' || s[0] >= 'A' && s[0] <= 'Z') {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(isWordByte(c) || c == '+' || c == '.' || c == '-') || c == '_' {
			return false
		}
	}
	return true
}

var reEmail = regexp.MustCompile(`^[^\s@]+@[^\s@.]+\.[^\s@]+$`)

func isEmail(s string) bool { return reEmail.MatchString(s) }

var (
	reImageMarkup = regexp.MustCompile(`!\[([^\]]*)\]\([^)]*\)`)
	reLinkMarkup  = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)
	reWikiMarkup  = regexp.MustCompile(`\[\[([^\]]+)\]\]`)
	reEmphasis    = regexp.MustCompile("[*_~`]")
)

// plainText strips inline markup, for headings in the table of contents, image
// alt text and search snippets.
func plainText(s string) string {
	s = reImageMarkup.ReplaceAllString(s, "$1")
	s = reLinkMarkup.ReplaceAllString(s, "$1")
	s = reWikiMarkup.ReplaceAllStringFunc(s, func(m string) string {
		inner := strings.TrimSuffix(strings.TrimPrefix(m, "[["), "]]")
		if bar := strings.IndexByte(inner, '|'); bar >= 0 {
			return strings.TrimSpace(inner[bar+1:])
		}
		return strings.TrimSpace(inner)
	})
	s = reEmphasis.ReplaceAllString(s, "")
	return strings.TrimSpace(s)
}

// Slug converts arbitrary text into a lowercase, hyphenated anchor.
func Slug(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	lastDash := true
	for _, r := range strings.ToLower(s) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
			lastDash = false
		case !lastDash:
			b.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}
