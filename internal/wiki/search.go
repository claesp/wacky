package wiki

import (
	"html"
	"html/template"
	"sort"
	"strings"
	"unicode"
)

// Result is one hit of a search, ordered by descending Score.
type Result struct {
	Page  *Page
	Score int
	// Snippet is the matching line with the query highlighted.
	Snippet template.HTML
}

// snippetRadius is how much context is kept around a match, in bytes.
const snippetRadius = 90

// Search performs a case-insensitive substring search over page titles, paths
// and bodies. It is intentionally simple: the index is small enough that a
// linear scan over a snapshot costs less than maintaining an inverted index,
// and results are deterministic for a given snapshot.
func (s *Store) Search(query string, limit int) []Result {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return nil
	}
	if limit <= 0 {
		limit = 50
	}

	snap := s.current()
	results := make([]Result, 0, 16)
	for _, page := range snap.sorted {
		src := snap.source[page.Path]
		body := string(src)
		lowerBody := strings.ToLower(body)

		score := 0
		if strings.Contains(strings.ToLower(page.Title), q) {
			score += 50
		}
		if strings.Contains(strings.ToLower(page.Slug), q) {
			score += 20
		}
		hits := strings.Count(lowerBody, q)
		score += hits

		if score == 0 {
			continue
		}
		results = append(results, Result{
			Page:    page,
			Score:   score,
			Snippet: snippet(body, lowerBody, q),
		})
	}

	sort.SliceStable(results, func(i, j int) bool {
		if results[i].Score != results[j].Score {
			return results[i].Score > results[j].Score
		}
		return results[i].Page.Path < results[j].Page.Path
	})
	if len(results) > limit {
		results = results[:limit]
	}
	return results
}

// snippet returns an escaped excerpt around the first match, with the match
// wrapped in <mark>.
func snippet(body, lowerBody, q string) template.HTML {
	idx := strings.Index(lowerBody, q)
	if idx < 0 {
		return template.HTML(html.EscapeString(firstLine(body)))
	}

	start := trimToBoundary(body, max(0, idx-snippetRadius), true)
	end := trimToBoundary(body, min(len(body), idx+len(q)+snippetRadius), false)

	var b strings.Builder
	if start > 0 {
		b.WriteString("&hellip;")
	}
	b.WriteString(html.EscapeString(collapseSpace(body[start:idx])))
	b.WriteString("<mark>")
	b.WriteString(html.EscapeString(body[idx : idx+len(q)]))
	b.WriteString("</mark>")
	b.WriteString(html.EscapeString(collapseSpace(body[idx+len(q) : end])))
	if end < len(body) {
		b.WriteString("&hellip;")
	}
	return template.HTML(b.String()) //nolint:gosec // all interpolated text is escaped above
}

// trimToBoundary moves an offset to the nearest space so words stay intact.
func trimToBoundary(s string, offset int, forward bool) int {
	if offset <= 0 {
		return 0
	}
	if offset >= len(s) {
		return len(s)
	}
	limit := 20
	for i := 0; i < limit; i++ {
		if unicode.IsSpace(rune(s[offset])) {
			return offset
		}
		if forward {
			offset++
			if offset >= len(s) {
				return len(s)
			}
			continue
		}
		offset--
		if offset <= 0 {
			return 0
		}
	}
	return offset
}

func collapseSpace(s string) string { return strings.Join(strings.Fields(s), " ") }

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
