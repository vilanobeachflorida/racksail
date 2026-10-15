package updates

import (
	"context"
	"fmt"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"time"
)

// Set by release builds:
//
//	-ldflags "-X github.com/vilanobeachflorida/racksail/internal/updates.version=1.2.3 ..."
//
// commit and commitTime are the commit the release is built from and the
// time it was made, in RFC 3339.
var (
	version    = ""
	commit     = ""
	commitTime = ""
	channel    = ""
)

// BuildInfo describes the running binary. It names the commit the binary
// was built from and when that commit was made, and no time of its own: a
// build of the same commit is the same binary, whenever it was made.
type BuildInfo struct {
	Version    string `json:"version" example:"1.0.0" doc:"Racksail version"`
	Commit     string `json:"commit,omitempty" doc:"Source commit the binary was built from"`
	CommitTime string `json:"commitTime,omitempty" format:"date-time" doc:"When that commit was made"`
	Channel    string `json:"channel" enum:"stable,beta,nightly,dev" doc:"Release channel the binary belongs to"`
	GoVersion  string `json:"goVersion" doc:"Go version used to build it"`
	OS         string `json:"os"`
	Arch       string `json:"arch"`
}

// Build returns information about the running binary. Builds without
// release flags report version 0.0.0-dev on the dev channel, with the commit
// and its time from Go's version control stamping when it is available.
func Build() BuildInfo {
	b := BuildInfo{Version: version, Commit: commit, CommitTime: commitTime, Channel: channel, GoVersion: runtime.Version(), OS: runtime.GOOS, Arch: runtime.GOARCH}
	if b.Version == "" {
		b.Version = "0.0.0-dev"
	}
	if b.Channel == "" {
		b.Channel = "dev"
	}
	if info, ok := debug.ReadBuildInfo(); ok && commit == "" {
		modified := false
		for _, s := range info.Settings {
			switch s.Key {
			case "vcs.revision":
				b.Commit = s.Value
			case "vcs.time":
				b.CommitTime = s.Value
			case "vcs.modified":
				modified = s.Value == "true"
			}
		}
		if modified && b.Commit != "" {
			b.Commit += "-dirty"
		}
	}
	return b
}

// Channel is a release channel.
type Channel string

// Channels.
const (
	Stable  Channel = "stable"
	Beta    Channel = "beta"
	Nightly Channel = "nightly"
)

// Valid reports whether c is a known channel.
func (c Channel) Valid() bool { return c == Stable || c == Beta || c == Nightly }

// Accepts reports whether a release on channel r is offered to servers on c:
// nightly servers get everything, beta servers get beta and stable releases,
// stable servers only stable ones.
func (c Channel) Accepts(r Channel) bool {
	switch c {
	case Nightly:
		return true
	case Beta:
		return r == Beta || r == Stable
	}
	return r == Stable
}

// Version is a semantic version.
type Version struct {
	Major, Minor, Patch int
	Pre                 string
	Build               string
}

// ParseVersion reads a version such as 1.2.3, v1.2.3-beta.1 or
// 1.2.3+20261015.
func ParseVersion(s string) (Version, error) {
	var v Version
	s = strings.TrimPrefix(s, "v")
	s, v.Build, _ = strings.Cut(s, "+")
	s, v.Pre, _ = strings.Cut(s, "-")
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return v, fmt.Errorf("version %q must look like 1.2.3", s)
	}
	nums := []*int{&v.Major, &v.Minor, &v.Patch}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || (len(p) > 1 && p[0] == '0') {
			return v, fmt.Errorf("version part %q isn't a number", p)
		}
		*nums[i] = n
	}
	return v, nil
}

// String formats the version without a leading v.
func (v Version) String() string {
	s := fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
	if v.Pre != "" {
		s += "-" + v.Pre
	}
	if v.Build != "" {
		s += "+" + v.Build
	}
	return s
}

// Compare returns -1, 0 or 1 following semantic versioning precedence; build
// metadata is ignored.
func (v Version) Compare(o Version) int {
	for _, d := range []int{v.Major - o.Major, v.Minor - o.Minor, v.Patch - o.Patch} {
		if d != 0 {
			return sign(d)
		}
	}
	switch {
	case v.Pre == o.Pre:
		return 0
	case v.Pre == "":
		return 1
	case o.Pre == "":
		return -1
	}
	a, b := strings.Split(v.Pre, "."), strings.Split(o.Pre, ".")
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] == b[i] {
			continue
		}
		na, errA := strconv.Atoi(a[i])
		nb, errB := strconv.Atoi(b[i])
		switch {
		case errA == nil && errB == nil:
			return sign(na - nb)
		case errA == nil:
			return -1
		case errB == nil:
			return 1
		}
		return strings.Compare(a[i], b[i])
	}
	return sign(len(a) - len(b))
}

func sign(n int) int {
	switch {
	case n < 0:
		return -1
	case n > 0:
		return 1
	}
	return 0
}

// Asset is a file attached to a release.
type Asset struct {
	Name   string `json:"name"`
	URL    string `json:"url"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
	// Bundle is the Sigstore bundle that signs the asset.
	Bundle string `json:"bundle,omitempty"`
}

// Release is a published version.
type Release struct {
	Version   Version   `json:"-"`
	Tag       string    `json:"tag"`
	Channel   Channel   `json:"channel"`
	Published time.Time `json:"published"`
	NotesURL  string    `json:"notesUrl,omitempty"`
	Assets    []Asset   `json:"assets"`
}

// Binary returns the release's racksail binary for an architecture.
func (r Release) Binary(arch string) (Asset, bool) {
	want := "racksail-linux-" + arch
	for _, a := range r.Assets {
		if a.Name == want {
			return a, true
		}
	}
	return Asset{}, false
}

// Source lists releases, from GitHub Releases or a mirror.
type Source interface {
	Releases(ctx context.Context) ([]Release, error)
}

// Verifier checks that an asset was signed by the Racksail release workflow.
type Verifier interface {
	Verify(ctx context.Context, asset Asset, content []byte) error
}

// Latest returns the newest release a server on channel c should be offered
// that is newer than current, or false when it is up to date.
func Latest(releases []Release, c Channel, current Version) (Release, bool) {
	var best Release
	found := false
	for _, r := range releases {
		if !c.Accepts(r.Channel) || r.Version.Compare(current) <= 0 {
			continue
		}
		if !found || r.Version.Compare(best.Version) > 0 {
			best, found = r, true
		}
	}
	return best, found
}
