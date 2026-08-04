// Package git provides read-only access to a Git repository by shelling out to
// the git binary. Every operation is a query: nothing in this package mutates
// the repository, so calls are safe to repeat.
package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Sentinel errors returned by this package.
var (
	// ErrNotFound reports a path that does not exist in the served revision.
	ErrNotFound = errors.New("git: path not found")
	// ErrOutsideRepo reports a path that escapes the repository root.
	ErrOutsideRepo = errors.New("git: path outside repository")
	// ErrTooLarge reports a file above the configured size limit.
	ErrTooLarge = errors.New("git: file too large")
)

// fieldSep separates fields in the custom log format below. Unit Separator
// cannot appear in a commit subject or author name.
const fieldSep = "\x1f"

// Commit describes a single revision.
type Commit struct {
	Hash    string
	Author  string
	Email   string
	When    time.Time
	Subject string
}

// Short returns the abbreviated commit hash.
func (c Commit) Short() string {
	if len(c.Hash) > 8 {
		return c.Hash[:8]
	}
	return c.Hash
}

// IsZero reports whether the commit is unset, which happens in a repository
// without any commits yet.
func (c Commit) IsZero() bool { return c.Hash == "" }

// File is a blob listed in the served revision.
type File struct {
	Path    string // slash-separated, relative to the repository root
	Size    int64
	ModTime time.Time
}

// Repository is a read-only handle on a Git repository. It is safe for
// concurrent use.
//
// When Open is given a directory below the repository root, that directory
// becomes the served tree: paths handed to and returned by Repository are
// relative to it, and files outside it are invisible.
type Repository struct {
	bin string
	// root is the repository top level, where git commands are run.
	root string
	// prefix is the served directory relative to root, or "" for the whole
	// repository.
	prefix string
	// base is the absolute path of the served directory.
	base    string
	ref     string
	timeout time.Duration
	maxSize int64
}

// Option customises a Repository.
type Option func(*Repository)

// WithRef pins the repository to a revision. When ref is empty the working
// tree is served, including files that are not yet committed.
func WithRef(ref string) Option {
	return func(r *Repository) { r.ref = strings.TrimSpace(ref) }
}

// WithTimeout bounds how long a single git invocation may run.
func WithTimeout(d time.Duration) Option {
	return func(r *Repository) {
		if d > 0 {
			r.timeout = d
		}
	}
}

// WithMaxFileSize caps the size of a file Read will return.
func WithMaxFileSize(n int64) Option {
	return func(r *Repository) {
		if n > 0 {
			r.maxSize = n
		}
	}
}

// Open locates the repository containing dir and verifies that the requested
// revision exists.
func Open(ctx context.Context, dir string, opts ...Option) (*Repository, error) {
	bin, err := exec.LookPath("git")
	if err != nil {
		return nil, fmt.Errorf("git executable not found in PATH: %w", err)
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("resolve %q: %w", dir, err)
	}

	if real, err := filepath.EvalSymlinks(abs); err == nil {
		abs = real
	}

	r := &Repository{
		bin:     bin,
		root:    abs,
		base:    abs,
		timeout: 20 * time.Second,
		maxSize: 4 << 20,
	}
	for _, opt := range opts {
		opt(r)
	}

	out, err := r.run(ctx, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, fmt.Errorf("%q is not inside a Git repository: %w", abs, err)
	}
	root := strings.TrimSpace(out)
	if root == "" {
		return nil, fmt.Errorf("%q is not inside a Git repository", abs)
	}
	// Resolve symlinks once so path containment checks compare like with like.
	if real, err := filepath.EvalSymlinks(root); err == nil {
		root = real
	}
	r.root = root
	r.base = abs

	// A directory below the top level narrows the wiki to that sub-tree.
	if rel, err := filepath.Rel(root, abs); err == nil && rel != "." {
		if strings.HasPrefix(rel, "..") {
			return nil, fmt.Errorf("%q is outside repository %q", abs, root)
		}
		r.prefix = filepath.ToSlash(rel)
	}

	if r.ref != "" {
		if _, err := r.run(ctx, "rev-parse", "--verify", "--quiet", r.ref+"^{commit}"); err != nil {
			return nil, fmt.Errorf("unknown revision %q: %w", r.ref, err)
		}
	}
	return r, nil
}

// Root returns the absolute path of the served directory.
func (r *Repository) Root() string { return r.base }

// Prefix returns the served directory relative to the repository top level, or
// an empty string when the whole repository is served.
func (r *Repository) Prefix() string { return r.prefix }

// repoPath maps a path relative to the served directory onto one relative to
// the repository top level, which is what git commands expect.
func (r *Repository) repoPath(rel string) string {
	if r.prefix == "" {
		return rel
	}
	return path.Join(r.prefix, rel)
}

// pathspec limits a git command to the served directory.
func (r *Repository) pathspec() []string {
	if r.prefix == "" {
		return nil
	}
	return []string{"--", r.prefix}
}

// Ref returns the pinned revision, or an empty string when the working tree is
// served.
func (r *Repository) Ref() string { return r.ref }

// Head returns the commit currently at the tip of the served revision. In an
// empty repository it returns the zero Commit and no error.
func (r *Repository) Head(ctx context.Context) (Commit, error) {
	rev := r.ref
	if rev == "" {
		rev = "HEAD"
	}
	args := append([]string{"log", "-1", "--format=" + logFormat(), rev}, r.pathspec()...)
	out, err := r.run(ctx, args...)
	if err != nil {
		// A repository without commits is a valid, if empty, wiki.
		if _, verifyErr := r.run(ctx, "rev-parse", "--verify", "--quiet", rev); verifyErr != nil {
			return Commit{}, nil
		}
		return Commit{}, err
	}
	commits := parseCommits(out)
	if len(commits) == 0 {
		return Commit{}, nil
	}
	return commits[0], nil
}

// Files lists every file in the served revision. For the working tree that is
// every tracked file plus untracked files that .gitignore does not exclude.
func (r *Repository) Files(ctx context.Context) ([]File, error) {
	var args []string
	if r.ref == "" {
		args = []string{"ls-files", "-z", "--cached", "--others", "--exclude-standard"}
	} else {
		args = []string{"ls-tree", "-r", "-z", "--name-only", r.ref}
	}
	out, err := r.run(ctx, append(args, r.pathspec()...)...)
	if err != nil {
		return nil, fmt.Errorf("list files: %w", err)
	}

	names := strings.Split(out, "\x00")
	files := make([]File, 0, len(names))
	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		if name == "" {
			continue
		}
		name = path.Clean(filepath.ToSlash(name))
		if name == "." || strings.HasPrefix(name, "../") || strings.HasPrefix(name, ".git/") {
			continue
		}
		if r.prefix != "" {
			if !strings.HasPrefix(name, r.prefix+"/") {
				continue
			}
			name = name[len(r.prefix)+1:]
		}
		if _, dup := seen[name]; dup {
			continue
		}
		seen[name] = struct{}{}

		f := File{Path: name}
		if r.ref == "" {
			// Stat is cheap and gives the sidebar useful timestamps. A file
			// that vanished between ls-files and Stat is simply skipped.
			info, statErr := os.Stat(filepath.Join(r.base, filepath.FromSlash(name)))
			if statErr != nil || info.IsDir() {
				continue
			}
			f.Size = info.Size()
			f.ModTime = info.ModTime()
		}
		files = append(files, f)
	}
	return files, nil
}

// Read returns the contents of rel in the served revision.
func (r *Repository) Read(ctx context.Context, rel string) ([]byte, error) {
	clean, err := cleanRel(rel)
	if err != nil {
		return nil, err
	}

	if r.ref != "" {
		out, err := r.runRaw(ctx, "show", r.ref+":"+r.repoPath(clean))
		if err != nil {
			return nil, fmt.Errorf("read %q: %w", clean, ErrNotFound)
		}
		if int64(len(out)) > r.maxSize {
			return nil, fmt.Errorf("read %q (%d bytes): %w", clean, len(out), ErrTooLarge)
		}
		return out, nil
	}

	full, err := r.resolve(clean)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(full)
	if err != nil {
		return nil, fmt.Errorf("read %q: %w", clean, ErrNotFound)
	}
	if info.IsDir() {
		return nil, fmt.Errorf("read %q: %w", clean, ErrNotFound)
	}
	if info.Size() > r.maxSize {
		return nil, fmt.Errorf("read %q (%d bytes): %w", clean, info.Size(), ErrTooLarge)
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return nil, fmt.Errorf("read %q: %w", clean, err)
	}
	return data, nil
}

// Log returns up to limit commits that touched rel, newest first. A file that
// is not committed yet has no history, which is not an error.
func (r *Repository) Log(ctx context.Context, rel string, limit int) ([]Commit, error) {
	clean, err := cleanRel(rel)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 30
	}
	rev := r.ref
	if rev == "" {
		rev = "HEAD"
	}

	out, err := r.run(ctx,
		"log", "--max-count="+strconv.Itoa(limit), "--format="+logFormat(), rev, "--", r.repoPath(clean))
	if err != nil {
		return nil, nil
	}
	return parseCommits(out), nil
}

// resolve converts a path relative to the served directory into an absolute
// one, refusing anything that escapes it, even through symlinks.
func (r *Repository) resolve(clean string) (string, error) {
	full := filepath.Join(r.base, filepath.FromSlash(clean))
	real, err := filepath.EvalSymlinks(full)
	if err != nil {
		return "", fmt.Errorf("resolve %q: %w", clean, ErrNotFound)
	}
	if real != r.base && !strings.HasPrefix(real, r.base+string(filepath.Separator)) {
		return "", fmt.Errorf("resolve %q: %w", clean, ErrOutsideRepo)
	}
	return real, nil
}

// run executes git and returns its standard output as a string.
func (r *Repository) run(ctx context.Context, args ...string) (string, error) {
	out, err := r.runRaw(ctx, args...)
	return string(out), err
}

func (r *Repository) runRaw(ctx context.Context, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	full := append([]string{"-C", r.root, "--no-pager"}, args...)
	cmd := exec.CommandContext(ctx, r.bin, full...)
	// A predictable environment keeps output parseable regardless of the
	// user's global git configuration.
	cmd.Env = append(os.Environ(),
		"GIT_OPTIONAL_LOCKS=0",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_NOSYSTEM=1",
		"LC_ALL=C",
	)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("git %s: %s", strings.Join(args, " "), msg)
	}
	return stdout.Bytes(), nil
}

// logFormat is the --format string parsed by parseCommits.
func logFormat() string {
	f := fieldSep
	return "%H" + f + "%an" + f + "%ae" + f + "%aI" + f + "%s"
}

func parseCommits(out string) []Commit {
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	commits := make([]Commit, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.Split(line, fieldSep)
		if len(parts) < 5 {
			continue
		}
		when, err := time.Parse(time.RFC3339, parts[3])
		if err != nil {
			when = time.Time{}
		}
		commits = append(commits, Commit{
			Hash:    parts[0],
			Author:  parts[1],
			Email:   parts[2],
			When:    when,
			Subject: parts[4],
		})
	}
	return commits
}

// cleanRel normalises a repository-relative path and rejects traversal.
func cleanRel(rel string) (string, error) {
	clean := path.Clean("/" + strings.TrimSpace(filepath.ToSlash(rel)))
	clean = strings.TrimPrefix(clean, "/")
	if clean == "" || clean == "." {
		return "", fmt.Errorf("path %q: %w", rel, ErrNotFound)
	}
	if clean == ".git" || strings.HasPrefix(clean, ".git/") {
		return "", fmt.Errorf("path %q: %w", rel, ErrNotFound)
	}
	return clean, nil
}
