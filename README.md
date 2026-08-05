# wacky — a Git-backed wiki in Go

A read-only wiki server. Point it at a Git repository and it serves every
Markdown file in it as a website: navigation tree, search, per-file history,
raw sources and images.

Written entirely in Go, with **no third-party dependencies** — the Markdown
renderer, the templates and the Git access are all part of this repository, and
the whole site is served without a single line of JavaScript.

## Quick start

```bash
go run ./cmd/wacky /path/to/a/git/repository
```

Then open <http://127.0.0.1:8080>. To serve this project's own documentation:

```bash
git init && git add -A && go run ./cmd/wacky .
```

Build a standalone binary — templates and stylesheet are embedded, so it is the
only file you need to deploy:

```bash
go build -o wacky ./cmd/wacky
```

## Usage

```
wacky [flags] [repository-path]
```

| Flag | Environment | Default | Meaning |
|------|-------------|---------|---------|
| `-addr` | `WACKY_ADDR` | `127.0.0.1:8080` | Listen address |
| `-git-repo` | `WACKY_GIT_REPO` | `.` | Repository (or sub-directory) to serve |
| `-git-ref` | `WACKY_GIT_REF` | working tree | Serve a pinned branch, tag or commit |
| `-brand-title` | `WACKY_BRAND_TITLE` | `Wacky` | Site title |
| `-brand-color` | `WACKY_BRAND_COLOR` | `#1f5fa8` | Header colour as an RGB hex string; the gradient and brand text derive from it |
| `-brand-image-url` | `WACKY_BRAND_IMAGE_URL` | none | Header logo, relative or `https` URL; replaces the title text |
| `-brand-image-data` | `WACKY_BRAND_IMAGE_DATA` | none | Header logo as base64 image data; wins over `-brand-image-url` |
| `-owner` | `WACKY_OWNER` | `The Authors` | Copyright holder in the footer |
| `-git-commit-url` | `WACKY_GIT_COMMIT_URL` | none | Base URL a commit hash is appended to, e.g. `https://github.com/org/repo/commit/` |
| `-classification-threshold-low` | `WACKY_CLASSIFICATION_THRESHOLD_LOW` | unset | `classification_level` at which a page carries a notice |
| `-classification-threshold-high` | `WACKY_CLASSIFICATION_THRESHOLD_HIGH` | unset | `classification_level` at which that notice becomes severe |
| `-reload-interval` | `WACKY_RELOAD_INTERVAL` | `15s` | Index rebuild period, `0` disables |
| `-log-level` | `WACKY_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `-max-file-size` | `WACKY_MAX_FILE_SIZE` | `4194304` | Largest file that will be read |
| `-git-history-limit` | `WACKY_GIT_HISTORY_LIMIT` | `30` | Commits shown in the history view |

Timeouts (`-read-timeout`, `-write-timeout`, `-idle-timeout`,
`-shutdown-timeout`, `-git-timeout`) follow the same flag/environment pattern.

### Branding

The header takes its gradient from `-brand-color` and picks the text colour
that stays legible against it. `-brand-title` names the site; either logo
setting replaces that text with an image, scaled to fit the bar.

A logo can be a URL the browser fetches:

```bash
wacky -brand-image-url /raw/assets/logo.svg ~/notes
```

…or the image itself, so the page carries no extra request:

```bash
wacky -brand-image-data "$(base64 < logo.svg | tr -d '\n')" ~/notes
```

That flag also takes a literal string. This one is a 223-byte SVG wordmark,
ready to paste:

```bash
wacky -brand-image-data "PHN2ZyB4bWxucz0iaHR0cDovL3d3dy53My5vcmcvMjAwMC9zdmciIHdpZHRoPSIxMjAiIGhlaWdodD0iMjgiPjxyZWN0IHdpZHRoPSIxMjAiIGhlaWdodD0iMjgiIHJ4PSI0IiBmaWxsPSIjMWY1ZmE4Ii8+PHRleHQgeD0iOCIgeT0iMjAiIGZvbnQtZmFtaWx5PSJIZWx2ZXRpY2EiIGZvbnQtc2l6ZT0iMTYiIGZvbnQtd2VpZ2h0PSI3MDAiIGZpbGw9IiNmZmYiPldhY2t5PC90ZXh0Pjwvc3ZnPg==" ~/notes
```

The value may be bare base64 or a full `data:` URI, wrapped or not — the
format is detected from the decoded bytes (PNG, JPEG, GIF, WebP and SVG),
and the image is capped at 256 KiB because it ships with every page. When
both logo settings are given, the inline data wins.

Pointing at a sub-directory narrows the wiki to that sub-tree — `wacky ~/code/app/docs`
serves `docs/` as the site root, and the rest of the repository stays invisible.

## Routes

| Route | Purpose |
|-------|---------|
| `/` | The repository's `README.md`, `index.md` or `Home.md` |
| `/wacky/{path}` | A page, or a listing for a directory without an index |
| `/raw/{path}` | The bytes of any file: sources, images, attachments |
| `/history/{path}` | The commits that touched a file |
| `/pages` | Every page in the wiki |
| `/search?q=` | Full-text search over titles and bodies |
| `/healthz` | JSON status: page count, indexed commit, load time |
| `/static/{path}` | The embedded stylesheet |

## Layout

```text
.
├── cmd/wacky/           # main: flags, dependency wiring, signal handling
├── internal/
│   ├── config/          # flag + environment parsing and validation
│   ├── git/             # read-only repository access via the git binary
│   ├── markdown/        # dependency-free Markdown → HTML renderer
│   ├── wacky/           # domain: pages, slugs, navigation tree, search
│   └── server/          # routing, handlers, middleware, template set
├── docs/                # this project's documentation (and demo content)
└── web/
    ├── templates/       # layout, partials and one template per page type
    └── static/          # stylesheet
```

`internal/` keeps the packages unimportable from outside the module, so the
package boundaries stay free to change. Dependencies point one way:
`server → wacky → {markdown, git} → config`.

## Design notes

**Idempotent by construction.** Every route is a safe `GET`; anything else is
answered with `405`. The server never writes to the repository, so a request
can be repeated, retried, prefetched or cached without changing anything.
`Store.Reload` rebuilds a complete index and swaps it in atomically, which
makes reloading an unchanged repository a no-op and keeps readers from seeing a
half-built index. Pages carry an `ETag` derived from their content hash, so a
repeated request costs a `304`.

**Repository content is untrusted.** The renderer escapes raw HTML instead of
passing it through, restricts link schemes to `http`, `https` and `mailto`, and
the responses carry `Content-Security-Policy: script-src 'none'`. Files with
unrecognised types are sent as attachments rather than rendered inline, and
path traversal is rejected before any file is opened — including through
symlinks.

**Why the `git` binary rather than a library.** It keeps the module free of
dependencies and gets `.gitignore` handling, revision resolution and history
for free. Every invocation runs with a fixed environment and a timeout.

## Tests

```bash
go test ./...
```

The Git tests build a throwaway repository in a temporary directory and skip
themselves when `git` is not installed.
