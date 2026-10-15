package updates

import (
	"runtime"
	"testing"
)

func TestBuild(t *testing.T) {
	b := Build()
	if b.Version != "0.0.0-dev" || b.Channel != "dev" || b.OS != runtime.GOOS || b.GoVersion == "" {
		t.Errorf("build = %+v", b)
	}
}

func TestReleaseBuild(t *testing.T) {
	defer func(v, c, ct, ch string) { version, commit, commitTime, channel = v, c, ct, ch }(version, commit, commitTime, channel)
	version, commit, commitTime, channel = "1.2.0", "e107092bcfd5d4d9e0ab371735a97927cbf7b035", "2026-10-15T17:50:02Z", "stable"
	b := Build()
	if b.Version != "1.2.0" || b.Commit != commit || b.CommitTime != commitTime || b.Channel != "stable" {
		t.Errorf("a release build reports what the release flags set: %+v", b)
	}
}

func TestParseAndCompare(t *testing.T) {
	order := []string{"0.9.0", "1.0.0-alpha", "1.0.0-alpha.1", "1.0.0-alpha.beta", "1.0.0-beta", "1.0.0-beta.2", "1.0.0-beta.11", "1.0.0-rc.1", "1.0.0", "1.0.1", "1.2.0", "v2.0.0"}
	for i := 0; i < len(order)-1; i++ {
		a, err := ParseVersion(order[i])
		if err != nil {
			t.Fatal(err)
		}
		b, err := ParseVersion(order[i+1])
		if err != nil {
			t.Fatal(err)
		}
		if a.Compare(b) != -1 || b.Compare(a) != 1 {
			t.Errorf("%s should come before %s", a, b)
		}
	}
	v, _ := ParseVersion("1.2.3-beta.1+20261015")
	if v.String() != "1.2.3-beta.1+20261015" {
		t.Errorf("String() = %q", v)
	}
	same, _ := ParseVersion("1.2.3-beta.1+other")
	if v.Compare(same) != 0 {
		t.Error("build metadata doesn't affect precedence")
	}
	for _, bad := range []string{"1.2", "1.2.x", "01.2.3", "", "1.2.3.4"} {
		if _, err := ParseVersion(bad); err == nil {
			t.Errorf("ParseVersion(%q) should fail", bad)
		}
	}
}

func TestLatest(t *testing.T) {
	mk := func(s string, c Channel) Release {
		v, _ := ParseVersion(s)
		return Release{Version: v, Tag: "v" + s, Channel: c}
	}
	releases := []Release{mk("1.0.0", Stable), mk("1.1.0-beta.1", Beta), mk("1.1.0-nightly.20261015", Nightly), mk("1.0.1", Stable)}
	current, _ := ParseVersion("1.0.0")
	if r, ok := Latest(releases, Stable, current); !ok || r.Tag != "v1.0.1" {
		t.Errorf("stable = %+v, %v", r, ok)
	}
	if r, ok := Latest(releases, Beta, current); !ok || r.Tag != "v1.1.0-beta.1" {
		t.Errorf("beta = %+v, %v", r, ok)
	}
	if r, ok := Latest(releases, Nightly, current); !ok || r.Tag != "v1.1.0-nightly.20261015" {
		t.Errorf("nightly = %+v, %v", r, ok)
	}
	newest, _ := ParseVersion("1.0.1")
	if _, ok := Latest(releases, Stable, newest); ok {
		t.Error("an up-to-date server gets nothing")
	}
	rel := Release{Assets: []Asset{{Name: "racksail-linux-arm64"}, {Name: "racksail-linux-amd64"}}}
	if a, ok := rel.Binary("arm64"); !ok || a.Name != "racksail-linux-arm64" {
		t.Errorf("binary = %+v", a)
	}
	if !Nightly.Valid() || Channel("edge").Valid() {
		t.Error("channel validation is wrong")
	}
}
