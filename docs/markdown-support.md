---
title: Markdown Support
---

# Markdown support

The renderer in `internal/markdown` covers the parts of CommonMark and GitHub
Flavored Markdown that documentation actually uses. It has no dependencies and
is a pure function: the same input always produces the same HTML.

## Blocks

| Syntax | Notes |
|--------|-------|
| `# Heading` … `###### Heading` | Gets a slug `id` and an anchor link |
| `Heading` + `=====` / `-----` | Setext headings |
| Paragraphs | Two trailing spaces or a `\` make a hard break |
| ` ```lang ` fences | Emits `class="language-lang"` |
| Four-space indents | Indented code blocks |
| `> quote` | Nested blocks inside, lazy continuation |
| `- item`, `1. item` | Nesting, `start` attribute, tight and loose lists |
| `- [ ]` / `- [x]` | Task lists with disabled checkboxes |
| `\|a\|b\|` + `\|:--\|--:\|` | Tables with per-column alignment |
| `---`, `***`, `___` | Thematic breaks |
| `---` front matter | Flat `key: value` pairs; `title` wins over the first heading |

## Front matter fields

| Key | Meaning |
|-----|---------|
| `title` | The page title, overriding the first heading |
| `classification` | The label shown in the classification notice, e.g. `Internal Use Only` |
| `classification_level` | A whole number compared against the configured thresholds |

`classification` and `classification_level` only do anything when the server
runs with `-classification-threshold-low` or `-classification-threshold-high`
set; without either, no notice is ever shown. With either set, a page that
declares neither field is reported as not yet rated. Any other keys are kept in
the document's metadata and ignored.


## Inline

`*em*`, `**strong**`, `***both***`, `~~strikethrough~~`, `` `code` ``,
`[link](target)`, `![image](target)`, `<https://autolink>`,
`<someone@example.com>` and backslash escapes.

Underscores inside a word stay literal, so `snake_case_identifiers` survive.

## Links between pages

Relative links are rewritten to the URL the server actually serves:

- `[setup](docs/setup.md)` → `/wacky/docs/setup`
- `[home](../README.md)` → `/`
- `![diagram](diagram.png)` → `/raw/docs/diagram.png`
- `[[Glossary]]` → the page whose title, slug or file name matches

A `[[wiki link]]` with no matching page is still a link, marked as missing and
pointing at a search for that term — broken links stay visible instead of
silently disappearing.

## What is deliberately missing

Raw HTML is **escaped, not rendered**. Repository content is untrusted input,
and a wiki that renders arbitrary HTML from a Git repository hands every person
with commit access a way to inject markup into everyone else's browser. Link
destinations are restricted to `http`, `https`, `mailto` and relative paths for
the same reason.

Reference-style links (`[text][ref]`), footnotes and inline HTML blocks are not
supported.
