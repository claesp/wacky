package git

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// newTestRepo creates a repository with one commit and returns its path.
func newTestRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}

	dir := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
	}

	writeFile(t, dir, "README.md", "# Home\n\nWelcome.\n")
	writeFile(t, dir, filepath.Join("docs", "setup.md"), "# Setup\n")
	writeFile(t, dir, ".gitignore", "ignored.md\n")
	writeFile(t, dir, "ignored.md", "# Ignored\n")

	gitCmd(t, dir, "init", "-b", "main")
	gitCmd(t, dir, "config", "user.email", "test@example.com")
	gitCmd(t, dir, "config", "user.name", "Test User")
	gitCmd(t, dir, "add", "README.md", "docs/setup.md", ".gitignore")
	gitCmd(t, dir, "commit", "-m", "initial commit")
	return dir
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	full := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func gitCmd(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_NOSYSTEM=1",
		"HOME="+dir,
		"GIT_AUTHOR_NAME=Test User",
		"GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test User",
		"GIT_COMMITTER_EMAIL=test@example.com",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func TestOpenRejectsNonRepository(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	if _, err := Open(context.Background(), t.TempDir()); err == nil {
		t.Fatal("Open succeeded on a directory that is not a repository")
	}
}

func TestFilesRespectsGitignore(t *testing.T) {
	repo := mustOpen(t, newTestRepo(t))

	files, err := repo.Files(context.Background())
	if err != nil {
		t.Fatalf("Files: %v", err)
	}

	got := map[string]bool{}
	for _, f := range files {
		got[f.Path] = true
	}
	for _, want := range []string{"README.md", "docs/setup.md"} {
		if !got[want] {
			t.Errorf("Files is missing %q, got %v", want, got)
		}
	}
	if got["ignored.md"] {
		t.Error("Files returned a .gitignore'd file")
	}
}

func TestReadAndLog(t *testing.T) {
	ctx := context.Background()
	repo := mustOpen(t, newTestRepo(t))

	data, err := repo.Read(ctx, "docs/setup.md")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if string(data) != "# Setup\n" {
		t.Errorf("Read = %q, want %q", data, "# Setup\n")
	}

	commits, err := repo.Log(ctx, "README.md", 10)
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	if len(commits) != 1 {
		t.Fatalf("len(Log) = %d, want 1", len(commits))
	}
	if commits[0].Subject != "initial commit" || commits[0].Author != "Test User" {
		t.Errorf("Log[0] = %+v", commits[0])
	}
	if commits[0].When.IsZero() {
		t.Error("commit timestamp was not parsed")
	}

	head, err := repo.Head(ctx)
	if err != nil {
		t.Fatalf("Head: %v", err)
	}
	if head.IsZero() || len(head.Short()) != 8 {
		t.Errorf("Head = %+v", head)
	}
}

// The copyright notice is dated by the repository's first commit.
func TestFirstCommit(t *testing.T) {
	ctx := context.Background()
	dir := newTestRepo(t)

	writeFile(t, dir, "later.md", "# Later\n")
	gitCmd(t, dir, "add", "later.md")
	gitCmd(t, dir, "commit", "-m", "second commit")

	repo := mustOpen(t, dir)
	first, err := repo.FirstCommit(ctx)
	if err != nil {
		t.Fatalf("FirstCommit: %v", err)
	}
	if first.Subject != "initial commit" {
		t.Errorf("FirstCommit = %q, want the root commit", first.Subject)
	}
	if first.When.IsZero() {
		t.Error("first commit has no date to take a year from")
	}

	head, err := repo.Head(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if first.Hash == head.Hash {
		t.Error("FirstCommit returned the tip of the branch")
	}
}

func TestReadRejectsTraversal(t *testing.T) {
	ctx := context.Background()
	repo := mustOpen(t, newTestRepo(t))

	for _, p := range []string{"../secret.txt", "../../etc/passwd", "/etc/passwd", ".git/config"} {
		if _, err := repo.Read(ctx, p); err == nil {
			t.Errorf("Read(%q) succeeded, want an error", p)
		} else if !errors.Is(err, ErrNotFound) && !errors.Is(err, ErrOutsideRepo) {
			t.Errorf("Read(%q) = %v, want ErrNotFound or ErrOutsideRepo", p, err)
		}
	}
}

func TestReadRejectsOversizedFile(t *testing.T) {
	dir := newTestRepo(t)
	repo := mustOpen(t, dir, WithMaxFileSize(8))

	if _, err := repo.Read(context.Background(), "README.md"); !errors.Is(err, ErrTooLarge) {
		t.Errorf("Read = %v, want ErrTooLarge", err)
	}
}

func TestRefPinsRevision(t *testing.T) {
	ctx := context.Background()
	dir := newTestRepo(t)

	// A second commit that the pinned revision must not see.
	writeFile(t, dir, "later.md", "# Later\n")
	gitCmd(t, dir, "add", "later.md")
	gitCmd(t, dir, "commit", "-m", "second commit")

	repo := mustOpen(t, dir, WithRef("HEAD~1"))
	files, err := repo.Files(ctx)
	if err != nil {
		t.Fatalf("Files: %v", err)
	}
	for _, f := range files {
		if f.Path == "later.md" {
			t.Fatal("pinned revision exposed a file from a later commit")
		}
	}
	if _, err := repo.Read(ctx, "README.md"); err != nil {
		t.Errorf("Read on pinned revision: %v", err)
	}
}

// Opening a directory below the top level narrows the wiki to that sub-tree.
func TestSubdirectoryBecomesTheRoot(t *testing.T) {
	ctx := context.Background()
	dir := newTestRepo(t)
	repo := mustOpen(t, filepath.Join(dir, "docs"))

	if repo.Prefix() != "docs" {
		t.Errorf("Prefix = %q, want %q", repo.Prefix(), "docs")
	}

	files, err := repo.Files(ctx)
	if err != nil {
		t.Fatalf("Files: %v", err)
	}
	if len(files) != 1 || files[0].Path != "setup.md" {
		t.Fatalf("Files = %+v, want just setup.md relative to docs/", files)
	}

	data, err := repo.Read(ctx, "setup.md")
	if err != nil || string(data) != "# Setup\n" {
		t.Errorf("Read(setup.md) = %q, %v", data, err)
	}
	if _, err := repo.Read(ctx, "../README.md"); err == nil {
		t.Error("a file above the served directory was readable")
	}
	if commits, err := repo.Log(ctx, "setup.md", 5); err != nil || len(commits) != 1 {
		t.Errorf("Log = %v, %v", commits, err)
	}
}

func mustOpen(t *testing.T, dir string, opts ...Option) *Repository {
	t.Helper()
	repo, err := Open(context.Background(), dir, opts...)
	if err != nil {
		t.Fatalf("Open(%q): %v", dir, err)
	}
	return repo
}

// A content syncer publishes an update by checking it out into a new directory,
// pointing a symlink at it and removing the old one. A Repository opened
// through that symlink has to follow the move rather than keep addressing the
// directory that has been deleted underneath it.
func TestRefreshFollowsSwappedSymlink(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	ctx := context.Background()

	root := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	link := filepath.Join(root, "current")

	first := newTestRepo(t)
	if err := os.Symlink(first, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	repo, err := Open(ctx, link)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := repo.Files(ctx); err != nil {
		t.Fatalf("Files before the swap: %v", err)
	}

	// The replacement carries a file the original does not.
	second := newTestRepo(t)
	writeFile(t, second, "published.md", "# Published\n")
	gitCmd(t, second, "add", "published.md")
	gitCmd(t, second, "commit", "-m", "publish")

	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(second, link); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(first); err != nil {
		t.Fatal(err)
	}

	if err := repo.Refresh(ctx); err != nil {
		t.Fatalf("Refresh after the swap: %v", err)
	}
	if got := repo.Root(); got != second {
		t.Errorf("Root() = %q, want the new directory %q", got, second)
	}

	files, err := repo.Files(ctx)
	if err != nil {
		t.Fatalf("Files after the swap: %v", err)
	}
	var found bool
	for _, f := range files {
		if f.Path == "published.md" {
			found = true
		}
	}
	if !found {
		t.Errorf("Files() = %v, want the file added in the new directory", files)
	}

	if data, err := repo.Read(ctx, "published.md"); err != nil {
		t.Errorf("Read after the swap: %v", err)
	} else if !strings.Contains(string(data), "Published") {
		t.Errorf("Read = %q, want the new content", data)
	}
}
