package updates

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/system"
	"github.com/vilanobeachflorida/racksail/internal/updates/sigstore"
)

// Repository is where Racksail is released.
const Repository = "vilanobeachflorida/racksail"

// Releases are signed keyless by the release workflow, from a version tag
// or, for nightly builds, from main, with an identity GitHub Actions vouches
// for.
const (
	Workflow = "https://github.com/" + Repository + "/.github/workflows/release.yml"
	Issuer   = "https://token.actions.githubusercontent.com"
)

// SigstoreRootSHA256 is the checksum of the Sigstore trusted root release
// signatures are checked against, the one packaging/install.sh pins; a test
// keeps the two the same. Sigstore's TUF repository and mirrors serve it
// under this name.
const SigstoreRootSHA256 = "6494e21ea73fa7ee769f85f57d5a3e6a08725eae1e38c755fc3517c9e6bc0b66"

// trustedRoots are the Sigstore trusted roots releases may be checked
// against, by their checksum: the one this build pins, and those added from
// init functions. Nothing can add to them once Racksail runs, so neither a
// panel nor a mirror can make the agent trust another root.
var trustedRoots = struct {
	sync.RWMutex
	pins map[string]bool
}{pins: map[string]bool{SigstoreRootSHA256: true}}

// TrustRoot adds a Sigstore trusted root, by the SHA-256 of its JSON, that
// releases may be checked against besides the one this build pins, as for
// a private Sigstore that signs builds of one's own, or a test's. Call it
// from an init function.
func TrustRoot(sha256Hex string) {
	trustedRoots.Lock()
	defer trustedRoots.Unlock()
	trustedRoots.pins[strings.ToLower(sha256Hex)] = true
}

func trusted(sha256Hex string) bool {
	trustedRoots.RLock()
	defer trustedRoots.RUnlock()
	return trustedRoots.pins[sha256Hex]
}

// SigstoreTUF is Sigstore's TUF repository.
const SigstoreTUF = "https://tuf-repo-cdn.sigstore.dev"

// Names of the files a release carries besides its artifacts, and of the
// list of releases a mirror keeps at its root.
const (
	ManifestFile = "release.json"
	BundleSuffix = ".sigstore.json"
	ReleasesFile = "releases.json"
)

var (
	betaPre    = regexp.MustCompile(`^(beta|rc)\.[0-9]+$`)
	nightlyPre = regexp.MustCompile(`^(alpha|(beta|rc)\.[0-9]+)\.nightly\.[0-9]{8}\.[0-9]+$`)
)

// ChannelOf tells which channel a version is released on, as the release
// workflow decides: 1.2.3 is stable, 1.3.0-beta.1 and 1.3.0-rc.1 are beta,
// and nightly builds, such as 1.3.0-alpha.nightly.20261015.42, are nightly.
func ChannelOf(version string) (Channel, error) {
	v, err := ParseVersion(version)
	if err != nil {
		return "", err
	}
	switch {
	case v.Build != "":
		return "", fmt.Errorf("release versions carry no build metadata: %s", version)
	case v.Pre == "":
		return Stable, nil
	case betaPre.MatchString(v.Pre):
		return Beta, nil
	case nightlyPre.MatchString(v.Pre):
		return Nightly, nil
	}
	return "", fmt.Errorf("%s isn't a release version", version)
}

// Identity is who signs a release's files.
func Identity(tag string) (sigstore.Identity, error) {
	ch, err := ChannelOf(tag)
	if err != nil {
		return sigstore.Identity{}, err
	}
	ref := "refs/tags/" + tag
	if ch == Nightly {
		ref = "refs/heads/main"
	}
	return sigstore.Identity{Subject: Workflow + "@" + ref, Issuer: Issuer}, nil
}

// Limits on what is downloaded: manifests, bundles and the trusted root are
// small; a binary is tens of megabytes.
const (
	maxSmall  = 4 << 20
	maxBinary = 512 << 20
)

// Client is how updates are fetched: from mirrors first, in the order
// listed, then from GitHub and Sigstore.
type Client struct {
	HTTP *http.Client
	// API and Downloads are GitHub's; tests point them elsewhere.
	API, Downloads string
	TUF            string
	// Mirrors serve each release's files under its tag and Sigstore's
	// trusted roots under sigstore/, as install.sh expects, and a copy of
	// GitHub's list of releases as releases.json.
	Mirrors []string
	// Root is the checksum of the trusted root releases are checked
	// against, which must be one TrustRoot added; SigstoreRootSHA256 when
	// empty.
	Root string
}

// NewClient returns a client of GitHub and Sigstore, through mirrors.
func NewClient(mirrors []string) *Client {
	return &Client{
		HTTP: &http.Client{Timeout: 10 * time.Minute}, API: "https://api.github.com",
		Downloads: "https://github.com/" + Repository + "/releases/download", TUF: SigstoreTUF, Mirrors: mirrors,
	}
}

func (c *Client) root() string {
	if c.Root != "" {
		return c.Root
	}
	return SigstoreRootSHA256
}

// CheckURL accepts https URLs, and http ones on the server itself, as the
// agent downloads from.
func CheckURL(raw string) error { return system.CheckFetchURL(raw) }

// get fetches one URL, refusing anything larger than max.
func (c *Client) get(ctx context.Context, target string, max int64, w io.Writer) error {
	if err := CheckURL(target); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "Racksail/"+Build().Version)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s answered %s", target, resp.Status)
	}
	if resp.ContentLength > max {
		return fmt.Errorf("%s is larger than %d bytes", target, max)
	}
	n, err := io.Copy(w, io.LimitReader(resp.Body, max+1))
	if err != nil {
		return err
	}
	if n > max {
		return fmt.Errorf("%s is larger than %d bytes", target, max)
	}
	return nil
}

// fetch returns the first answer of the URLs that works.
func (c *Client) fetch(ctx context.Context, urls []string, max int64) ([]byte, error) {
	var errs []error
	for _, u := range urls {
		var buf strings.Builder
		if err := c.get(ctx, u, max, &buf); err != nil {
			errs = append(errs, err)
			continue
		}
		return []byte(buf.String()), nil
	}
	return nil, errors.Join(errs...)
}

// releaseURLs are where a release's file can be fetched from.
func (c *Client) releaseURLs(tag, name string) []string {
	var out []string
	for _, m := range c.Mirrors {
		out = append(out, strings.TrimSuffix(m, "/")+"/"+tag+"/"+name)
	}
	return append(out, strings.TrimSuffix(c.Downloads, "/")+"/"+tag+"/"+name)
}

// TrustedRootURLs are where the pinned trusted root can be fetched from.
func (c *Client) TrustedRootURLs() []string {
	var out []string
	for _, m := range c.Mirrors {
		out = append(out, strings.TrimSuffix(m, "/")+"/sigstore/"+c.root()+".trusted_root.json")
	}
	return append(out, strings.TrimSuffix(c.TUF, "/")+"/targets/"+c.root()+".trusted_root.json")
}

// TrustedRoot returns the pinned trusted root: from cache when it was
// fetched before, otherwise from a mirror or Sigstore, checked against the
// pin either way.
func (c *Client) TrustedRoot(ctx context.Context, cache string) (*sigstore.TrustedRoot, []byte, error) {
	path := ""
	if cache != "" {
		path = filepath.Join(cache, c.root()+".trusted_root.json")
		if data, err := os.ReadFile(path); err == nil {
			if root, err := c.pinned(data); err == nil {
				return root, data, nil
			}
		}
	}
	data, err := c.fetch(ctx, c.TrustedRootURLs(), maxSmall)
	if err != nil {
		return nil, nil, fmt.Errorf("fetching Sigstore's trusted root: %w", err)
	}
	root, err := c.pinned(data)
	if err != nil {
		return nil, nil, err
	}
	if path != "" {
		if err := os.MkdirAll(cache, 0o750); err == nil {
			os.WriteFile(path, data, 0o640)
		}
	}
	return root, data, nil
}

// pinned parses a trusted root after checking it is the client's.
func (c *Client) pinned(data []byte) (*sigstore.TrustedRoot, error) {
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != c.root() {
		return nil, errors.New("the Sigstore trusted root isn't the one this version of Racksail pins")
	}
	return PinnedRoot(data)
}

// PinnedRoot parses a trusted root after checking it is one releases may
// be checked against: the one this build pins, or one TrustRoot added.
func PinnedRoot(data []byte) (*sigstore.TrustedRoot, error) {
	sum := sha256.Sum256(data)
	if !trusted(hex.EncodeToString(sum[:])) {
		return nil, errors.New("the Sigstore trusted root isn't the one this version of Racksail pins")
	}
	return sigstore.ParseTrustedRoot(data)
}

// githubRelease is a release as GitHub's API lists it.
type githubRelease struct {
	TagName string `json:"tag_name"`
	Draft   bool   `json:"draft"`
}

// Releases lists the published releases, newest first, without their
// files: every tag that names a release on a channel. The list isn't
// signed, but it only names tags and each release's manifest is checked
// against its signature, so a mirror can at worst hold releases back.
func (c *Client) Releases(ctx context.Context) ([]Release, error) {
	var urls []string
	for _, m := range c.Mirrors {
		urls = append(urls, strings.TrimSuffix(m, "/")+"/"+ReleasesFile)
	}
	urls = append(urls, strings.TrimSuffix(c.API, "/")+"/repos/"+Repository+"/releases?per_page=100")
	data, err := c.fetch(ctx, urls, maxSmall)
	if err != nil {
		return nil, fmt.Errorf("listing releases: %w", err)
	}
	var list []githubRelease
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, fmt.Errorf("reading the list of releases: %w", err)
	}
	var out []Release
	for _, r := range list {
		ch, err := ChannelOf(r.TagName)
		if r.Draft || err != nil || !strings.HasPrefix(r.TagName, "v") {
			continue
		}
		v, _ := ParseVersion(r.TagName)
		out = append(out, Release{Version: v, Tag: r.TagName, Channel: ch})
	}
	slices.SortFunc(out, func(a, b Release) int { return b.Version.Compare(a.Version) })
	return out, nil
}

// Manifest fetches a release's manifest and checks it was signed by the
// release workflow for its tag.
func (c *Client) Manifest(ctx context.Context, root *sigstore.TrustedRoot, tag string) (*Release, error) {
	id, err := Identity(tag)
	if err != nil {
		return nil, err
	}
	data, err := c.fetch(ctx, c.releaseURLs(tag, ManifestFile), maxSmall)
	if err != nil {
		return nil, fmt.Errorf("fetching the manifest of %s: %w", tag, err)
	}
	bundle, err := c.fetch(ctx, c.releaseURLs(tag, ManifestFile+BundleSuffix), maxSmall)
	if err != nil {
		return nil, fmt.Errorf("fetching the signature of the manifest of %s: %w", tag, err)
	}
	sum := sha256.Sum256(data)
	if _, err := sigstore.Verify(root, bundle, sum[:], id); err != nil {
		return nil, fmt.Errorf("the manifest of %s isn't signed by Racksail's release workflow: %w", tag, err)
	}
	var rel Release
	if err := json.Unmarshal(data, &rel); err != nil {
		return nil, fmt.Errorf("reading the manifest of %s: %w", tag, err)
	}
	if rel.Tag != tag {
		return nil, fmt.Errorf("the manifest fetched for %s is for %s", tag, rel.Tag)
	}
	ch, err := ChannelOf(tag)
	if err != nil || rel.Channel != ch {
		return nil, fmt.Errorf("the manifest of %s names the %s channel", tag, rel.Channel)
	}
	if rel.Version, err = ParseVersion(tag); err != nil {
		return nil, err
	}
	return &rel, nil
}

// AssetURLs are where an asset of a release can be downloaded from:
// mirrors first, then where the manifest says.
func (c *Client) AssetURLs(tag string, a Asset) (files, bundles []string) {
	files = c.releaseURLs(tag, a.Name)
	bundles = c.releaseURLs(tag, a.Name+BundleSuffix)
	if a.URL != "" && !slices.Contains(files, a.URL) {
		files = append(files, a.URL)
	}
	if a.Bundle != "" && !slices.Contains(bundles, a.Bundle) {
		bundles = append(bundles, a.Bundle)
	}
	return files, bundles
}
