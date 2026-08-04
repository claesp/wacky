package server

import (
	"bytes"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"
)

// templates holds one parsed template set per page. Each set contains the
// layout plus a single page template, which keeps "content" blocks from
// colliding between pages.
type templates struct {
	set map[string]*template.Template
}

func newTemplates(fsys fs.FS) (*templates, error) {
	pages, err := fs.Glob(fsys, "templates/pages/*.gohtml")
	if err != nil {
		return nil, err
	}
	if len(pages) == 0 {
		return nil, fmt.Errorf("no page templates found")
	}

	t := &templates{set: make(map[string]*template.Template, len(pages))}
	for _, page := range pages {
		parsed, err := template.New("base.gohtml").Funcs(funcs).ParseFS(fsys,
			"templates/layout/*.gohtml",
			"templates/partials/*.gohtml",
			page,
		)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", page, err)
		}
		t.set[path.Base(page)] = parsed
	}
	return t, nil
}

// render writes a page. It renders into a buffer first so a template error
// cannot produce a half-written response with a 200 status.
func (t *templates) render(w http.ResponseWriter, status int, name string, data any) error {
	tmpl, ok := t.set[name]
	if !ok {
		return fmt.Errorf("unknown template %q", name)
	}

	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "base.gohtml", data); err != nil {
		return fmt.Errorf("execute %s: %w", name, err)
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, err := buf.WriteTo(w)
	return err
}

// funcs are the helpers available inside templates.
var funcs = template.FuncMap{
	"formatTime": func(t time.Time) string {
		if t.IsZero() {
			return "unknown"
		}
		return t.Format("2006-01-02 15:04")
	},
	"formatDate": func(t time.Time) string {
		if t.IsZero() {
			return "unknown"
		}
		return t.Format("2006-01-02")
	},
	"humanSize": humanSize,
	"pathExt":   path.Ext,
	"lower":     strings.ToLower,
	"dict":      dict,
	"urlPath":   urlPath,
}

// urlPath drops the query string, so a link that carries one can still be
// compared against the request path.
func urlPath(u string) string {
	if i := strings.IndexAny(u, "?#"); i >= 0 {
		return u[:i]
	}
	return u
}

// dict builds a map from alternating key/value arguments, which is how nested
// templates receive more than one value.
func dict(values ...any) (map[string]any, error) {
	if len(values)%2 != 0 {
		return nil, fmt.Errorf("dict: got %d arguments, want an even number", len(values))
	}
	m := make(map[string]any, len(values)/2)
	for i := 0; i < len(values); i += 2 {
		key, ok := values[i].(string)
		if !ok {
			return nil, fmt.Errorf("dict: key %d is %T, want string", i, values[i])
		}
		m[key] = values[i+1]
	}
	return m, nil
}

func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit && exp < 3; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGT"[exp])
}
