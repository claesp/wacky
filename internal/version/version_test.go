package version

import (
	"strings"
	"testing"
)

// An unstamped build still has to identify itself: the toolchain records the
// revision even when the linker supplies nothing.
func TestGetFallsBackToDev(t *testing.T) {
	i := Get()
	if i.Version == "" {
		t.Error("Version is empty; it should never be")
	}
	if i.Go == "" || i.Platform == "" {
		t.Errorf("Go = %q, Platform = %q; both should be filled in", i.Go, i.Platform)
	}
	if !strings.Contains(i.Platform, "/") {
		t.Errorf("Platform = %q, want GOOS/GOARCH", i.Platform)
	}
}

func TestShortCommit(t *testing.T) {
	tests := map[string]string{
		"":       "",
		"abc123": "abc123",
		"0123456789abcdef0123456789abcdef01234567": "0123456789ab",
		// The dirty marker has to survive truncation, or a modified build
		// would report itself as a clean one.
		"0123456789abcdef0123456789abcdef01234567-dirty": "0123456789ab-dirty",
		"abc123-dirty": "abc123-dirty",
	}
	for in, want := range tests {
		if got := (Info{Commit: in}).ShortCommit(); got != want {
			t.Errorf("Info{Commit: %q}.ShortCommit() = %q, want %q", in, got, want)
		}
	}
}

func TestString(t *testing.T) {
	i := Info{
		Version:  "v1.2.3",
		Commit:   "0123456789abcdef0123456789abcdef01234567",
		Date:     "2026-08-05T01:00:00Z",
		Go:       "go1.24.0",
		Platform: "linux/amd64",
	}
	got := i.String()
	want := "wacky v1.2.3 (0123456789ab, built 2026-08-05T01:00:00Z, go1.24.0, linux/amd64)"
	if got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}

	// A build with no VCS data should not leave an empty field behind.
	bare := Info{Version: "dev", Go: "go1.24.0", Platform: "linux/amd64"}
	if got := bare.String(); got != "wacky dev (go1.24.0, linux/amd64)" {
		t.Errorf("String() with no commit = %q", got)
	}
}
