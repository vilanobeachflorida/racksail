package packaging

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The install script runs against stand-ins for the commands it uses: curl
// serves a fake release, cosign accepts bundles that name the identity it is
// asked for, and the racksail binary records how it was called.
var stubs = map[string]string{
	"id":       `echo "${STUB_UID:-0}"`,
	"uname":    `echo "${STUB_MACHINE:-x86_64}"`,
	"hostname": `echo 203.0.113.10 2001:db8::10`,
	"curl": `out= url=
while [ $# -gt 0 ]; do
	case $1 in
	--output) out=$2; shift 2 ;;
	--proto | --retry) shift 2 ;;
	-*) shift ;;
	*) url=$1; shift ;;
	esac
done
echo "$url" >>"$STUB_LOG.urls"
case $url in
https://api.github.com/*) src=$STUB_FILES/releases.json ;;
https://github.com/sigstore/cosign/*) src=$STUB_FILES/cosign/${url##*/} ;;
*) dir=${url%/*}; src=$STUB_FILES/${dir##*/}/${url##*/} ;;
esac
[ -f "$src" ] || exit 22
cp "$src" "$out"`,
	"cosign": `[ "$1" = verify-blob ] || exit 1
shift
while [ $# -gt 0 ]; do
	case $1 in
	--bundle) bundle=$2; shift 2 ;;
	--certificate-identity) identity=$2; shift 2 ;;
	--certificate-oidc-issuer) issuer=$2; shift 2 ;;
	*) file=$1; shift ;;
	esac
done
echo "cosign $identity ${file##*/}" >>"$STUB_LOG"
[ "$issuer" = https://token.actions.githubusercontent.com ] && [ "$(cat "$bundle")" = "$identity" ]`,
}

// fakeRacksail stands in for a release binary.
const fakeRacksail = `#!/bin/sh
case $1 in
version) printf '{\n  "version": "%s",\n  "goVersion": "go1.27.2"\n}\n' "$STUB_BINARY_VERSION" ;;
install)
	echo "racksail $*" >>"$STUB_LOG"
	while [ $# -gt 0 ]; do
		[ "$1" != --answers ] || cp "$2" "$STUB_LOG.answers"
		shift
	done
	;;
esac
`

const workflow = "https://github.com/vilanobeachflorida/racksail/.github/workflows/release.yml"

type harness struct {
	t                     *testing.T
	dir, root, files, log string
	stubDir               string
	env                   []string
	tags                  []string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	dir := t.TempDir()
	h := &harness{t: t, dir: dir, root: filepath.Join(dir, "root"), files: filepath.Join(dir, "files"), log: filepath.Join(dir, "log"), stubDir: filepath.Join(dir, "bin")}
	for _, d := range []string{h.root + "/etc", h.files, h.stubDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	h.osRelease("debian", "13", "Debian GNU/Linux 13 (trixie)")
	for name, body := range stubs {
		h.write(filepath.Join(h.stubDir, name), "#!/bin/sh\n"+body+"\n", 0o755)
	}
	for _, tag := range []string{"v1.2.0", "v1.9.3", "v1.9.10", "v1.10.0-beta.2", "v1.10.0-rc.1", "v1.10.0-rc.1.nightly.20261014.7"} {
		h.publish(tag, "amd64")
	}
	h.publish("v1.9.10", "arm64")
	return h
}

func (h *harness) write(file, content string, mode os.FileMode) {
	h.t.Helper()
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		h.t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(content), mode); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) osRelease(id, version, name string) {
	h.write(filepath.Join(h.root, "etc/os-release"), fmt.Sprintf("PRETTY_NAME=%q\nID=%s\nVERSION_ID=%q\n", name, id, version), 0o644)
}

// publish puts a signed release in the fake download area.
func (h *harness) publish(tag, arch string) {
	identity := workflow + "@refs/tags/" + tag
	if strings.Contains(tag, "nightly") {
		identity = workflow + "@refs/heads/main"
	}
	asset := "racksail-linux-" + arch
	sum := sha256.Sum256([]byte(fakeRacksail))
	h.write(filepath.Join(h.files, tag, asset), fakeRacksail, 0o644)
	h.write(filepath.Join(h.files, tag, asset+".sigstore.json"), identity, 0o644)
	sums := ""
	if data, err := os.ReadFile(filepath.Join(h.files, tag, "SHA256SUMS")); err == nil {
		sums = string(data)
	}
	h.write(filepath.Join(h.files, tag, "SHA256SUMS"), sums+hex.EncodeToString(sum[:])+"  "+asset+"\n", 0o644)
	h.write(filepath.Join(h.files, tag, "SHA256SUMS.sigstore.json"), identity, 0o644)
	h.tags = append(h.tags, tag)
	var releases bytes.Buffer
	releases.WriteString("[\n")
	for i, tg := range h.tags {
		if i > 0 {
			releases.WriteString(",\n")
		}
		fmt.Fprintf(&releases, "  {\n    \"tag_name\": %q,\n    \"prerelease\": %v\n  }", tg, strings.Contains(tg, "-"))
	}
	releases.WriteString("\n]\n")
	h.write(filepath.Join(h.files, "releases.json"), releases.String(), 0o644)
}

type result struct {
	out     string
	err     error
	log     string
	urls    string
	answers string
}

func (h *harness) run(shell string, binaryVersion string, args ...string) result {
	h.t.Helper()
	os.Remove(h.log)
	os.Remove(h.log + ".urls")
	os.Remove(h.log + ".answers")
	cmd := exec.Command(shell, append([]string{"install.sh"}, args...)...)
	cmd.Env = append([]string{
		"PATH=" + h.stubDir + ":/usr/bin:/bin:/usr/sbin:/sbin",
		"HOME=" + h.dir,
		"TMPDIR=" + h.dir,
		"RACKSAIL_ROOT=" + h.root,
		"STUB_FILES=" + h.files,
		"STUB_LOG=" + h.log,
		"STUB_BINARY_VERSION=" + binaryVersion,
	}, h.env...)
	out, err := cmd.CombinedOutput()
	read := func(f string) string { data, _ := os.ReadFile(f); return string(data) }
	return result{out: string(out), err: err, log: read(h.log), urls: read(h.log + ".urls"), answers: read(h.log + ".answers")}
}

func shells(t *testing.T) []string {
	out := []string{"sh"}
	if _, err := os.Stat("/bin/dash"); err == nil {
		out = append(out, "/bin/dash")
	}
	return out
}

func TestInstallScriptPicksTheRelease(t *testing.T) {
	cases := []struct {
		args            []string
		version, binary string
		identity        string
	}{
		{nil, "1.9.10", "racksail-linux-amd64", "refs/tags/v1.9.10"},
		{[]string{"--channel", "beta"}, "1.10.0-rc.1", "racksail-linux-amd64", "refs/tags/v1.10.0-rc.1"},
		{[]string{"--channel", "nightly"}, "1.10.0-rc.1.nightly.20261014.7", "racksail-linux-amd64", "refs/heads/main"},
		{[]string{"--version", "1.2.0"}, "1.2.0", "racksail-linux-amd64", "refs/tags/v1.2.0"},
		{[]string{"--version", "v1.9.3"}, "1.9.3", "racksail-linux-amd64", "refs/tags/v1.9.3"},
	}
	for _, shell := range shells(t) {
		for _, tc := range cases {
			t.Run(shell+" "+strings.Join(tc.args, " "), func(t *testing.T) {
				t.Parallel()
				h := newHarness(t)
				r := h.run(shell, tc.version, append([]string{"--yes"}, tc.args...)...)
				if r.err != nil {
					t.Fatalf("%v\n%s", r.err, r.out)
				}
				tag := "v" + tc.version
				if !strings.Contains(r.urls, "/releases/download/"+tag+"/"+tc.binary+"\n") {
					t.Errorf("downloads:\n%s", r.urls)
				}
				for _, file := range []string{tc.binary, "SHA256SUMS"} {
					if !strings.Contains(r.log, "cosign "+workflow+"@"+tc.identity+" "+file+"\n") {
						t.Errorf("%s wasn't verified for %s:\n%s", file, tc.identity, r.log)
					}
				}
				if !strings.Contains(r.log, "racksail install --convert --binary /usr/local/bin/racksail --answers ") {
					t.Errorf("racksail wasn't run to convert the server:\n%s", r.log)
				}
				channel := "stable"
				if len(tc.args) == 2 && tc.args[0] == "--channel" {
					channel = tc.args[1]
				}
				if r.answers != "format: 1\nmode: convert\nchannel: "+channel+"\n" {
					t.Errorf("answers = %q", r.answers)
				}
				info, err := os.Stat(filepath.Join(h.root, "usr/local/bin/racksail"))
				if err != nil || info.Mode().Perm() != 0o755 {
					t.Errorf("the binary wasn't installed: %v %v", info, err)
				}
				if !strings.Contains(r.out, "https://203.0.113.10:8443") {
					t.Errorf("output:\n%s", r.out)
				}
				if tc.args != nil && tc.args[0] == "--version" && strings.Contains(r.urls, "api.github.com") {
					t.Error("an exact version needs no release listing")
				}
			})
		}
	}
}

func TestInstallScriptOptions(t *testing.T) {
	t.Run("arm64", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.env = append(h.env, "STUB_MACHINE=aarch64")
		r := h.run("sh", "1.9.10", "--yes")
		if r.err != nil || !strings.Contains(r.urls, "/v1.9.10/racksail-linux-arm64\n") {
			t.Fatalf("%v\n%s\n%s", r.err, r.out, r.urls)
		}
	})
	t.Run("hostname", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		r := h.run("sh", "1.9.10", "--yes", "--hostname", "panel.example.com")
		if r.err != nil || !strings.HasSuffix(r.answers, "hostname: panel.example.com\n") {
			t.Fatalf("%v\n%s\nanswers: %q", r.err, r.out, r.answers)
		}
	})
	t.Run("dry run", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		r := h.run("sh", "1.9.10", "--dry-run")
		if r.err != nil || !strings.Contains(r.log, "racksail install --convert --binary /usr/local/bin/racksail --answers ") || !strings.Contains(r.log, " --dry-run\n") {
			t.Fatalf("%v\n%s\n%s", r.err, r.out, r.log)
		}
		if _, err := os.Stat(filepath.Join(h.root, "usr/local/bin/racksail")); err == nil {
			t.Error("a dry run must not install the binary")
		}
	})
	t.Run("mirror", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		r := h.run("sh", "1.9.10", "--yes", "--mirror", "https://mirror.example.com/racksail/", "--version", "1.9.10")
		if r.err != nil || !strings.Contains(r.urls, "https://mirror.example.com/racksail/v1.9.10/racksail-linux-amd64\n") {
			t.Fatalf("%v\n%s\n%s", r.err, r.out, r.urls)
		}
	})
	t.Run("reinstall", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.write(filepath.Join(h.root, "usr/local/bin/racksail"), "old", 0o755)
		if r := h.run("sh", "1.9.10", "--yes"); r.err == nil || !strings.Contains(r.out, "already installed") {
			t.Fatalf("an existing install must be kept: %v\n%s", r.err, r.out)
		}
		if r := h.run("sh", "1.9.10", "--yes", "--reinstall"); r.err != nil {
			t.Fatalf("%v\n%s", r.err, r.out)
		}
		if data, _ := os.ReadFile(filepath.Join(h.root, "usr/local/bin/racksail")); string(data) != fakeRacksail {
			t.Error("--reinstall didn't replace the binary")
		}
	})
}

func TestInstallScriptRefuses(t *testing.T) {
	cases := []struct {
		name    string
		prepare func(h *harness)
		args    []string
		version string
		want    string
	}{
		{"not root", func(h *harness) { h.env = append(h.env, "STUB_UID=1000") }, nil, "1.9.10", "run this script as root"},
		{"another release", func(h *harness) { h.osRelease("debian", "12", "Debian GNU/Linux 12 (bookworm)") }, nil, "1.9.10", "needs Debian 13 (trixie), and this is Debian GNU/Linux 12 (bookworm)"},
		{"another distribution", func(h *harness) { h.osRelease("ubuntu", "24.04", "Ubuntu 24.04 LTS") }, nil, "1.9.10", "needs Debian 13"},
		{"another architecture", func(h *harness) { h.env = append(h.env, "STUB_MACHINE=riscv64") }, nil, "1.9.10", "not riscv64"},
		{"installed from the package", func(h *harness) {
			h.write(filepath.Join(h.root, "usr/bin/racksail"), "packaged", 0o755)
		}, []string{"--reinstall"}, "1.9.10", "installed from its Debian package"},
		{"bad signature", func(h *harness) {
			h.write(filepath.Join(h.files, "v1.9.10/racksail-linux-amd64.sigstore.json"), workflow+"@refs/tags/v1.2.0", 0o644)
		}, nil, "1.9.10", "racksail-linux-amd64 isn't signed by Racksail's release workflow for v1.9.10"},
		{"bad checksum", func(h *harness) {
			h.write(filepath.Join(h.files, "v1.9.10/SHA256SUMS"), strings.Repeat("0", 64)+"  racksail-linux-amd64\n", 0o644)
		}, nil, "1.9.10", "doesn't match its checksum"},
		{"another version inside", nil, nil, "1.2.0", "reports version 1.2.0, not 1.9.10"},
		{"no terminal to ask on", nil, []string{"--no-yes"}, "1.9.10", "run it again with --yes"},
		{"mirror without a version", nil, []string{"--mirror", "https://mirror.example.com"}, "1.9.10", "--mirror needs --version"},
		{"plain http mirror", nil, []string{"--mirror", "http://mirror.example.com", "--version", "1.9.10"}, "1.9.10", "must be an https:// URL"},
		{"unknown channel", nil, []string{"--channel", "edge"}, "1.9.10", "not edge"},
		{"unknown option", nil, []string{"--frobnicate"}, "1.9.10", "unknown option --frobnicate"},
		{"bad hostname", nil, []string{"--hostname", "Not A Host"}, "1.9.10", "isn't a hostname"},
		{"bad version", nil, []string{"--version", "latest"}, "1.9.10", "isn't a version"},
		{"nothing published", func(h *harness) { h.write(filepath.Join(h.files, "releases.json"), "[]\n", 0o644) }, nil, "1.9.10", "no stable release has been published yet"},
		{"cosign download tampered", func(h *harness) {
			os.Remove(filepath.Join(h.stubDir, "cosign"))
			h.write(filepath.Join(h.files, "cosign/cosign-linux-amd64"), "#!/bin/sh\nexit 0\n", 0o644)
		}, nil, "1.9.10", "the cosign download doesn't match the checksum in this script"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t)
			if tc.prepare != nil {
				tc.prepare(h)
			}
			args := []string{"--yes"}
			if len(tc.args) > 0 && tc.args[0] == "--no-yes" {
				args = nil
			} else {
				args = append(args, tc.args...)
			}
			r := h.run("sh", tc.version, args...)
			if r.err == nil {
				t.Fatalf("the script should fail with %q:\n%s", tc.want, r.out)
			}
			if !strings.Contains(r.out, tc.want) {
				t.Errorf("output:\n%s\nwant %q", r.out, tc.want)
			}
			if _, err := os.Stat(filepath.Join(h.root, "usr/local/bin/racksail")); err == nil {
				t.Error("nothing may be installed when the script fails")
			}
			if strings.Contains(r.log, "racksail install") {
				t.Error("the server must not be converted when the script fails")
			}
		})
	}
}

func TestInstallScriptLeavesNothingBehind(t *testing.T) {
	h := newHarness(t)
	if r := h.run("sh", "1.9.10", "--yes"); r.err != nil {
		t.Fatalf("%v\n%s", r.err, r.out)
	}
	entries, _ := os.ReadDir(h.dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "tmp.") {
			t.Errorf("the script left %s behind", e.Name())
		}
	}
}
