package server

import (
	"compress/gzip"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

// The stylesheet is sent gzipped to clients that accept it, and the bytes that
// arrive must decompress back to exactly what is embedded.
func TestStaticIsGzipped(t *testing.T) {
	srv := newTestServer(t)

	plain := get(t, srv, "/static/style.css")
	if plain.Code != http.StatusOK {
		t.Fatalf("status = %d", plain.Code)
	}
	if enc := plain.Header().Get("Content-Encoding"); enc != "" {
		t.Errorf("Content-Encoding = %q without an Accept-Encoding header", enc)
	}

	zipped := get(t, srv, "/static/style.css", [2]string{"Accept-Encoding", "gzip, deflate, br"})
	if enc := zipped.Header().Get("Content-Encoding"); enc != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", enc)
	}

	// Measure before decompressing: reading the body empties the buffer.
	plainSize, gzipSize := plain.Body.Len(), zipped.Body.Len()

	zr, err := gzip.NewReader(zipped.Body)
	if err != nil {
		t.Fatalf("the body is not valid gzip: %v", err)
	}
	decoded, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("decompress: %v", err)
	}
	if string(decoded) != plain.Body.String() {
		t.Error("the gzipped body does not decompress to the plain one")
	}

	// The whole point: the compressed form is meaningfully smaller.
	if gzipSize == 0 || gzipSize >= plainSize/2 {
		t.Errorf("gzip saved little: %d bytes from %d", gzipSize, plainSize)
	}
	t.Logf("style.css: %d bytes plain, %d gzipped (%.0f%% saved)",
		plainSize, gzipSize, 100*(1-float64(gzipSize)/float64(plainSize)))
}

// A cache must not hand a gzipped copy to a client that cannot read it.
func TestStaticVaryAndLength(t *testing.T) {
	srv := newTestServer(t)

	for _, accept := range []string{"", "gzip"} {
		rec := get(t, srv, "/static/style.css", [2]string{"Accept-Encoding", accept})
		if v := rec.Header().Get("Vary"); v != "Accept-Encoding" {
			t.Errorf("Accept-Encoding %q: Vary = %q, want Accept-Encoding", accept, v)
		}
		want := strconv.Itoa(rec.Body.Len())
		if got := rec.Header().Get("Content-Length"); got != want {
			t.Errorf("Accept-Encoding %q: Content-Length = %q, want %q", accept, got, want)
		}
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/css") {
			t.Errorf("Content-Type = %q, want text/css", ct)
		}
	}
}

// "gzip;q=0" is a refusal, and an unknown encoding is not gzip.
func TestAcceptsGzip(t *testing.T) {
	tests := map[string]bool{
		"":                       false,
		"gzip":                   true,
		"GZIP":                   true,
		" gzip ":                 true,
		"deflate, gzip":          true,
		"gzip;q=1.0, deflate":    true,
		"br, deflate":            false,
		"gzip;q=0":               false,
		"gzip;q=0.0":             false,
		"deflate;q=1, gzip;q=0":  false,
		"*":                      true,
		"identity;q=1, gzip;q=0": false,
	}

	for header, want := range tests {
		r, err := http.NewRequest(http.MethodGet, "/static/style.css", nil)
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("Accept-Encoding", header)
		if got := acceptsGzip(r); got != want {
			t.Errorf("acceptsGzip(%q) = %v, want %v", header, got, want)
		}
	}
}

// Assets revalidate by ETag, so an unchanged binary costs a 304.
func TestStaticConditionalGet(t *testing.T) {
	srv := newTestServer(t)

	first := get(t, srv, "/static/style.css")
	etag := first.Header().Get("ETag")
	if etag == "" {
		t.Fatal("no ETag on a static asset")
	}

	second := get(t, srv, "/static/style.css", [2]string{"If-None-Match", etag})
	if second.Code != http.StatusNotModified {
		t.Errorf("conditional GET = %d, want 304", second.Code)
	}
	if second.Body.Len() != 0 {
		t.Errorf("304 carried %d bytes", second.Body.Len())
	}
}

func TestStaticNotFound(t *testing.T) {
	if rec := get(t, newTestServer(t), "/static/nope.css"); rec.Code != http.StatusNotFound {
		t.Errorf("GET /static/nope.css = %d, want 404", rec.Code)
	}
}

// The stylesheet is read by machines only.
func TestStylesheetHasNoComments(t *testing.T) {
	css := get(t, newTestServer(t), "/static/style.css").Body.String()

	if strings.Contains(css, "/*") || strings.Contains(css, "*/") {
		t.Error("the stylesheet still contains comments")
	}
}
