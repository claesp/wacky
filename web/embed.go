// Package web embeds the HTML templates and static assets of the wiki so that
// the compiled binary is self-contained.
package web

import (
	"embed"
	"io/fs"
)

//go:embed templates static
var files embed.FS

// FS returns the embedded asset filesystem, rooted at the repository's web
// directory: templates live under "templates/", assets under "static/".
func FS() fs.FS { return files }

// Static returns the "static" sub-tree, ready to be served over HTTP.
func Static() (fs.FS, error) { return fs.Sub(files, "static") }
