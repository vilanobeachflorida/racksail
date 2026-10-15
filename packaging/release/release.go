package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/updates"
)

// Names of the files a release carries besides its artifacts.
const (
	ChecksumsFile = "SHA256SUMS"
	ManifestFile  = "release.json"
	BundleSuffix  = ".sigstore.json"
)

var (
	betaPre    = regexp.MustCompile(`^(beta|rc)\.[0-9]+$`)
	nightlyPre = regexp.MustCompile(`^((beta|rc)\.[0-9]+\.)?nightly\.[0-9]{8}\.[0-9]+$`)
)

// Channel tells which channel a version is released on: 1.2.3 is stable,
// 1.3.0-beta.1 and 1.3.0-rc.1 are beta, and versions with a nightly part are
// nightly. Other pre-release forms aren't releases.
func Channel(version string) (updates.Channel, error) {
	v, err := updates.ParseVersion(version)
	if err != nil {
		return "", err
	}
	if v.Build != "" {
		return "", fmt.Errorf("release versions carry no build metadata: %s", version)
	}
	switch {
	case v.Pre == "":
		return updates.Stable, nil
	case betaPre.MatchString(v.Pre):
		return updates.Beta, nil
	case nightlyPre.MatchString(v.Pre):
		return updates.Nightly, nil
	}
	return "", fmt.Errorf("%s isn't a release version: use 1.2.3, 1.2.3-beta.1 or 1.2.3-rc.1", version)
}

// NightlyVersion names a nightly build so that it sorts after everything
// released before it and before the next release: after the newest beta or
// release candidate, as 1.3.0-rc.1.nightly.20261015.42, or else in the minor
// version after the newest stable release, as 1.3.0-nightly.20261015.42.
// date is the day as YYYYMMDD and run tells builds of one day apart.
func NightlyVersion(tags []string, date string, run int) (string, error) {
	if ok, _ := regexp.MatchString(`^[0-9]{8}$`, date); !ok {
		return "", fmt.Errorf("the date %q must look like 20261015", date)
	}
	if run < 1 {
		return "", errors.New("the run number starts at 1")
	}
	var newest *updates.Version
	for _, tag := range tags {
		ch, err := Channel(tag)
		if err != nil || ch == updates.Nightly {
			continue
		}
		v, _ := updates.ParseVersion(tag)
		if newest == nil || v.Compare(*newest) > 0 {
			newest = &v
		}
	}
	suffix := fmt.Sprintf("nightly.%s.%d", date, run)
	switch {
	case newest == nil:
		return "0.1.0-" + suffix, nil
	case newest.Pre != "":
		return newest.String() + "." + suffix, nil
	}
	return fmt.Sprintf("%d.%d.0-%s", newest.Major, newest.Minor+1, suffix), nil
}

// artifacts lists the files of a release directory that are checksummed and
// signed: everything except signatures, checksums and the manifest.
func artifacts(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		name := e.Name()
		if !e.Type().IsRegular() || strings.HasPrefix(name, ".") || strings.HasSuffix(name, BundleSuffix) || name == ChecksumsFile || name == ManifestFile {
			continue
		}
		out = append(out, name)
	}
	sort.Strings(out)
	return out, nil
}

func hashFile(file string) (string, int64, error) {
	f, err := os.Open(file)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// Checksums lists every artifact in dir with its SHA-256, in the format
// sha256sum reads.
func Checksums(dir string) ([]byte, error) {
	names, err := artifacts(dir)
	if err != nil {
		return nil, err
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("%s holds no artifacts", dir)
	}
	var b bytes.Buffer
	for _, name := range names {
		sum, _, err := hashFile(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		fmt.Fprintf(&b, "%s  %s\n", sum, name)
	}
	return b.Bytes(), nil
}

// Manifest describes a release for servers that update themselves and for
// mirrors: every artifact with its size, checksum and signature bundle, as
// internal/updates reads it. Artifacts are expected at baseURL/<name>.
func Manifest(dir, tag, baseURL, notesURL string, published time.Time) (*updates.Release, error) {
	ch, err := Channel(tag)
	if err != nil {
		return nil, err
	}
	names, err := artifacts(dir)
	if err != nil {
		return nil, err
	}
	baseURL = strings.TrimSuffix(baseURL, "/")
	rel := &updates.Release{Tag: tag, Channel: ch, Published: published.UTC().Truncate(time.Second), NotesURL: notesURL}
	for _, name := range append(names, ChecksumsFile) {
		sum, size, err := hashFile(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		bundle := name + BundleSuffix
		if _, err := os.Stat(filepath.Join(dir, bundle)); err != nil {
			return nil, fmt.Errorf("%s has no signature bundle %s; sign every artifact before writing the manifest", name, bundle)
		}
		rel.Assets = append(rel.Assets, updates.Asset{Name: name, URL: baseURL + "/" + name, Size: size, SHA256: sum, Bundle: baseURL + "/" + bundle})
	}
	for _, arch := range []string{"amd64", "arm64"} {
		if _, ok := rel.Binary(arch); !ok {
			return nil, fmt.Errorf("the release has no racksail binary for %s", arch)
		}
	}
	return rel, nil
}

var (
	pinVersion = regexp.MustCompile(`(?m)^COSIGN_VERSION=(v[0-9.]+)$`)
	pinSum     = regexp.MustCompile(`(?m)^COSIGN_SHA256_(AMD64|ARM64)=([0-9a-f]{64})$`)
)

// CosignPins reads the cosign version and checksums the install script
// trusts.
func CosignPins(script []byte) (string, map[string]string, error) {
	m := pinVersion.FindSubmatch(script)
	if m == nil {
		return "", nil, errors.New("the install script doesn't set COSIGN_VERSION")
	}
	sums := map[string]string{}
	for _, s := range pinSum.FindAllSubmatch(script, -1) {
		sums[strings.ToLower(string(s[1]))] = string(s[2])
	}
	if len(sums) != 2 {
		return "", nil, errors.New("the install script needs COSIGN_SHA256_AMD64 and COSIGN_SHA256_ARM64")
	}
	return string(m[1]), sums, nil
}

// publishedSums reads a checksums file in the format sha256sum writes.
func publishedSums(published []byte) map[string]string {
	upstream := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(published))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) == 2 {
			upstream[fields[1]] = fields[0]
		}
	}
	return upstream
}

var sha256Hex = regexp.MustCompile(`^[0-9a-f]{64}$`)

// UpdateCosignPins writes the checksums cosign published into the install
// script, for when a dependency update bumps COSIGN_VERSION.
func UpdateCosignPins(script, published []byte) ([]byte, error) {
	upstream := publishedSums(published)
	for _, arch := range []string{"AMD64", "ARM64"} {
		sum, ok := upstream["cosign-linux-"+strings.ToLower(arch)]
		if !ok || !sha256Hex.MatchString(sum) {
			return nil, fmt.Errorf("cosign's checksums don't list cosign-linux-%s", strings.ToLower(arch))
		}
		line := regexp.MustCompile(`(?m)^COSIGN_SHA256_` + arch + `=[0-9a-f]{64}$`)
		if !line.Match(script) {
			return nil, fmt.Errorf("the install script has no COSIGN_SHA256_%s line", arch)
		}
		script = line.ReplaceAll(script, []byte("COSIGN_SHA256_"+arch+"="+sum))
	}
	return script, nil
}

// CheckCosignPins compares the install script's checksums with the ones
// cosign published for the release it names.
func CheckCosignPins(sums map[string]string, published []byte) error {
	upstream := publishedSums(published)
	var errs []error
	for _, arch := range []string{"amd64", "arm64"} {
		name := "cosign-linux-" + arch
		want, ok := upstream[name]
		switch {
		case !ok:
			errs = append(errs, fmt.Errorf("cosign's checksums don't list %s", name))
		case want != sums[arch]:
			errs = append(errs, fmt.Errorf("the install script pins %s at %s, but cosign published %s", name, sums[arch], want))
		}
	}
	return errors.Join(errs...)
}
