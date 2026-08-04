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
| `-repo` | `WACKY_REPO` | `.` | Repository (or sub-directory) to serve |
| `-ref` | `WACKY_REF` | working tree | Serve a pinned branch, tag or commit |
| `-title` | `WACKY_TITLE` | directory name | Site title |
| `-owner` | `WACKY_OWNER` | `The Authors` | Copyright holder in the footer |
| `-reload-interval` | `WACKY_RELOAD_INTERVAL` | `15s` | Index rebuild period, `0` disables |
| `-log-level` | `WACKY_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `-max-file-size` | `WACKY_MAX_FILE_SIZE` | `4194304` | Largest file that will be read |
| `-history-limit` | `WACKY_HISTORY_LIMIT` | `30` | Commits shown in the history view |

Timeouts (`-read-timeout`, `-write-timeout`, `-idle-timeout`,
`-shutdown-timeout`, `-git-timeout`) follow the same flag/environment pattern.

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
