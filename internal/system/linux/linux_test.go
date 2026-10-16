package linux

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/system"
)

// fakeRunner records commands and answers them from a handler.
type fakeRunner struct {
	mu      sync.Mutex
	calls   []system.Command
	handler func(cmd system.Command) (system.Output, error)
	// stdin is what the last streamed program read.
	stdin []byte
}

func (f *fakeRunner) Run(ctx context.Context, cmd system.Command) (system.Output, error) {
	if err := cmd.Validate(); err != nil {
		return system.Output{}, err
	}
	f.mu.Lock()
	f.calls = append(f.calls, cmd)
	f.mu.Unlock()
	if f.handler == nil {
		return system.Output{}, nil
	}
	return f.handler(cmd)
}

func (f *fakeRunner) Stream(ctx context.Context, cmd system.Command, in io.Reader, out io.Writer) error {
	f.mu.Lock()
	f.calls = append(f.calls, cmd)
	f.mu.Unlock()
	if in == nil {
		return nil
	}
	// The program reads all it is given, as btrfs receive does, and fails
	// when its input breaks off.
	data, err := io.ReadAll(in)
	f.mu.Lock()
	f.stdin = data
	f.mu.Unlock()
	if err != nil {
		return &system.ExitError{Program: cmd.Path, ExitCode: 1, Stderr: "ERROR: short read from stream"}
	}
	return nil
}

func (f *fakeRunner) argv() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.calls {
		out = append(out, strings.Join(append([]string{c.Path}, c.Args...), " "))
	}
	return out
}

// fakeSystemd records unit operations.
type fakeSystemd struct {
	ops   []string
	props map[string]map[string]any
}

func (f *fakeSystemd) DaemonReload(ctx context.Context) error {
	f.ops = append(f.ops, "daemon-reload")
	return nil
}
func (f *fakeSystemd) Start(ctx context.Context, n string) error   { return f.op("start", n) }
func (f *fakeSystemd) Stop(ctx context.Context, n string) error    { return f.op("stop", n) }
func (f *fakeSystemd) Restart(ctx context.Context, n string) error { return f.op("restart", n) }
func (f *fakeSystemd) Reload(ctx context.Context, n string) error  { return f.op("reload", n) }
func (f *fakeSystemd) Enable(ctx context.Context, n []string) error {
	return f.op("enable", strings.Join(n, ","))
}
func (f *fakeSystemd) Disable(ctx context.Context, n []string) error {
	return f.op("disable", strings.Join(n, ","))
}
func (f *fakeSystemd) Properties(ctx context.Context, n string) (map[string]any, error) {
	if p, ok := f.props[n]; ok {
		return p, nil
	}
	return map[string]any{"LoadState": "not-found", "ActiveState": "inactive", "SubState": "dead"}, nil
}
func (f *fakeSystemd) List(ctx context.Context, patterns []string) ([]string, error) {
	var out []string
	for n := range f.props {
		for _, p := range patterns {
			if ok, _ := filepath.Match(p, n); ok {
				out = append(out, n)
			}
		}
	}
	return out, nil
}
func (f *fakeSystemd) Kill(ctx context.Context, n string, sig int32) error {
	return f.op("kill", fmt.Sprintf("%s:%d", n, sig))
}
func (f *fakeSystemd) ResetFailed(ctx context.Context, n string) error {
	return f.op("reset-failed", n)
}
func (f *fakeSystemd) op(verb, name string) error {
	f.ops = append(f.ops, verb+" "+name)
	return nil
}

// testRoot builds a machine root whose account files map both root and
// "tester" to the user running the tests, so ownership changes need no
// privileges.
func testRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	uid, gid := os.Getuid(), os.Getgid()
	write(t, root, "/etc/passwd", fmt.Sprintf("root:x:%d:%d:root:/root:/bin/bash\ntester:x:%d:%d:Tester,,,:/home/tester:/bin/bash\nblog:x:20001:20001::/srv/racksail/accounts/blog:/usr/sbin/nologin\n", uid, gid, uid, gid))
	write(t, root, "/etc/group", fmt.Sprintf("root:x:%d:\ntesters:x:%d:\nusers:x:100:blog,tester\nblog:x:20001:\n", gid, gid))
	for _, dir := range []string{"etc/systemd/system", "etc/systemd/network"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write(t, root, "/etc/shadow", "root:*:19000:0:99999:7:::\ntester:$y$hash:19000::::::\nblog:!:19000::::::\n")
	write(t, root, "/etc/subuid", "blog:1065536:65536\n")
	return root
}

func write(t *testing.T, root, p, content string) {
	t.Helper()
	full := filepath.Join(root, p)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func newTestBackend(t *testing.T, handler func(system.Command) (system.Output, error)) (*system.Backend, *fakeRunner, *fakeSystemd, string) {
	t.Helper()
	root := testRoot(t)
	runner := &fakeRunner{handler: handler}
	sd := &fakeSystemd{props: map[string]map[string]any{}}
	b := NewWith(Options{Root: root, Runner: runner, Systemd: sd})
	if err := b.Check(); err != nil {
		t.Fatal(err)
	}
	return b, runner, sd, root
}

func TestAccounts(t *testing.T) {
	ctx := context.Background()
	b, runner, _, _ := newTestBackend(t, nil)
	blog, err := b.Users.LookupUser(ctx, "blog")
	if err != nil {
		t.Fatal(err)
	}
	if !blog.Locked || blog.SubIDs == nil || blog.SubIDs.Start != 1065536 || !slices.Contains(blog.Groups, "users") || blog.System {
		t.Errorf("blog = %+v", blog)
	}
	tester, _ := b.Users.LookupUser(ctx, "tester")
	if tester.Comment != "Tester" || tester.Locked {
		t.Errorf("tester = %+v", tester)
	}
	if _, err := b.Users.LookupUser(ctx, "nobody-here"); !errors.Is(err, system.ErrNotFound) {
		t.Errorf("missing user: %v", err)
	}

	err = b.Users.CreateUser(ctx, system.User{Name: "shop", UID: 20002, Home: "/srv/racksail/accounts/shop", SubIDs: &system.IDRange{Start: 1131072, Count: 65536}})
	if err != nil {
		t.Fatal(err)
	}
	blog.Locked = false
	blog.SubIDs = &system.IDRange{Start: 2000000, Count: 65536}
	blog.Groups = nil
	if err := b.Users.UpdateUser(ctx, blog); err != nil {
		t.Fatal(err)
	}
	if err := b.Users.CreateUser(ctx, system.User{Name: "--evil"}); !errors.Is(err, system.ErrInvalid) {
		t.Errorf("a name that looks like an option must be refused: %v", err)
	}
	want := []string{
		"/usr/sbin/useradd --no-create-home --shell /usr/sbin/nologin -K SUB_UID_COUNT=0 -K SUB_GID_COUNT=0 --home-dir /srv/racksail/accounts/shop --uid 20002 --user-group -- shop",
		"/usr/sbin/usermod --add-subuids 1131072-1196607 --add-subgids 1131072-1196607 -- shop",
		"/usr/sbin/usermod --shell /usr/sbin/nologin --comment  --groups  --unlock --del-subuids 1065536-1131071 --del-subgids 1065536-1131071 --add-subuids 2000000-2065535 --add-subgids 2000000-2065535 -- blog",
	}
	if got := runner.argv(); !slices.Equal(got, want) {
		t.Errorf("commands:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestFilesAdapter(t *testing.T) {
	ctx := context.Background()
	b, _, _, root := newTestBackend(t, nil)
	f := b.Files
	me := system.Owner{User: "tester", Group: "testers"}
	if err := f.Mkdir(ctx, "/srv", 0o755, me); err != nil {
		t.Fatal(err)
	}
	if err := f.Mkdir(ctx, "/srv", 0o755, me); !errors.Is(err, system.ErrExists) {
		t.Errorf("second mkdir: %v", err)
	}
	if err := f.WriteFile(ctx, "/srv/site.conf", []byte("v1"), 0o640, me); err != nil {
		t.Fatal(err)
	}
	if err := f.WriteFile(ctx, "/srv/site.conf", []byte("v2"), 0o600, me); err != nil {
		t.Fatal(err)
	}
	info, err := f.Stat(ctx, "/srv/site.conf")
	if err != nil || info.Size != 2 || info.Perm() != 0o600 || info.UID != os.Getuid() || info.Owner == "" {
		t.Errorf("stat = %+v, %v", info, err)
	}
	data, _ := f.ReadFile(ctx, "/srv/site.conf", 0)
	if string(data) != "v2" {
		t.Errorf("content = %q", data)
	}
	if _, err := f.ReadFile(ctx, "/srv/site.conf", 1); !errors.Is(err, system.ErrTooLarge) {
		t.Errorf("limit: %v", err)
	}
	if err := f.Symlink(ctx, "site.conf", "/srv/link"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.ReadFile(ctx, "/srv/link", 0); err == nil {
		t.Error("reading through a final symbolic link must fail")
	}
	if info, _ := f.Stat(ctx, "/srv/link"); !info.IsSymlink() || info.Target != "site.conf" {
		t.Errorf("symlink stat = %+v", info)
	}
	if err := f.Chmod(ctx, "/srv/link", 0o777); !errors.Is(err, system.ErrInvalid) {
		t.Errorf("chmod through a symlink: %v", err)
	}
	entries, _ := f.ReadDir(ctx, "/srv")
	if len(entries) != 2 {
		t.Errorf("entries = %+v", entries)
	}
	if err := f.Remove(ctx, "/srv"); !errors.Is(err, system.ErrNotEmpty) {
		t.Errorf("removing a non-empty dir: %v", err)
	}
	if err := f.WriteFile(ctx, "/srv/x", nil, 0o644, system.Owner{User: "ghost"}); !errors.Is(err, system.ErrNotFound) {
		t.Errorf("unknown owner: %v", err)
	}
	if err := f.WriteFile(ctx, "/srv/../etc/passwd", nil, 0o644, me); !errors.Is(err, system.ErrInvalid) {
		t.Errorf("unclean path: %v", err)
	}
	leftovers, _ := filepath.Glob(filepath.Join(root, "srv", ".racksail-*"))
	if len(leftovers) != 0 {
		t.Errorf("temporary files left behind: %v", leftovers)
	}
}

func TestUnitsAdapter(t *testing.T) {
	ctx := context.Background()
	b, runner, sd, root := newTestBackend(t, nil)
	sd.props["racksail-site-blog.service"] = map[string]any{
		"Description": "Blog", "LoadState": "loaded", "ActiveState": "active", "SubState": "running",
		"UnitFileState": "enabled", "MainPID": uint32(4242), "MemoryCurrent": uint64(32 << 20),
		"CPUUsageNSec": uint64(1_500_000_000), "TasksCurrent": uint64(3), "NRestarts": uint32(1),
		"ActiveEnterTimestamp": uint64(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).UnixMicro()),
	}
	if err := b.Units.WriteUnit(ctx, "racksail-site-blog.service", []byte("[Service]\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "etc/systemd/system/racksail-site-blog.service")); err != nil {
		t.Errorf("unit file not written: %v", err)
	}
	if err := b.Units.WriteDropIn(ctx, "php8.4-fpm.service", "50-racksail", []byte("[Service]\nLimitNOFILE=65536\n")); err != nil {
		t.Fatal(err)
	}
	st, err := b.Units.Status(ctx, "racksail-site-blog.service")
	if err != nil || !st.Active() || st.MainPID != 4242 || st.MemoryBytes != 32<<20 || st.CPUTime != 1500*time.Millisecond || st.ActiveSince.Year() != 2026 {
		t.Errorf("status = %+v, %v", st, err)
	}
	list, _ := b.Units.List(ctx, "racksail-*")
	if len(list) != 1 {
		t.Errorf("list = %+v", list)
	}
	if err := b.Units.Kill(ctx, "racksail-site-blog.service", system.SIGUSR1); err != nil {
		t.Fatal(err)
	}
	if err := b.Units.SetProperties(ctx, "racksail-accounts-blog.slice", true, system.UnitProperty{Name: "MemoryMax", Value: "512M"}, system.UnitProperty{Name: "CPUQuota", Value: "50%"}); err != nil {
		t.Fatal(err)
	}
	if err := b.Units.SetProperties(ctx, "racksail-accounts-blog.slice", true, system.UnitProperty{Name: "ExecStart", Value: "/bin/sh -c x"}); !errors.Is(err, system.ErrInvalid) {
		t.Errorf("unsafe property value: %v", err)
	}
	if err := b.Units.Start(ctx, "../evil.service"); !errors.Is(err, system.ErrInvalid) {
		t.Errorf("bad unit name: %v", err)
	}
	if !slices.Contains(sd.ops, "kill racksail-site-blog.service:10") {
		t.Errorf("USR1 should be signal 10 on Linux: %v", sd.ops)
	}
	if got := runner.argv(); len(got) != 1 || got[0] != "/usr/bin/systemctl set-property --runtime -- racksail-accounts-blog.slice MemoryMax=512M CPUQuota=50%" {
		t.Errorf("commands = %v", got)
	}
}

// ResourceControl reads the effective limits from systemd's D-Bus properties
// with runtime true: the base-unit values, infinity for an unset limit, and
// never the AllowedCPUs bitmask, which can't be reduced with certainty. With
// runtime false it reads the persistent configuration from the unit file and
// its drop-ins instead, so a persistent change isn't fooled by a runtime
// override.
func TestUnitsResourceControl(t *testing.T) {
	ctx := context.Background()
	b, _, sd, _ := newTestBackend(t, nil)
	name := "racksail-accounts-blog.slice"
	sd.props[name] = map[string]any{
		"LoadState": "loaded", "ActiveState": "active",
		"MemoryMax":          uint64(2 << 30),
		"MemoryHigh":         ^uint64(0),
		"CPUQuotaPerSecUSec": uint64(500000),
		"CPUWeight":          uint64(200),
		"TasksMax":           uint64(512),
		"AllowedCPUs":        []byte{0x3},
	}
	eff, err := b.Units.ResourceControl(ctx, name, true)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"MemoryMax": "2147483648", "MemoryHigh": "infinity", "CPUQuota": "500000", "CPUWeight": "200", "TasksMax": "512"}
	for k, v := range want {
		if eff[k] != v {
			t.Errorf("effective %s = %q, want %q", k, eff[k], v)
		}
	}
	if _, ok := eff["AllowedCPUs"]; ok {
		t.Errorf("AllowedCPUs is a bitmask and must not be read: %v", eff)
	}

	// The persistent view reads the unit file and its drop-ins, last wins, and
	// ignores the live D-Bus values.
	if err := b.Units.WriteUnit(ctx, name, []byte("[Slice]\nMemoryMax=1G\nTasksMax=256\n")); err != nil {
		t.Fatal(err)
	}
	if err := b.Units.WriteDropIn(ctx, name, "50-racksail", []byte("[Slice]\nMemoryMax=3G\n")); err != nil {
		t.Fatal(err)
	}
	persistent, err := b.Units.ResourceControl(ctx, name, false)
	if err != nil {
		t.Fatal(err)
	}
	if persistent["MemoryMax"] != "3221225472" || persistent["TasksMax"] != "256" {
		t.Errorf("persistent limits = %v", persistent)
	}
	if persistent["CPUWeight"] != "" {
		t.Errorf("persistent view must not carry the live CPUWeight: %v", persistent)
	}
}

func TestPackagesParsing(t *testing.T) {
	installed := parseDpkgQuery([]byte("knot\t3.4.7-1\tamd64\t4096\tinstalled\nold\t1.0\tamd64\t10\tconfig-files\n"))
	if len(installed) != 1 || installed[0].InstalledBytes != 4096*1024 {
		t.Errorf("dpkg-query = %+v", installed)
	}
	inst := parseInst([]byte("NOTE: simulation\nInst php8.4-common (8.4.13-1+0~20251002 Sury:packages.sury.org [amd64])\nInst libc6 [2.41-11] (2.41-12 Debian:13.1/stable [arm64]) []\nConf php8.4-common (8.4.13-1)\n"))
	if len(inst) != 2 || inst[0].Origin != "Sury:packages.sury.org" || inst[1].Version != "2.41-12" || inst[1].Arch != "arm64" {
		t.Errorf("inst = %+v", inst)
	}
	sizes := parseAptShow([]byte("Package: knot\nVersion: 3.4.7-1\nInstalled-Size: 4000\nSize: 1048576\nDescription: dns\n\nPackage: unbound\nSize: 900\n"))
	if sizes["knot"].DownloadBytes != 1048576 || sizes["knot"].InstalledBytes != 4000*1024 || sizes["unbound"].DownloadBytes != 900 {
		t.Errorf("sizes = %+v", sizes)
	}
}

func TestPackagesCommands(t *testing.T) {
	ctx := context.Background()
	b, runner, _, root := newTestBackend(t, func(cmd system.Command) (system.Output, error) {
		if cmd.Path == progDpkgQuery {
			return system.Output{Stdout: []byte("knot\t3.4.7-1\tamd64\t10\tinstalled\n"), ExitCode: 1}, &system.ExitError{Program: cmd.Path, ExitCode: 1}
		}
		return system.Output{}, nil
	})
	got, err := b.Packages.Installed(ctx, "knot", "unknown-pkg")
	if err != nil || len(got) != 1 {
		t.Errorf("installed = %+v, %v", got, err)
	}
	if err := b.Packages.Install(ctx, system.PackageRequest{Name: "knot"}, system.PackageRequest{Name: "unbound", Version: "1.22.0-2"}); err != nil {
		t.Fatal(err)
	}
	if err := b.Packages.Install(ctx, system.PackageRequest{Name: "-oAPT::Evil=1"}); !errors.Is(err, system.ErrInvalid) {
		t.Errorf("option-like package names must be refused: %v", err)
	}
	src := system.AptSource{
		Name: "pgdg", URIs: []string{"https://apt.postgresql.org/pub/repos/apt"}, Suites: []string{"trixie-pgdg"}, Components: []string{"main"},
		Key: []byte("-----BEGIN PGP PUBLIC KEY BLOCK-----\nk\n-----END PGP PUBLIC KEY BLOCK-----\n"), Packages: []string{"postgresql-*"},
	}
	if err := b.Packages.AddSource(ctx, src); err != nil {
		t.Fatal(err)
	}
	if names, _ := b.Packages.Sources(ctx); !slices.Equal(names, []string{"pgdg"}) {
		t.Errorf("sources = %v", names)
	}
	if _, err := os.Stat(filepath.Join(root, "etc/apt/keyrings/racksail-pgdg.asc")); err != nil {
		t.Error(err)
	}
	if err := b.Packages.RemoveSource(ctx, "pgdg"); err != nil {
		t.Fatal(err)
	}
	argv := runner.argv()
	if !slices.Contains(argv, "/usr/bin/apt-get install -y -q --no-install-recommends -o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold -- knot unbound=1.22.0-2") {
		t.Errorf("install command missing from %v", argv)
	}
}

func tarGz(t *testing.T, files map[string]string, links map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	slices.Sort(names)
	for _, name := range names {
		body := files[name]
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		tw.Write([]byte(body))
	}
	for name, target := range links {
		if err := tw.WriteHeader(&tar.Header{Name: name, Linkname: target, Typeflag: tar.TypeSymlink}); err != nil {
			t.Fatal(err)
		}
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func TestInstallArchive(t *testing.T) {
	ctx := context.Background()
	good := tarGz(t, map[string]string{"node-v22/bin/node": "ELF", "node-v22/lib/x.js": "js"}, map[string]string{"node-v22/bin/npm": "../lib/x.js"})
	evil := tarGz(t, map[string]string{"node-v22/../../escape": "x"}, nil)
	badLink := tarGz(t, map[string]string{"node-v22/a": "x"}, map[string]string{"node-v22/link": "/etc/shadow"})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/good.tar.gz":
			w.Write(good)
		case "/evil.tar.gz":
			w.Write(evil)
		case "/badlink.tar.gz":
			w.Write(badLink)
		}
	}))
	defer srv.Close()
	root := testRoot(t)
	sum := func(data []byte) string { s := sha256.Sum256(data); return hex.EncodeToString(s[:]) }
	// Archives must come over https, so a TLS server stands in for the vendor.
	tls := httptest.NewTLSServer(srv.Config.Handler)
	defer tls.Close()
	b := NewWith(Options{Root: root, Runner: &fakeRunner{}, Systemd: &fakeSystemd{}, HTTP: tls.Client()})

	a := system.Archive{URL: tls.URL + "/good.tar.gz", SHA256: sum(good), Size: int64(len(good)), Format: "tar.gz", Dest: "/opt/racksail/runtimes/node/22.0.0", StripComponents: 1}
	if err := b.Packages.InstallArchive(ctx, a); err != nil {
		t.Fatal(err)
	}
	node, err := os.ReadFile(filepath.Join(root, "opt/racksail/runtimes/node/22.0.0/bin/node"))
	if err != nil || string(node) != "ELF" {
		t.Errorf("unpacked node = %q, %v", node, err)
	}
	if target, _ := os.Readlink(filepath.Join(root, "opt/racksail/runtimes/node/22.0.0/bin/npm")); target != "../lib/x.js" {
		t.Errorf("npm link = %q", target)
	}

	wrongSum := a
	wrongSum.Dest = "/opt/racksail/runtimes/node/22.0.1"
	wrongSum.SHA256 = strings.Repeat("0", 64)
	if err := b.Packages.InstallArchive(ctx, wrongSum); !errors.Is(err, system.ErrInvalid) {
		t.Errorf("checksum mismatch: %v", err)
	}
	tooBig := a
	tooBig.Dest = "/opt/racksail/runtimes/node/22.0.2"
	tooBig.Size = 10
	if err := b.Packages.InstallArchive(ctx, tooBig); !errors.Is(err, system.ErrTooLarge) {
		t.Errorf("size limit: %v", err)
	}
	for _, tc := range []struct {
		name string
		data []byte
	}{{"evil", evil}, {"badlink", badLink}} {
		bad := system.Archive{URL: tls.URL + "/" + tc.name + ".tar.gz", SHA256: sum(tc.data), Size: int64(len(tc.data)), Format: "tar.gz", Dest: "/opt/racksail/runtimes/x/" + tc.name}
		if err := b.Packages.InstallArchive(ctx, bad); !errors.Is(err, system.ErrInvalid) {
			t.Errorf("%s archive: %v", tc.name, err)
		}
		if _, err := os.Stat(filepath.Join(root, "opt/racksail/runtimes/x", tc.name)); err == nil {
			t.Errorf("%s archive left a destination behind", tc.name)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "escape")); err == nil {
		t.Error("an archive escaped its destination")
	}
}

func TestBtrfsParsing(t *testing.T) {
	mounts := parseMountinfo([]byte(`22 1 0:21 /@ / rw,noatime shared:1 - btrfs /dev/nvme0n1p2 rw,compress=zstd:1,subvol=/@
23 22 0:21 /@srv /srv/racksail rw,noatime shared:2 - btrfs /dev/nvme0n1p2 rw,subvol=/@srv
24 22 259:1 / /boot/efi rw,relatime shared:3 - vfat /dev/nvme0n1p1 rw
25 22 0:21 /@data /mnt/racksail/my\040data rw shared:4 - btrfs /dev/nvme0n1p2 rw,subvol=/@data
`))
	if len(mounts) != 4 || mounts[3].Target != "/mnt/racksail/my data" || mounts[1].Root != "/@srv" {
		t.Fatalf("mountinfo = %+v", mounts)
	}
	svs := parseSubvolumeList([]byte("ID 256 gen 10 top level 5 parent_uuid -                                    uuid 11111111-0000-0000-0000-000000000001 path @\nID 260 gen 12 top level 258 parent_uuid 11111111-0000-0000-0000-000000000002 uuid 11111111-0000-0000-0000-000000000003 path @srv/accounts/blog\n"))
	if len(svs) != 2 || svs[1].ParentUUID == "" || svs[0].ParentUUID != "" {
		t.Fatalf("subvolumes = %+v", svs)
	}
	// With -R, subvolumes that arrived by receive name where they came from.
	received := parseSubvolumeList([]byte("ID 256 gen 10 top level 5 parent_uuid -                                    received_uuid -                                    uuid 11111111-0000-0000-0000-000000000001 path @\n" +
		"ID 261 gen 14 top level 258 parent_uuid -                                    received_uuid 22222222-0000-0000-0000-000000000009 uuid 11111111-0000-0000-0000-000000000004 path @srv/restore/blog-1\n"))
	if len(received) != 2 || received[0].ReceivedUUID != "" || received[1].ReceivedUUID != "22222222-0000-0000-0000-000000000009" ||
		received[1].UUID != "11111111-0000-0000-0000-000000000004" || received[1].Path != "@srv/restore/blog-1" {
		t.Errorf("received subvolumes = %+v", received)
	}
	if m, ok := holdingMount(mounts, "/srv/racksail/accounts/blog"); !ok || m.Target != "/srv/racksail" {
		t.Errorf("the mount holding an account = %+v", m)
	}
	if m, ok := holdingMount(mounts, "/boot/efi/EFI"); !ok || m.FSType != "vfat" {
		t.Errorf("the mount holding the EFI partition = %+v", m)
	}
	if got := livePath(mounts, "/dev/nvme0n1p2", "@srv/accounts/blog"); got != "/srv/racksail/accounts/blog" {
		t.Errorf("livePath = %q", got)
	}
	if got := livePath(mounts, "/dev/nvme0n1p2", "@"); got != "/" {
		t.Errorf("livePath(@) = %q", got)
	}
	q, err := parseQgroupShow([]byte("qgroupid         rfer         excl     max_rfer     max_excl \n--------         ----         ----     --------     -------- \n0/260        16384        16384   1073741824         none \n"))
	if err != nil || q.ReferencedBytes != 16384 || q.LimitBytes != 1<<30 {
		t.Errorf("qgroup = %+v, %v", q, err)
	}
	u := parseFilesystemUsage([]byte("Overall:\n    Device size:\t\t 21474836480\n    Used:\t\t\t   397606912\n    Free (estimated):\t\t 20219850752\t(min: 20219850752)\nData,RAID1: Size:1073741824, Used:386924544 (36.04%)\n   /dev/sda\t1073741824\n   /dev/sdb\t1073741824\n\nMetadata,RAID1: Size:536870912, Used:5324800 (0.99%)\n   /dev/sda\t536870912\n   /dev/sdb\t536870912\n"))
	if u.SizeBytes != 21474836480 || u.Data != "raid1" || u.Metadata != "raid1" || len(u.Devices) != 2 {
		t.Errorf("usage = %+v", u)
	}
	s := parseScrubStatus([]byte("UUID: x\nScrub started:    Thu Oct 15 10:00:00 2026\nStatus:           finished\ndata_bytes_scrubbed: 1000\ntree_bytes_scrubbed: 24\ncsum_errors: 2\ncorrected_errors: 1\n"))
	if s.Running || s.BytesScrubbed != 1024 || s.Errors != 2 || s.CorrectedCount != 1 || s.StartedAt.Day() != 15 {
		t.Errorf("scrub = %+v", s)
	}
}

func TestDisksParsing(t *testing.T) {
	modern := []byte(`{"blockdevices":[
	 {"name":"nvme0n1","path":"/dev/nvme0n1","type":"disk","size":512110190592,"model":"SSD 512GB","serial":"S1","wwn":null,"rota":false,"tran":"nvme","rm":false,"fstype":null,"label":null,"uuid":null,"partuuid":null,"partn":null,"mountpoints":[null],
	  "children":[{"name":"nvme0n1p1","path":"/dev/nvme0n1p1","type":"part","size":1073741824,"fstype":"vfat","label":"EFI","uuid":"1A2B","partuuid":"p1","partn":1,"mountpoints":["/boot/efi"]},
	              {"name":"nvme0n1p2","path":"/dev/nvme0n1p2","type":"part","size":511035342336,"fstype":"btrfs","uuid":"u2","partn":2,"mountpoints":["/var/log","/"]}]},
	 {"name":"sda","path":"/dev/sda","type":"disk","size":4000787030016,"model":"HDD 4TB ","serial":"H1","rota":true,"tran":"sata","rm":false,"mountpoints":[null]},
	 {"name":"zram0","path":"/dev/zram0","type":"disk","size":1024,"mountpoints":["[SWAP]"]},
	 {"name":"loop0","path":"/dev/loop0","type":"loop","size":1}]}`)
	disks, err := parseLsblk(modern, map[string]string{"sda": "/dev/disk/by-id/ata-HDD_4TB_H1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(disks) != 3 || !disks[0].System || disks[1].System || !disks[1].Rotational || disks[1].Model != "HDD 4TB" || disks[1].ByID == "" {
		t.Fatalf("disks = %+v", disks)
	}
	if p := disks[0].Partitions[1]; p.Number != 2 || !slices.Equal(p.MountPoints, []string{"/", "/var/log"}) {
		t.Errorf("partition = %+v", p)
	}
	// A root filesystem on RAID and LVM still marks its disks as the
	// system's, and what's built on a disk keeps it in use.
	layered := []byte(`{"blockdevices":[
	 {"name":"sda","path":"/dev/sda","type":"disk","size":1000,"mountpoints":[null],"children":[
	  {"name":"sda1","path":"/dev/sda1","type":"part","partn":1,"fstype":"linux_raid_member","mountpoints":[null],"children":[
	   {"name":"md0","path":"/dev/md0","type":"raid1","fstype":"LVM2_member","mountpoints":[null],"children":[
	    {"name":"vg-root","path":"/dev/mapper/vg-root","type":"lvm","fstype":"ext4","mountpoints":["/"]}]}]}]},
	 {"name":"sdb","path":"/dev/sdb","type":"disk","size":1000,"mountpoints":[null],"children":[
	  {"name":"vg-data","path":"/dev/mapper/vg-data","type":"lvm","fstype":"xfs","mountpoints":["/srv/data"]}]},
	 {"name":"sdc","path":"/dev/sdc","type":"disk","size":1000,"mountpoints":[null],"children":[
	  {"name":"sdc1","path":"/dev/sdc1","type":"part","partn":1,"fstype":"crypto_LUKS","mountpoints":[null],"children":[
	   {"name":"luks-1","path":"/dev/mapper/luks-1","type":"crypt","mountpoints":[null]}]}]}]}`)
	disks, err = parseLsblk(layered, nil)
	if err != nil || len(disks) != 3 {
		t.Fatalf("layered disks = %+v, %v", disks, err)
	}
	if !disks[0].System || !slices.Equal(disks[0].Partitions[0].Holders, []string{"md0", "vg-root"}) {
		t.Errorf("a root on RAID and LVM = %+v", disks[0])
	}
	if disks[1].System || !slices.Equal(disks[1].Holders, []string{"vg-data"}) {
		t.Errorf("a disk with LVM on it = %+v", disks[1])
	}
	if disks[2].System || !slices.Equal(disks[2].Partitions[0].Holders, []string{"luks-1"}) {
		t.Errorf("an encrypted partition = %+v", disks[2])
	}
	old := []byte(`{"blockdevices":[{"name":"sdb","path":"/dev/sdb","type":"disk","size":"1000","rota":"1","rm":"0","mountpoints":[null]}]}`)
	disks, err = parseLsblk(old, nil)
	if err != nil || disks[0].SizeBytes != 1000 || !disks[0].Rotational {
		t.Errorf("old lsblk format = %+v, %v", disks, err)
	}
	report, err := parseSmartctl([]byte(`{"smart_support":{"available":true,"enabled":true},"smart_status":{"passed":true},"temperature":{"current":36},"power_on_time":{"hours":1200},
	 "ata_smart_attributes":{"table":[{"id":5,"name":"Reallocated_Sector_Ct","value":100,"worst":100,"thresh":10,"when_failed":"","raw":{"value":0}},{"id":197,"name":"Current_Pending_Sector","value":1,"worst":1,"thresh":0,"when_failed":"now","raw":{"value":8}}]}}`))
	if err != nil || !report.Supported || !report.Healthy || report.TemperatureC != 36 || !report.Attributes[1].Failing {
		t.Errorf("smart = %+v, %v", report, err)
	}
}

func TestDisksCommands(t *testing.T) {
	ctx := context.Background()
	b, runner, _, _ := newTestBackend(t, nil)
	if err := b.Disks.Partition(ctx, "sda", []system.PartitionSpec{{Number: 1, SizeBytes: 1 << 30, Type: "efi", Name: "EFI"}, {Number: 2, Type: "linux"}}); err != nil {
		t.Fatal(err)
	}
	if err := b.Disks.Format(ctx, system.FormatSpec{Type: "btrfs", Devices: []string{"/dev/sda2", "/dev/sdb"}, Label: "pool", Profiles: system.BtrfsProfiles{Data: "raid1", Metadata: "raid1c3"}}); err != nil {
		t.Fatal(err)
	}
	if err := b.Disks.Format(ctx, system.FormatSpec{Type: "ext4", Devices: []string{"/dev/sda1", "/dev/sdb"}}); !errors.Is(err, system.ErrInvalid) {
		t.Errorf("ext4 on two devices: %v", err)
	}
	if err := b.Disks.Wipe(ctx, "/dev/../etc/passwd"); !errors.Is(err, system.ErrInvalid) {
		t.Errorf("bad device: %v", err)
	}
	want := []string{
		"/usr/sbin/sgdisk --zap-all --new=1:0:+1048576K --typecode=1:EF00 --change-name=1:EFI --new=2:0:0 --typecode=2:8300 -- /dev/sda",
		"/usr/bin/udevadm settle",
		"/usr/sbin/mkfs.btrfs --label pool --data raid1 --metadata raid1c3 -- /dev/sda2 /dev/sdb",
	}
	if got := runner.argv(); !slices.Equal(got, want) {
		t.Errorf("commands:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestFirewallAdapter(t *testing.T) {
	ctx := context.Background()
	var applied []byte
	b, runner, _, _ := newTestBackend(t, func(cmd system.Command) (system.Output, error) {
		switch {
		case cmd.Path == progNft && slices.Equal(cmd.Args, []string{"--json", "list", "tables"}):
			return system.Output{Stdout: []byte(`{"nftables":[{"metainfo":{"version":"1.1.3"}},{"table":{"family":"inet","name":"racksail","handle":1}},{"table":{"family":"ip","name":"nat","handle":2}}]}`)}, nil
		case cmd.Path == progNft && slices.Contains(cmd.Args, "--file"):
			applied = cmd.Stdin
		case cmd.Path == progNft && len(cmd.Args) > 2 && cmd.Args[2] == "set":
			return system.Output{Stdout: []byte(`{"nftables":[{"set":{"family":"inet","name":"bans","table":"racksail","elem":["192.0.2.1",{"prefix":{"addr":"198.51.100.0","len":24}},{"elem":{"val":"203.0.113.9","timeout":3600,"expires":1800}}]}}]}`)}, nil
		}
		return system.Output{}, nil
	})
	ruleset := []byte("table inet racksail {\n  chain input { type filter hook input priority 0; policy drop; }\n}")
	if err := b.Firewall.Apply(ctx, ruleset); err != nil {
		t.Fatal(err)
	}
	if want := "delete table inet racksail\n" + string(ruleset) + "\n"; string(applied) != want {
		t.Errorf("script = %q, want %q", applied, want)
	}
	for _, bad := range []string{
		"table ip nat { }",
		"flush ruleset\n" + string(ruleset),
		"include \"/etc/shadow\"\n" + string(ruleset),
		string(ruleset) + "\nadd rule ip nat postrouting accept\n",
	} {
		before := len(runner.argv())
		if err := b.Firewall.Apply(ctx, []byte(bad)); !errors.Is(err, system.ErrInvalid) {
			t.Errorf("Apply(%q): %v", bad, err)
		}
		if err := b.Firewall.Check(ctx, []byte(bad)); !errors.Is(err, system.ErrInvalid) {
			t.Errorf("Check(%q): %v", bad, err)
		}
		// nft never sees a refused ruleset.
		if after := runner.argv(); len(after) != before {
			t.Errorf("ran %v for a refused ruleset", after[before:])
		}
	}
	bans := system.SetRef{Family: "inet", Table: "racksail", Set: "bans"}
	if err := b.Firewall.AddElements(ctx, bans, system.SetElement{Value: "192.0.2.7", Timeout: time.Hour}, system.SetElement{Value: "2001:db8::/32"}); err != nil {
		t.Fatal(err)
	}
	if err := b.Firewall.AddElements(ctx, bans, system.SetElement{Value: "1.2.3.4 } ; flush ruleset"}); !errors.Is(err, system.ErrInvalid) {
		t.Errorf("injected element: %v", err)
	}
	elems, err := b.Firewall.Elements(ctx, bans)
	if err != nil || len(elems) != 3 || elems[2].Value != "203.0.113.9" || elems[2].Timeout != 30*time.Minute || elems[1].Value != "198.51.100.0/24" {
		t.Errorf("elements = %+v, %v", elems, err)
	}
	if !slices.Contains(runner.argv(), "/usr/sbin/nft add element inet racksail bans { 192.0.2.7 timeout 3600s, 2001:db8::/32 }") {
		t.Errorf("add element command missing: %v", runner.argv())
	}
}

func TestNetworkAdapter(t *testing.T) {
	ctx := context.Background()
	b, runner, _, root := newTestBackend(t, func(cmd system.Command) (system.Output, error) {
		if cmd.Path != progIP {
			return system.Output{}, nil
		}
		if slices.Contains(cmd.Args, "address") {
			return system.Output{Stdout: []byte(`[{"ifindex":1,"ifname":"lo","flags":["LOOPBACK","UP","LOWER_UP"],"mtu":65536,"link_type":"loopback","address":"00:00:00:00:00:00","addr_info":[{"family":"inet","local":"127.0.0.1","prefixlen":8,"scope":"host"}]},
			 {"ifindex":2,"ifname":"enp1s0","flags":["BROADCAST","MULTICAST","UP","LOWER_UP"],"mtu":1500,"link_type":"ether","address":"52:54:00:12:34:56","master":"br0","addr_info":[{"family":"inet","local":"203.0.113.10","prefixlen":24,"scope":"global","dynamic":true}]},
			 {"ifindex":3,"ifname":"br0","flags":["UP"],"mtu":1500,"link_type":"ether","address":"52:54:00:aa:bb:cc","linkinfo":{"info_kind":"bridge"},"addr_info":[]}]`)}, nil
		}
		if cmd.Args[0] == "-6" {
			return system.Output{Stdout: []byte(`[{"dst":"default","gateway":"fe80::1","dev":"enp1s0","metric":1024},{"type":"local","dst":"::1","dev":"lo","table":"local"}]`)}, nil
		}
		return system.Output{Stdout: []byte(`[{"dst":"default","gateway":"203.0.113.1","dev":"enp1s0","metric":100},{"dst":"10.0.0.0/8","gateway":"10.1.1.1","dev":"enp1s0","table":"100"},{"type":"broadcast","dst":"203.0.113.255","dev":"enp1s0","table":"local"}]`)}, nil
	})
	write(t, root, "/sys/class/net/enp1s0/speed", "1000\n")
	ifaces, err := b.Network.Interfaces(ctx)
	if err != nil || len(ifaces) != 3 || ifaces[1].SpeedMbps != 1000 || ifaces[1].Master != "br0" || ifaces[2].Type != "bridge" || !ifaces[1].Addresses[0].Dynamic {
		t.Errorf("interfaces = %+v, %v", ifaces, err)
	}
	routes, err := b.Network.Routes(ctx)
	if err != nil || len(routes) != 3 || routes[0].Destination != "0.0.0.0/0" || routes[1].Table != 100 || routes[2].Destination != "::/0" {
		t.Errorf("routes = %+v, %v", routes, err)
	}
	write(t, root, "/etc/systemd/network/50-racksail-old.network", "[Match]\n")
	write(t, root, "/etc/systemd/network/10-cloud.network", "[Match]\n")
	if err := b.Network.ApplyConfig(ctx, map[string][]byte{"50-racksail-enp1s0.network": []byte("[Match]\nName=enp1s0\n")}); err != nil {
		t.Fatal(err)
	}
	cfg, _ := b.Network.Config(ctx)
	if len(cfg) != 1 || cfg["50-racksail-enp1s0.network"] == nil {
		t.Errorf("config = %v", cfg)
	}
	if _, err := os.Stat(filepath.Join(root, "etc/systemd/network/10-cloud.network")); err != nil {
		t.Error("files Racksail doesn't own must stay")
	}
	if !slices.Contains(runner.argv(), "/usr/bin/networkctl reload") {
		t.Errorf("networkd was not reloaded: %v", runner.argv())
	}
	if err := b.Network.SetHostname(ctx, "-bad"); !errors.Is(err, system.ErrInvalid) {
		t.Errorf("bad hostname: %v", err)
	}
}

func TestProcAndFacts(t *testing.T) {
	ctx := context.Background()
	b, _, _, root := newTestBackend(t, func(cmd system.Command) (system.Output, error) {
		switch cmd.Path {
		case progDetectVirt:
			return system.Output{Stdout: []byte("kvm\n")}, nil
		case progHostname:
			return system.Output{Stdout: []byte("web1.example.test\n")}, nil
		}
		return system.Output{}, nil
	})
	write(t, root, "/proc/stat", "cpu  1 2 3\nbtime 1760000000\n")
	write(t, root, "/proc/4242/stat", "4242 (php-fpm: pool (www)) S 1 4242 4242 0 -1 4194560 100 0 0 0 250 50 0 0 20 0 1 0 1000 123456 789 18446744073709551615\n")
	write(t, root, "/proc/4242/status", "Name:\tphp-fpm\nUid:\t20001\t20001\t20001\t20001\nVmRSS:\t   2048 kB\n")
	write(t, root, "/proc/4242/cmdline", "php-fpm: pool www\x00")
	write(t, root, "/proc/4242/cgroup", "0::/racksail.slice/racksail-accounts.slice/racksail-accounts-blog.slice/racksail-site-blog.service\n")
	write(t, root, "/proc/self/mountinfo", "")
	procs, err := b.Processes.List(ctx, system.ProcessFilter{User: "blog"})
	if err != nil || len(procs) != 1 {
		t.Fatalf("processes = %+v, %v", procs, err)
	}
	p := procs[0]
	if p.Command != "php-fpm: pool (www)" || p.RSSBytes != 2<<20 || p.CPUTime != 3*time.Second || p.Unit != "racksail-site-blog.service" || p.Started.Unix() != 1760000010 {
		t.Errorf("process = %+v", p)
	}
	if err := b.Processes.Signal(ctx, 1, system.SIGTERM); !errors.Is(err, system.ErrInvalid) {
		t.Errorf("signalling PID 1: %v", err)
	}

	write(t, root, "/proc/sys/net/core/somaxconn", "4096\n")
	if v, err := b.Sysctl.Get(ctx, "net.core.somaxconn"); err != nil || v != "4096" {
		t.Errorf("sysctl get = %q, %v", v, err)
	}
	if err := b.Sysctl.Set(ctx, "net.core.somaxconn", "8192"); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(root, "proc/sys/net/core/somaxconn")); string(data) != "8192\n" {
		t.Errorf("sysctl file = %q", data)
	}

	write(t, root, "/etc/os-release", "PRETTY_NAME=\"Debian GNU/Linux 13 (trixie)\"\nID=debian\nVERSION_ID=\"13\"\nVERSION_CODENAME=trixie\n")
	write(t, root, "/etc/hostname", "web1\n")
	write(t, root, "/proc/cpuinfo", "processor\t: 0\nmodel name\t: Test CPU\nphysical id\t: 0\ncore id\t: 0\nflags\t: fpu vmx sse\n\nprocessor\t: 1\nmodel name\t: Test CPU\nphysical id\t: 0\ncore id\t: 0\nflags\t: fpu vmx sse\n")
	write(t, root, "/proc/meminfo", "MemTotal:        8000000 kB\nSwapTotal:       1000000 kB\n")
	write(t, root, "/proc/sys/kernel/osrelease", "6.12.48+deb13-amd64\n")
	write(t, root, "/sys/firmware/efi/efivars/SecureBoot-8be4df61-93ca-11d2-aa0d-00e098032b8c", "\x06\x00\x00\x00\x01")
	write(t, root, "/sys/class/tpm/tpm0/uevent", "")
	write(t, root, "/etc/timezone", "Etc/UTC\n")
	facts, err := b.Facts.Facts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !facts.OS.IsDebian13() || facts.Hostname != "web1" || facts.FQDN != "web1.example.test" || facts.CPU.Threads != 2 || facts.CPU.Cores != 1 ||
		!facts.CPU.Virtualization || facts.MemoryBytes != 8000000*1024 || !facts.Firmware.UEFI || !facts.Firmware.SecureBoot || !facts.TPM ||
		facts.Virtualization != "kvm" || facts.Timezone != "Etc/UTC" || facts.BootTime.Unix() != 1760000000 {
		t.Errorf("facts = %+v", facts)
	}
}

func TestOSRunner(t *testing.T) {
	ctx := context.Background()
	r := &OSRunner{}
	out, err := r.Run(ctx, system.Command{Path: "/bin/echo", Args: []string{"hello"}})
	if err != nil || string(out.Stdout) != "hello\n" {
		t.Errorf("echo = %q, %v", out.Stdout, err)
	}
	_, err = r.Run(ctx, system.Command{Path: "/bin/ls", Args: []string{"/nonexistent-racksail-path"}})
	var exitErr *system.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode == 0 || !strings.Contains(err.Error(), "nonexistent-racksail-path") {
		t.Errorf("failing program: %v", err)
	}
	out, err = r.Run(ctx, system.Command{Path: "/usr/bin/head", Args: []string{"-c", "100", "/dev/zero"}, MaxOutput: 10})
	if err != nil || len(out.Stdout) != 10 {
		t.Errorf("output cap: %d bytes, %v", len(out.Stdout), err)
	}
	if _, err := r.Run(ctx, system.Command{Path: "/bin/sleep", Args: []string{"5"}, Timeout: 50 * time.Millisecond}); err == nil {
		t.Error("the timeout should stop the program")
	}
	if _, err := r.Run(ctx, system.Command{Path: "/bin/echo", User: "nobody"}); !errors.Is(err, system.ErrInvalid) {
		t.Errorf("running as a user without a lookup: %v", err)
	}
	var buf bytes.Buffer
	if err := r.Stream(ctx, system.Command{Path: "/bin/cat"}, strings.NewReader("streamed"), &buf); err != nil || buf.String() != "streamed" {
		t.Errorf("stream = %q, %v", buf.String(), err)
	}
}

// sendCommand builds one command of a Btrfs send stream, with its checksum.
func sendCommand(kind uint16, attrs ...[]byte) []byte {
	var payload []byte
	for _, a := range attrs {
		payload = append(payload, a...)
	}
	cmd := binary.LittleEndian.AppendUint32(nil, uint32(len(payload)))
	cmd = binary.LittleEndian.AppendUint16(cmd, kind)
	cmd = append(cmd, 0, 0, 0, 0)
	cmd = append(cmd, payload...)
	sum := ^crc32.Update(^uint32(0), crc32.MakeTable(crc32.Castagnoli), cmd)
	binary.LittleEndian.PutUint32(cmd[6:10], sum)
	return cmd
}

// sendAttribute is a type, length and value.
func sendAttribute(kind uint16, value []byte) []byte {
	a := binary.LittleEndian.AppendUint16(nil, kind)
	a = binary.LittleEndian.AppendUint16(a, uint16(len(value)))
	return append(a, value...)
}

func TestBtrfsReceiveIsConfined(t *testing.T) {
	ctx := context.Background()
	b, runner, _, _ := newTestBackend(t, nil)
	header := append([]byte("btrfs-stream\x00"), 1, 0, 0, 0)
	stream := slices.Concat(header,
		sendCommand(1, sendAttribute(15, []byte("blog")), sendAttribute(1, bytes.Repeat([]byte{1}, 16))),
		sendCommand(18, sendAttribute(15, []byte("su")), sendAttribute(5, binary.LittleEndian.AppendUint64(nil, 0o104755))),
		sendCommand(21))
	if err := b.Btrfs.Receive(ctx, "/srv/racksail/restore", bytes.NewReader(stream), nil); err != nil {
		t.Fatal(err)
	}
	if got := runner.argv(); len(got) != 1 || got[0] != "/usr/bin/btrfs receive --chroot -- /srv/racksail/restore" {
		t.Errorf("commands = %q", got)
	}
	// btrfs gets the stream with the set-user-ID bit taken off.
	if len(runner.stdin) != len(stream) || bytes.Contains(runner.stdin, binary.LittleEndian.AppendUint64(nil, 0o104755)) ||
		!bytes.Contains(runner.stdin, binary.LittleEndian.AppendUint64(nil, 0o100755)) {
		t.Errorf("btrfs got %x", runner.stdin)
	}
	// A stream the filter refuses fails with the filter's reason, not
	// btrfs's.
	device := slices.Concat(header, sendCommand(5, sendAttribute(15, []byte("sda")), sendAttribute(5, binary.LittleEndian.AppendUint64(nil, 0o060600))))
	err := b.Btrfs.Receive(ctx, "/srv/racksail/restore", bytes.NewReader(device), nil)
	if !errors.Is(err, system.ErrInvalid) || !strings.Contains(err.Error(), "device nodes") {
		t.Errorf("a device node: %v", err)
	}
}

func TestMountsAdapter(t *testing.T) {
	ctx := context.Background()
	b, runner, _, root := newTestBackend(t, nil)
	for _, dir := range []string{"mnt/racksail/pool", "etc/racksail"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write(t, root, "/mnt/racksail/file", "")
	if err := os.Symlink("/etc", filepath.Join(root, "mnt/racksail/link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("racksail", filepath.Join(root, "mnt/elsewhere")); err != nil {
		t.Fatal(err)
	}
	pool := system.Mount{Source: "/dev/disk/by-id/ata-HDD_H1", Target: "/mnt/racksail/pool", FSType: "btrfs", Options: []string{"noatime", "nosuid", "nodev"}}
	if err := b.Mounts.Mount(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := b.Mounts.Unmount(ctx, pool.Target); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"/usr/bin/mount -t btrfs -o noatime,nosuid,nodev -- /dev/disk/by-id/ata-HDD_H1 /mnt/racksail/pool",
		"/usr/bin/umount -- /mnt/racksail/pool",
	}
	if got := runner.argv(); !slices.Equal(got, want) {
		t.Errorf("commands = %q", got)
	}
	// mount and umount follow links in the paths they're given, so a
	// mount point reached through one is refused before they run.
	for _, target := range []string{"/mnt/racksail/link", "/mnt/elsewhere/pool", "/mnt/racksail/file", "/mnt/racksail/missing"} {
		bad := pool
		bad.Target = target
		if err := b.Mounts.Mount(ctx, bad); err == nil {
			t.Errorf("mounting at %s should fail", target)
		}
		if err := b.Mounts.Unmount(ctx, target); err == nil {
			t.Errorf("unmounting %s should fail", target)
		}
	}
	if err := b.Mounts.Unmount(ctx, "/"); !errors.Is(err, system.ErrInvalid) {
		t.Errorf("unmounting /: %v", err)
	}
	if got := runner.argv(); len(got) != len(want) {
		t.Errorf("ran %q for refused mount points", got[len(want):])
	}
}
