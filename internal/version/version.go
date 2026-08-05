// Package version reports which build of wacky is running.
//
// A release build stamps the number in at link time:
//
//	go build -ldflags "-X github.com/claesp/wacky/internal/version.version=v1.2.3" ./cmd/wacky
//
// Nothing else has to be stamped. The commit, the build date and the
// working-tree state are recorded by the Go toolchain itself and read back out
// of the binary, so an ordinary `go build` still identifies itself precisely.
package version

import (
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
)

// Set by the linker. They are deliberately unexported: nothing should be able
// to change the identity of a running binary.
var (
	version string
	commit  string
	date    string
)

// shortCommit is how much of a hash is worth showing. Git itself abbreviates
// to seven; twelve stays unambiguous in a large repository.
const shortCommit = 12

// Info describes the running binary.
type Info struct {
	// Version is the release, or "dev" for a build made outside one.
	Version string
	// Commit is the revision it was built from, "-dirty" when the working tree
	// had uncommitted changes. Empty when the build carried no VCS data.
	Commit string
	// Date is when it was built, in RFC 3339, or empty if unrecorded.
	Date string
	// Go is the toolchain that built it.
	Go string
	// Platform is the target it was built for, as GOOS/GOARCH.
	Platform string
}

// Get returns the identity of this binary. The answer cannot change while the
// process runs, so it is worked out once.
var Get = sync.OnceValue(func() Info {
	i := Info{
		Version:  version,
		Commit:   commit,
		Date:     date,
		Go:       runtime.Version(),
		Platform: runtime.GOOS + "/" + runtime.GOARCH,
	}

	// Whatever the linker did not supply, the build info might. This is what
	// makes `go install ...@v1.2.3` and a plain `go build` self-describing.
	if bi, ok := debug.ReadBuildInfo(); ok {
		// A module built from source records "(devel)", which says less than
		// the fallback below.
		if i.Version == "" && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
			i.Version = bi.Main.Version
		}
		var dirty bool
		for _, s := range bi.Settings {
			switch s.Key {
			case "vcs.revision":
				if i.Commit == "" {
					i.Commit = s.Value
				}
			case "vcs.time":
				if i.Date == "" {
					i.Date = s.Value
				}
			case "vcs.modified":
				dirty = s.Value == "true"
			}
		}
		if dirty && i.Commit != "" && !strings.HasSuffix(i.Commit, "-dirty") {
			i.Commit += "-dirty"
		}
	}

	if i.Version == "" {
		i.Version = "dev"
	}
	return i
})

// Short returns just the version, for a log field or a JSON value.
func Short() string { return Get().Version }

// String renders the full identity on one line, the way -version prints it.
func (i Info) String() string {
	var b strings.Builder
	b.WriteString("wacky ")
	b.WriteString(i.Version)

	var parts []string
	if c := i.ShortCommit(); c != "" {
		parts = append(parts, c)
	}
	if i.Date != "" {
		parts = append(parts, "built "+i.Date)
	}
	parts = append(parts, i.Go, i.Platform)

	b.WriteString(" (")
	b.WriteString(strings.Join(parts, ", "))
	b.WriteString(")")
	return b.String()
}

// ShortCommit abbreviates the revision, keeping any -dirty marker: a truncated
// hash that hid the marker would misreport the build.
func (i Info) ShortCommit() string {
	hash, suffix, found := strings.Cut(i.Commit, "-")
	if len(hash) > shortCommit {
		hash = hash[:shortCommit]
	}
	if found {
		return hash + "-" + suffix
	}
	return hash
}
