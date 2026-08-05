package server

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"sort"
	"strconv"
	"strings"
)

// asset is one embedded static file, held in the encodings the server can
// send. Compressing at start-up rather than per request costs a few
// milliseconds once and nothing afterwards, because the files are baked into
// the binary and never change while it runs.
type asset struct {
	contentType string
	plain       []byte
	// gzipped is nil when compression does not make the file smaller.
	gzipped []byte
	etag    string
}

// assets serves the embedded static files.
//
// This is where "minification" happens, and it deliberately is not
// minification: the standard library already implements the standard way to
// make a stylesheet smaller over the wire, so the bytes are gzipped with
// compress/gzip and sent with Content-Encoding. That beats stripping
// whitespace, needs no dependency, and involves no home-made compressor whose
// bugs would silently corrupt the page.
type assets struct {
	files map[string]asset
	// version is a hash of every file, used to cache-bust asset URLs.
	version string
}

// newAssets prepares the embedded files plus any generated ones, whose
// contents depend on configuration and so cannot be embedded.
func newAssets(fsys fs.FS, generated map[string][]byte) (*assets, error) {
	files := make(map[string]asset)

	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := fs.ReadFile(fsys, p)
		if err != nil {
			return fmt.Errorf("read asset %s: %w", p, err)
		}
		files[p], err = prepareAsset(p, data)
		return err
	})
	if err != nil {
		return nil, err
	}
	for name, data := range generated {
		if files[name], err = prepareAsset(name, data); err != nil {
			return nil, err
		}
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no static assets found")
	}

	// The version covers every file, so a changed brand colour invalidates
	// the browser's copy. Names are hashed in order to keep it reproducible.
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)

	all := sha256.New()
	for _, name := range names {
		all.Write([]byte(name))
		all.Write(files[name].plain)
	}

	return &assets{files: files, version: hex.EncodeToString(all.Sum(nil))[:12]}, nil
}

func prepareAsset(name string, data []byte) (asset, error) {
	sum := sha256.Sum256(data)
	a := asset{
		contentType: contentTypeFor(name),
		plain:       data,
		etag:        `"` + hex.EncodeToString(sum[:16]) + `"`,
	}

	gz, err := gzipBytes(data)
	if err != nil {
		return asset{}, fmt.Errorf("compress asset %s: %w", name, err)
	}
	// Tiny files can grow: the gzip header costs more than it saves.
	if len(gz) < len(data) {
		a.gzipped = gz
	}
	return a, nil
}

// ServeHTTP writes an asset, gzipped when the client accepts it.
func (a *assets) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	file, ok := a.files[name]
	if !ok {
		http.NotFound(w, r)
		return
	}

	h := w.Header()
	h.Set("Content-Type", file.contentType)
	h.Set("ETag", file.etag)
	// The body depends on the request's Accept-Encoding, so a shared cache
	// must not serve a gzipped copy to a client that cannot read it.
	h.Set("Vary", "Accept-Encoding")

	if matchesETag(r.Header.Get("If-None-Match"), file.etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	body := file.plain
	if file.gzipped != nil && acceptsGzip(r) {
		h.Set("Content-Encoding", "gzip")
		body = file.gzipped
	}
	h.Set("Content-Length", strconv.Itoa(len(body)))

	// net/http drops the body for HEAD on its own.
	if _, err := w.Write(body); err != nil {
		return
	}
}

// gzipBytes compresses data with the standard library at its best setting.
func gzipBytes(data []byte) ([]byte, error) {
	var buf bytes.Buffer
	zw, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if err != nil {
		return nil, err
	}
	if _, err := zw.Write(data); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// acceptsGzip reports whether the client asked for gzip, honouring an
// explicit "gzip;q=0" refusal.
func acceptsGzip(r *http.Request) bool {
	for _, part := range strings.Split(r.Header.Get("Accept-Encoding"), ",") {
		token, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		if !strings.EqualFold(token, "gzip") && token != "*" {
			continue
		}
		if q, found := strings.CutPrefix(strings.TrimSpace(params), "q="); found {
			if v, err := strconv.ParseFloat(strings.TrimSpace(q), 64); err == nil && v == 0 {
				continue
			}
		}
		return true
	}
	return false
}

func contentTypeFor(name string) string {
	if t := mime.TypeByExtension(path.Ext(name)); t != "" {
		return t
	}
	return "application/octet-stream"
}
