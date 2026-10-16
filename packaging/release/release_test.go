package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/updates"
)

func TestChannel(t *testing.T) {
	cases := map[string]updates.Channel{
		"v1.2.3":                           updates.Stable,
		"1.2.3":                            updates.Stable,
		"v1.3.0-beta.1":                    updates.Beta,
		"v1.3.0-rc.2":                      updates.Beta,
		"v1.3.0-alpha.nightly.20261015.42": updates.Nightly,
		"v1.3.0-beta.2.nightly.20261015.7": updates.Nightly,
		"v1.3.0-rc.1.nightly.20261015.7":   updates.Nightly,
	}
	for v, want := range cases {
		got, err := Channel(v)
		if err != nil || got != want {
			t.Errorf("Channel(%s) = %s, %v; want %s", v, got, err, want)
		}
	}
	bad := []string{
		"v1.2", "v1.3.0-alpha.1", "v1.3.0-beta", "v1.2.3+build.5", "v1.3.0-nightly", "latest",
		// Nightly builds after a stable release are alphas, so they sort
		// before the next minor version's betas.
		"v1.3.0-nightly.20261015.42", "v1.3.0-alpha.1.nightly.20261015.42", "v1.3.0-alpha.nightly.2026101.4",
	}
	for _, v := range bad {
		if _, err := Channel(v); err == nil {
			t.Errorf("Channel(%s) should fail", v)
		}
	}
}

func TestNightlyVersion(t *testing.T) {
	cases := []struct {
		tags []string
		want string
	}{
		{nil, "0.1.0-alpha.nightly.20261015.42"},
		{[]string{"v1.2.0", "v1.9.10", "v1.9.3"}, "1.10.0-alpha.nightly.20261015.42"},
		{[]string{"v1.9.10", "v1.10.0-beta.2"}, "1.10.0-beta.2.nightly.20261015.42"},
		{[]string{"v1.9.10", "v1.10.0-beta.2", "v1.10.0-rc.1", "v1.10.0-rc.1.nightly.20261014.41"}, "1.10.0-rc.1.nightly.20261015.42"},
		{[]string{"v1.10.0-rc.1", "v1.10.0"}, "1.11.0-alpha.nightly.20261015.42"},
		{[]string{"v2.0.0", "not-a-tag", "v1.0.0-weird.1"}, "2.1.0-alpha.nightly.20261015.42"},
		// The first beta of the version nightly builds were alphas of.
		{[]string{"v1.2.0", "v1.3.0-alpha.nightly.20261014.41", "v1.3.0-beta.1"}, "1.3.0-beta.1.nightly.20261015.42"},
		// A release candidate of a patch release, made while main works
		// towards the next minor version, leaves nightly builds where they are.
		{[]string{"v1.2.0", "v1.3.0-alpha.nightly.20261014.41", "v1.2.1-rc.1"}, "1.3.0-alpha.nightly.20261015.42"},
		{[]string{"v1.2.0", "v1.3.0-beta.1", "v1.3.0-beta.1.nightly.20261014.41", "v1.2.1-rc.1", "v1.2.1"}, "1.3.0-beta.1.nightly.20261015.42"},
		// Builds named the old way aren't release versions.
		{[]string{"v1.2.0", "v1.3.0-nightly.20261014.41"}, "1.3.0-alpha.nightly.20261015.42"},
	}
	for _, tc := range cases {
		got, err := NightlyVersion(tc.tags, "20261015", 42)
		if err != nil || got != tc.want {
			t.Errorf("NightlyVersion(%v) = %s, %v; want %s", tc.tags, got, err, tc.want)
			continue
		}
		// A nightly build is newer than everything it was built after, so
		// servers on the nightly channel take it.
		nightly, _ := updates.ParseVersion(got)
		for _, tag := range tc.tags {
			if v, err := updates.ParseVersion(tag); err == nil && v.Compare(nightly) >= 0 {
				if _, err := Channel(tag); err == nil {
					t.Errorf("%s isn't newer than %s", got, tag)
				}
			}
		}
		if ch, err := Channel(got); err != nil || ch != updates.Nightly {
			t.Errorf("%s is on channel %s, %v", got, ch, err)
		}
	}
	if _, err := NightlyVersion(nil, "2026-10-15", 1); err == nil {
		t.Error("a malformed date must fail")
	}
	if _, err := NightlyVersion(nil, "20261015", 0); err == nil {
		t.Error("run numbers start at 1")
	}
	// The same day and run again, or an earlier day, would give servers a
	// build they never take.
	built := []string{"v1.2.0", "v1.3.0-alpha.nightly.20261015.42"}
	for _, day := range []string{"20261015", "20261014"} {
		if v, err := NightlyVersion(built, day, 42); err == nil || !strings.Contains(err.Error(), "wouldn't be newer than the nightly build 1.3.0-alpha.nightly.20261015.42") {
			t.Errorf("NightlyVersion on %s = %s, %v; want an error", day, v, err)
		}
	}
}

// Over a release cycle, with patch releases of the previous version in
// between, every nightly build is newer than every release and nightly build
// before it, and older than the next release made from main, so servers on
// the nightly channel only ever move forward.
func TestNightlyVersionsOnlyMoveForward(t *testing.T) {
	type event struct {
		release  string // tagged that day before the nightly build, if any
		fromMain bool   // the release is made from main, so it follows the nightly builds
		want     string
	}
	days := []event{
		{"v1.2.0", true, "1.3.0-alpha.nightly.20261001.1"},
		{"", false, "1.3.0-alpha.nightly.20261002.2"},
		{"v1.2.1-rc.1", false, "1.3.0-alpha.nightly.20261003.3"},
		{"v1.2.1", false, "1.3.0-alpha.nightly.20261004.4"},
		{"v1.3.0-beta.1", true, "1.3.0-beta.1.nightly.20261005.5"},
		{"", false, "1.3.0-beta.1.nightly.20261006.6"},
		{"v1.3.0-beta.2", true, "1.3.0-beta.2.nightly.20261007.7"},
		{"v1.2.2", false, "1.3.0-beta.2.nightly.20261008.8"},
		{"v1.3.0-rc.1", true, "1.3.0-rc.1.nightly.20261009.9"},
		{"v1.3.0", true, "1.4.0-alpha.nightly.20261010.10"},
		{"v1.3.1-rc.1", false, "1.4.0-alpha.nightly.20261011.11"},
		{"v2.0.0-beta.1", true, "2.0.0-beta.1.nightly.20261012.12"},
		{"v2.0.0", true, "2.1.0-alpha.nightly.20261013.13"},
	}
	var tags []string
	var lastNightly updates.Version
	for i, day := range days {
		if day.release != "" {
			if v, _ := updates.ParseVersion(day.release); day.fromMain && i > 0 && v.Compare(lastNightly) <= 0 {
				t.Errorf("%s doesn't sort after the nightly build before it, %s", day.release, lastNightly)
			}
			tags = append(tags, day.release)
		}
		got, err := NightlyVersion(tags, fmt.Sprintf("202610%02d", i+1), i+1)
		if err != nil || got != day.want {
			t.Fatalf("day %d: NightlyVersion = %s, %v; want %s", i+1, got, err, day.want)
		}
		v, _ := updates.ParseVersion(got)
		for _, tag := range tags {
			if other, _ := updates.ParseVersion(tag); other.Compare(v) >= 0 {
				t.Errorf("day %d: %s isn't newer than %s", i+1, got, tag)
			}
		}
		tags = append(tags, "v"+got)
		lastNightly = v
	}
}

func TestLatest(t *testing.T) {
	tags := []string{"catalog", "v1.2.0", "v1.2.5", "v1.3.0", "v1.4.0-beta.1", "v1.4.0-alpha.nightly.20261015.4", "9.9.9"}
	cases := map[string]bool{
		"1.3.1":                           true,
		"1.4.0":                           true,
		"1.3.0":                           true,
		"1.2.6":                           false,
		"1.4.0-rc.1":                      false,
		"1.4.0-beta.1.nightly.20261016.5": false,
	}
	for version, want := range cases {
		if got, err := Latest(tags, version); err != nil || got != want {
			t.Errorf("Latest(%s) = %v, %v; want %v", version, got, err, want)
		}
	}
	if got, err := Latest(nil, "1.0.0"); err != nil || !got {
		t.Errorf("the first stable release is the latest: %v, %v", got, err)
	}
	if _, err := Latest(tags, "next"); err == nil {
		t.Error("a malformed version must fail")
	}
}

func TestPrevious(t *testing.T) {
	tags := []string{
		"catalog", "v1.1.0", "v1.2.0", "v1.2.1", "v1.3.0-alpha.nightly.20261001.30", "v1.3.0-beta.1",
		"v1.3.0-rc.1", "v1.3.0-rc.1.nightly.20261014.41", "v1.3.0-rc.1.nightly.20261015.42", "v1.4.0-weird.1", "1.9.9",
	}
	cases := []struct {
		channel updates.Channel
		version string
		want    string
	}{
		{updates.Stable, "1.3.0", "v1.2.1"},
		{updates.Stable, "1.2.1", "v1.2.0"},
		{updates.Stable, "1.1.0", ""},
		{updates.Beta, "1.3.0-rc.2", "v1.3.0-rc.1"},
		{updates.Beta, "1.3.0-beta.1", "v1.2.1"},
		{updates.Nightly, "1.3.0-rc.1.nightly.20261016.43", "v1.3.0-rc.1.nightly.20261015.42"},
		{updates.Nightly, "1.3.0-rc.1.nightly.20261015.42", "v1.3.0-rc.1.nightly.20261014.41"},
		// Servers on the nightly channel took the first beta, which is newer
		// than the alphas built before it.
		{updates.Nightly, "1.3.0-beta.1.nightly.20261002.31", "v1.3.0-beta.1"},
		{updates.Nightly, "1.3.0-alpha.nightly.20261002.31", "v1.3.0-alpha.nightly.20261001.30"},
		// Stable servers upgrade to a nightly build from the newest stable
		// release, which an upgrade test checks as well.
		{updates.Stable, "1.3.0-rc.1.nightly.20261016.43", "v1.2.1"},
		{updates.Nightly, "1.0.0", ""},
	}
	for _, tc := range cases {
		got, err := Previous(tags, tc.channel, tc.version)
		if err != nil || got != tc.want {
			t.Errorf("Previous(%s, %s) = %q, %v; want %q", tc.channel, tc.version, got, err, tc.want)
		}
	}
	if _, err := Previous(tags, "lts", "1.3.0"); err == nil {
		t.Error("an unknown channel must fail")
	}
	if _, err := Previous(tags, updates.Stable, "next"); err == nil {
		t.Error("a malformed version must fail")
	}
}

func writeRelease(t *testing.T, signed bool) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range map[string]string{
		"racksail-linux-amd64":           "amd64 binary",
		"racksail-linux-arm64":           "arm64 binary",
		"racksail_1.2.3_amd64.deb":       "package",
		"install.sh":                     "#!/bin/sh\n",
		"racksail-linux-amd64.spdx.json": "{}",
	} {
		os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644)
	}
	sums, err := Checksums(dir)
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, ChecksumsFile), sums, 0o644)
	if signed {
		names, _ := artifacts(dir)
		for _, name := range append(names, ChecksumsFile) {
			os.WriteFile(filepath.Join(dir, name+BundleSuffix), []byte("{}"), 0o644)
		}
	}
	return dir
}

func TestChecksums(t *testing.T) {
	dir := writeRelease(t, true)
	data, err := os.ReadFile(filepath.Join(dir, ChecksumsFile))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 5 || !strings.HasSuffix(lines[0], "  install.sh") || len(strings.Fields(lines[0])[0]) != 64 {
		t.Errorf("SHA256SUMS =\n%s", data)
	}
	if bytes.Contains(data, []byte(BundleSuffix)) || bytes.Contains(data, []byte(ChecksumsFile)) {
		t.Error("signatures and the checksums file itself aren't checksummed")
	}
	if _, err := Checksums(t.TempDir()); err == nil {
		t.Error("an empty release must fail")
	}
}

func TestManifest(t *testing.T) {
	dir := writeRelease(t, true)
	published := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	rel, err := Manifest(dir, "v1.2.3", "https://github.com/vilanobeachflorida/racksail/releases/download/v1.2.3/", "https://github.com/vilanobeachflorida/racksail/releases/tag/v1.2.3", published)
	if err != nil {
		t.Fatal(err)
	}
	if rel.Channel != updates.Stable || len(rel.Assets) != 6 {
		t.Fatalf("release = %+v", rel)
	}
	bin, ok := rel.Binary("arm64")
	if !ok || bin.URL != "https://github.com/vilanobeachflorida/racksail/releases/download/v1.2.3/racksail-linux-arm64" ||
		bin.Bundle != bin.URL+BundleSuffix || bin.Size != int64(len("arm64 binary")) || len(bin.SHA256) != 64 {
		t.Errorf("arm64 binary = %+v", bin)
	}
	// The manifest reads back as internal/updates expects it.
	data, _ := json.Marshal(rel)
	var back updates.Release
	if err := json.Unmarshal(data, &back); err != nil || back.Tag != "v1.2.3" || len(back.Assets) != 6 {
		t.Errorf("round trip = %+v, %v", back, err)
	}

	if _, err := Manifest(writeRelease(t, false), "v1.2.3", "https://example.com", "", published); err == nil {
		t.Error("a manifest of unsigned artifacts must fail")
	}
	if _, err := Manifest(dir, "v1.2.3-alpha", "https://example.com", "", published); err == nil {
		t.Error("a tag that isn't a release must fail")
	}
	os.Remove(filepath.Join(dir, "racksail-linux-amd64"))
	if _, err := Manifest(dir, "v1.2.3", "https://example.com", "", published); err == nil {
		t.Error("a release without a binary for every architecture must fail")
	}
}

func TestCosignPins(t *testing.T) {
	script, err := os.ReadFile("../install.sh")
	if err != nil {
		t.Fatal(err)
	}
	version, sums, err := CosignPins(script)
	if err != nil || !strings.HasPrefix(version, "v") || len(sums["amd64"]) != 64 || len(sums["arm64"]) != 64 {
		t.Fatalf("pins = %s %v %v", version, sums, err)
	}
	published := []byte(sums["amd64"] + "  cosign-linux-amd64\n" + sums["arm64"] + "  cosign-linux-arm64\n" + strings.Repeat("0", 64) + "  cosign-darwin-arm64\n")
	if err := CheckCosignPins(sums, published); err != nil {
		t.Error(err)
	}
	stale := bytes.Replace(published, []byte(sums["arm64"]), []byte(strings.Repeat("1", 64)), 1)
	if err := CheckCosignPins(sums, stale); err == nil || !strings.Contains(err.Error(), "cosign-linux-arm64") {
		t.Errorf("a changed checksum must be reported: %v", err)
	}
	if _, _, err := CosignPins([]byte("#!/bin/sh\n")); err == nil {
		t.Error("a script without pins must fail")
	}

	updated, err := UpdateCosignPins(script, stale)
	if err != nil {
		t.Fatal(err)
	}
	_, newSums, _ := CosignPins(updated)
	if newSums["arm64"] != strings.Repeat("1", 64) || newSums["amd64"] != sums["amd64"] {
		t.Errorf("updated pins = %v", newSums)
	}
	if !bytes.Equal(bytes.Replace(updated, []byte(strings.Repeat("1", 64)), []byte(sums["arm64"]), 1), script) {
		t.Error("updating the pins changed more than the checksums")
	}
	if _, err := UpdateCosignPins(script, []byte("nothing useful\n")); err == nil {
		t.Error("checksums without cosign's binaries must fail")
	}
}

func TestCommands(t *testing.T) {
	var out bytes.Buffer
	if err := run(context.Background(), []string{"channel", "v1.3.0-rc.1"}, nil, &out); err != nil || out.String() != "beta\n" {
		t.Errorf("channel = %q, %v", out.String(), err)
	}
	dir := writeRelease(t, true)
	if err := run(context.Background(), []string{"manifest", "-dir", dir, "-tag", "v1.2.3", "-base-url", "https://example.com/v1.2.3", "-time", "2026-10-15T12:00:00Z"}, nil, &out); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, ManifestFile))
	if err != nil || !bytes.Contains(data, []byte(`"channel": "stable"`)) || !bytes.Contains(data, []byte(`"published": "2026-10-15T12:00:00Z"`)) {
		t.Errorf("release.json = %s, %v", data, err)
	}
	if err := run(context.Background(), []string{"manifest", "-dir", dir}, nil, &out); err == nil {
		t.Error("manifest without a tag must fail")
	}
	if err := run(context.Background(), []string{"nonsense"}, nil, &out); err == nil {
		t.Error("an unknown command must fail")
	}

	out.Reset()
	tags := strings.NewReader("v1.2.0\nv1.3.0-beta.1\ncatalog\nv1.2.1\n")
	if err := run(context.Background(), []string{"previous", "-version", "1.3.0-beta.2"}, tags, &out); err != nil || out.String() != "v1.3.0-beta.1\n" {
		t.Errorf("previous = %q, %v", out.String(), err)
	}
	out.Reset()
	tags = strings.NewReader("v1.2.0\nv1.3.0-beta.1\n")
	if err := run(context.Background(), []string{"previous", "-version", "1.3.0-beta.2", "-channel", "stable"}, tags, &out); err != nil || out.String() != "v1.2.0\n" {
		t.Errorf("previous on the stable channel = %q, %v", out.String(), err)
	}
	out.Reset()
	if err := run(context.Background(), []string{"previous", "-version", "0.1.0"}, strings.NewReader(""), &out); err != nil || out.Len() != 0 {
		t.Errorf("before the first release, previous prints nothing: %q, %v", out.String(), err)
	}
	if err := run(context.Background(), []string{"previous"}, strings.NewReader(""), &out); err == nil {
		t.Error("previous without a version must fail")
	}

	for version, want := range map[string]string{"1.2.6": "false\n", "1.3.1": "true\n"} {
		out.Reset()
		tags := strings.NewReader("v1.2.5\nv1.3.0\ncatalog\n")
		if err := run(context.Background(), []string{"latest", "-version", version}, tags, &out); err != nil || out.String() != want {
			t.Errorf("latest -version %s = %q, %v; want %q", version, out.String(), err, want)
		}
	}
	if err := run(context.Background(), []string{"latest"}, strings.NewReader(""), &out); err == nil {
		t.Error("latest without a version must fail")
	}
}
