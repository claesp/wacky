---
title: Architecture
---

# Architecture

The wiki is five small packages with one-way dependencies and no shared mutable
state beyond a single, atomically swapped index.

## Request flow

1. `cmd/wacky` parses configuration, opens the repository, builds the index and
   starts the server. Nothing else touches global state.
2. `internal/server` routes the request, renders a template into a buffer and
   writes it. Middleware adds panic recovery, request logging and security
   headers.
3. `internal/wiki` answers from the current snapshot: a page lookup, a search, a
   directory listing, or a memoised render.
4. `internal/markdown` converts the file to HTML, rewriting links through a
   resolver the store supplies.
5. `internal/git` is consulted for anything the snapshot does not hold —
   history, and file bytes larger than the index keeps.

## The snapshot

`wiki.Store` holds a pointer to an immutable `snapshot`: the page map, the slug
and title indexes, the file list, the raw sources, the navigation tree and a
render cache.

`Reload` never mutates the live snapshot. It builds a complete replacement and
swaps the pointer under a write lock, so:

- a reload over an unchanged repository is indistinguishable from no reload;
- a failed reload leaves the previous index serving;
- readers hold a consistent view for the whole request, even mid-reload.

The render cache lives *inside* the snapshot rather than beside it. Rendering
depends on which pages exist — that is how `[[wiki links]]` know whether their
target is missing — so a cache that outlived its snapshot could serve links
that no longer make sense.

## Where the interfaces live

`wiki.Source` is declared in `internal/wiki`, the package that consumes it, not
in `internal/git`, the package that implements it. The store can therefore be
tested against an in-memory map with no repository, no subprocesses and no
temporary directories, which is what `store_test.go` does.

## Concurrency

- `Store` guards its snapshot pointer with an `RWMutex`; the render cache has
  its own mutex so rendering never blocks a reload.
- The background reloader is a goroutine bound to the server's context. It stops
  before `Shutdown` begins, so no reload can outlive the process.
- `git.Repository` holds no mutable state; every call runs its own subprocess
  under a timeout.
