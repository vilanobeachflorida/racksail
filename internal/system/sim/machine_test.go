package sim

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/armor"

	"github.com/vilanobeachflorida/racksail/internal/system"
)

// clock is a settable time source for tests.
type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }

func TestFactsDescribeTheHardware(t *testing.T) {
	ctx := context.Background()
	b := New(Options{Hostname: "hw"}).Backend()
	facts, err := b.Facts.Facts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if facts.CPU.VirtualizationType != "vmx" || !facts.CPU.HasFlag("vmx") || !facts.CPU.HasFlag("aes") || facts.CPU.Vendor == "" || facts.TPMVersion != "2" {
		t.Errorf("cpu = %+v, tpm %q", facts.CPU, facts.TPMVersion)
	}
	if len(facts.Disks) != 4 || facts.Disks[0].Transport != "nvme" || !facts.Disks[2].Rotational {
		t.Errorf("disks = %+v", facts.Disks)
	}
	if len(facts.NICs) != 1 || facts.NICs[0].Name != "enp1s0" || facts.NICs[0].SpeedMbps != 1000 {
		t.Errorf("nics = %+v", facts.NICs)
	}
	noVT, _ := New(Options{NoVirtualization: true}).Backend().Facts.Facts(ctx)
	if noVT.CPU.Virtualization || noVT.CPU.HasFlag("vmx") || noVT.KVM {
		t.Errorf("without virtualization = %+v", noVT.CPU)
	}
	arm, _ := New(Options{Arch: "arm64"}).Backend().Facts.Facts(ctx)
	if !arm.CPU.HasFlag("asimd") || arm.CPU.VirtualizationType != "arm64" || arm.Kernel != "6.12.48+deb13-arm64" {
		t.Errorf("arm64 = %+v %s", arm.CPU, arm.Kernel)
	}
}

func TestJournal(t *testing.T) {
	ctx := context.Background()
	c := &clock{t: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)}
	m := New(Options{Clock: c.now})
	b := m.Backend()
	f := b.Files
	unit := "[Unit]\nDescription=Blog site\n[Service]\nUser=blog\nExecStart=/usr/bin/blog\nLogNamespace=blog\n"
	if err := f.WriteFile(ctx, "/etc/systemd/system/racksail-site-blog.service", []byte(unit), 0o644, system.Owner{}); err != nil {
		t.Fatal(err)
	}
	if err := b.Units.Start(ctx, "racksail-site-blog.service"); err != nil {
		t.Fatal(err)
	}
	c.advance(5 * time.Minute)
	m.Log("racksail-site-blog.service", system.PriorityError, "database connection refused")
	page, err := b.Journal.Read(ctx, system.JournalQuery{Namespace: "blog", Units: []string{"racksail-site-blog.service"}})
	if err != nil {
		t.Fatal(err)
	}
	var messages []string
	for _, e := range page.Entries {
		messages = append(messages, e.Message)
	}
	if !slices.Contains(messages, "Started Blog site.") || !slices.Contains(messages, "database connection refused") || len(messages) < 5 {
		t.Errorf("messages = %q", messages)
	}
	errs := system.PriorityError
	only, _ := b.Journal.Read(ctx, system.JournalQuery{Namespace: "blog", Priority: &errs})
	if len(only.Entries) != 1 || only.Entries[0].PID == 0 {
		t.Errorf("errors only = %+v", only.Entries)
	}
	// The system journal doesn't show an account's namespace.
	if sys, _ := b.Journal.Read(ctx, system.JournalQuery{Units: []string{"racksail-site-blog.service"}}); len(sys.Entries) != 0 {
		t.Errorf("namespaced entries leaked into the system journal: %+v", sys.Entries)
	}
	newest, _ := b.Journal.Read(ctx, system.JournalQuery{Namespace: "blog", Newest: true, Limit: 1})
	if len(newest.Entries) != 1 || newest.Entries[0].Message != "database connection refused" {
		t.Errorf("newest = %+v", newest.Entries)
	}
	first, _ := b.Journal.Read(ctx, system.JournalQuery{Units: []string{"ssh.service"}, Limit: 1})
	if len(first.Entries) != 1 {
		t.Fatalf("ssh entries = %+v", first.Entries)
	}
	b.Units.Restart(ctx, "ssh.service")
	next, _ := b.Journal.Read(ctx, system.JournalQuery{Units: []string{"ssh.service"}, After: first.Cursor})
	if len(next.Entries) == 0 || next.Entries[0].Message != "Stopping ssh.service..." {
		t.Errorf("after the cursor = %+v", next.Entries)
	}
	if _, err := b.Journal.Read(ctx, system.JournalQuery{After: "bogus"}); !errors.Is(err, system.ErrInvalid) {
		t.Errorf("a bad cursor: %v", err)
	}
	m.CrashUnit("racksail-site-blog.service")
	crash, _ := b.Journal.Read(ctx, system.JournalQuery{Namespace: "blog", Newest: true, Limit: 1})
	if !strings.Contains(crash.Entries[0].Message, "Failed with result 'signal'") {
		t.Errorf("crash = %+v", crash.Entries)
	}
}

func TestMetricsGrow(t *testing.T) {
	ctx := context.Background()
	c := &clock{t: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)}
	m := New(Options{Clock: c.now})
	b := m.Backend()
	first, err := b.Metrics.System(ctx)
	if err != nil {
		t.Fatal(err)
	}
	c.advance(time.Minute)
	second, _ := b.Metrics.System(ctx)
	if second.CPU.Total()-first.CPU.Total() != time.Duration(second.CPUs)*time.Minute {
		t.Errorf("a minute of CPU time across %d CPUs = %v", second.CPUs, second.CPU.Total()-first.CPU.Total())
	}
	busy := second.CPU.Busy() - first.CPU.Busy()
	if busy <= 0 || busy > time.Duration(second.CPUs)*time.Minute {
		t.Errorf("busy = %v", busy)
	}
	if second.Disks[0].WriteBytes <= first.Disks[0].WriteBytes || second.Networks[0].TxBytes <= first.Networks[0].TxBytes {
		t.Error("disk and network counters grow")
	}
	if second.Memory.UsedBytes() <= 0 || second.Memory.AvailableBytes <= 0 || len(second.Filesystems) == 0 {
		t.Errorf("memory = %+v, filesystems = %+v", second.Memory, second.Filesystems)
	}
	if second.Uptime != 72*time.Hour+time.Minute {
		t.Errorf("uptime = %v", second.Uptime)
	}
	units, err := b.Metrics.Units(ctx, "racksail-panel.service", "racksail.slice", "nothing.service")
	if err != nil || len(units) != 2 {
		t.Fatalf("units = %+v, %v", units, err)
	}
	if units[0].MemoryBytes == 0 || units[0].CPUUsage == 0 || units[1].MemoryBytes < units[0].MemoryBytes {
		t.Errorf("panel = %+v, slice = %+v", units[0], units[1])
	}
}

func TestSMART(t *testing.T) {
	ctx := context.Background()
	c := &clock{t: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)}
	m := New(Options{Clock: c.now})
	disks := m.Backend().Disks
	nvme, err := disks.SMART(ctx, "nvme1n1")
	if err != nil || nvme.Protocol != "NVMe" || !nvme.Healthy || nvme.AvailableSpare != 100 {
		t.Errorf("nvme = %+v, %v", nvme, err)
	}
	if err := disks.SelfTest(ctx, "sda", false); err != nil {
		t.Fatal(err)
	}
	if err := disks.SelfTest(ctx, "sda", true); !errors.Is(err, system.ErrInvalid) {
		t.Errorf("a second test while one runs: %v", err)
	}
	c.advance(time.Minute)
	running, _ := disks.SMART(ctx, "sda")
	if running.SelfTest == nil || running.SelfTest.Status != "running" || running.SelfTest.RemainingPercent != 50 {
		t.Errorf("running test = %+v", running.SelfTest)
	}
	c.advance(time.Minute)
	done, _ := disks.SMART(ctx, "sda")
	if done.SelfTest == nil || done.SelfTest.Status != "passed" || done.SelfTest.Type != "short" {
		t.Errorf("finished test = %+v", done.SelfTest)
	}
	m.DegradeDisk("sda")
	disks.SelfTest(ctx, "sda", false)
	c.advance(3 * time.Minute)
	bad, _ := disks.SMART(ctx, "sda")
	if bad.Healthy || bad.ReallocatedSectors != 48 || bad.SelfTest.Status != "failed" {
		t.Errorf("failing disk = %+v %+v", bad, bad.SelfTest)
	}
}

func TestUpgradesHoldsAndReboot(t *testing.T) {
	ctx := context.Background()
	m := New(Options{})
	b := m.Backend()
	pk := b.Packages
	if err := pk.Install(ctx, system.PackageRequest{Name: "mariadb-server"}); err != nil {
		t.Fatal(err)
	}
	m.OfferUpgrade("mariadb-server", "1:11.8.4-0+deb13u1")
	m.OfferUpgrade("linux-image-amd64", "6.12.57-1")
	m.OfferUpgrade("sudo", "1.9.16p2-4")
	if err := pk.SetHold(ctx, true, "mariadb-server"); err != nil {
		t.Fatal(err)
	}
	if held, _ := pk.Held(ctx); !slices.Equal(held, []string{"mariadb-server"}) {
		t.Errorf("held = %v", held)
	}
	if err := pk.Upgrade(ctx, false); err != nil {
		t.Fatal(err)
	}
	installed, _ := pk.Installed(ctx, "mariadb-server", "sudo", "linux-image-amd64")
	if installed[0].Version != "1:11.8.3-0+deb13u1" || installed[1].Version != "1.9.16p2-4" || installed[2].Version != "6.12.48-1" {
		t.Errorf("after a plain upgrade = %+v", installed)
	}
	if err := pk.Upgrade(ctx, true); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Files.Stat(ctx, "/boot/vmlinuz-6.12.57+deb13-amd64"); err != nil {
		t.Errorf("the new kernel isn't installed: %v", err)
	}
	before, _ := b.Facts.Facts(ctx)
	m.Reboot()
	after, _ := b.Facts.Facts(ctx)
	if after.Kernel != "6.12.57+deb13-amd64" || after.BootID == before.BootID {
		t.Errorf("after the reboot = %s %s", after.Kernel, after.BootID)
	}
	if st, _ := b.Units.Status(ctx, "ssh.service"); !st.Active() {
		t.Errorf("enabled units start at boot: %+v", st)
	}
	if st, _ := b.Units.Status(ctx, "racksail-edge.service"); st.Active() {
		t.Error("the edge isn't started at boot")
	}
	if err := pk.SetHold(ctx, true, "not-installed"); !errors.Is(err, system.ErrNotFound) {
		t.Errorf("holding a missing package: %v", err)
	}
}

func signedArchive(t *testing.T) (archive, sums, sig, public []byte) {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range map[string]string{"node/bin/node": "ELF", "node/README.md": "readme"} {
		tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg})
		tw.Write([]byte(body))
	}
	tw.Close()
	gz.Close()
	archive = buf.Bytes()
	sum := sha256.Sum256(archive)
	sums = []byte(hex.EncodeToString(sum[:]) + "  node.tar.gz\n")
	e, err := openpgp.NewEntity("Vendor", "", "v@vendor.test", nil)
	if err != nil {
		t.Fatal(err)
	}
	var s bytes.Buffer
	openpgp.DetachSign(&s, e, bytes.NewReader(sums), nil)
	var k bytes.Buffer
	w, _ := armor.Encode(&k, openpgp.PublicKeyType, nil)
	e.Serialize(w)
	w.Close()
	return archive, sums, s.Bytes(), k.Bytes()
}

func TestInstallArchiveVerifiesAndUnpacks(t *testing.T) {
	ctx := context.Background()
	m := New(Options{})
	b := m.Backend()
	archive, sums, sig, public := signedArchive(t)
	base := "https://vendor.test/dist/"
	m.SetDownload(base+"node.tar.gz", archive)
	m.SetDownload(base+"SHASUMS256.txt", sums)
	m.SetDownload(base+"SHASUMS256.txt.sig", sig)
	a := system.Archive{
		URL: base + "node.tar.gz", Size: int64(len(archive)), Format: "tar.gz", StripComponents: 1, Dest: "/opt/racksail/runtimes/node/22.11.0",
		Signature: &system.ArchiveSignature{Key: "vendor", URL: base + "SHASUMS256.txt.sig", Checksums: base + "SHASUMS256.txt", PublicKey: public},
	}
	if err := b.Packages.InstallArchive(ctx, a); err != nil {
		t.Fatal(err)
	}
	data, err := b.Files.ReadFile(ctx, "/opt/racksail/runtimes/node/22.11.0/bin/node", 0)
	if err != nil || string(data) != "ELF" {
		t.Errorf("unpacked = %q, %v", data, err)
	}
	entries, _ := b.Files.ReadDir(ctx, "/opt/racksail/runtimes/node")
	if len(entries) != 1 {
		t.Errorf("temporary files left behind: %+v", entries)
	}
	m.SetDownload(base+"SHASUMS256.txt", []byte(strings.Repeat("1", 64)+"  node.tar.gz\n"))
	forged := a
	forged.Dest = "/opt/racksail/runtimes/node/22.11.1"
	if err := b.Packages.InstallArchive(ctx, forged); !errors.Is(err, system.ErrInvalid) {
		t.Errorf("a forged checksums list: %v", err)
	}
	if _, err := b.Files.Stat(ctx, forged.Dest); !errors.Is(err, system.ErrNotFound) {
		t.Errorf("a rejected archive left %s behind: %v", forged.Dest, err)
	}
	missing := a
	missing.Dest = "/opt/racksail/runtimes/node/22.11.2"
	missing.Signature = &system.ArchiveSignature{Key: "vendor", URL: base + "nope.sig", PublicKey: public}
	if err := b.Packages.InstallArchive(ctx, missing); !errors.Is(err, system.ErrNotFound) {
		t.Errorf("a signature that can't be downloaded: %v", err)
	}
}

func TestTreeAndArchives(t *testing.T) {
	ctx := context.Background()
	m := New(Options{})
	f := m.Backend().Files
	f.Mkdir(ctx, "/srv/racksail/accounts/blog", 0o750, system.Owner{})
	f.Mkdir(ctx, "/srv/racksail/accounts/blog/site", 0o755, system.Owner{})
	f.WriteFile(ctx, "/srv/racksail/accounts/blog/site/index.html", []byte("hi"), 0o644, system.Owner{})
	f.Symlink(ctx, "index.html", "/srv/racksail/accounts/blog/site/home.html")
	f.Symlink(ctx, "../../../../../etc/os-release", "/srv/racksail/accounts/blog/site/escape")
	tr, err := f.OpenTree(ctx, "/srv/racksail/accounts/blog")
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	if _, err := tr.Open("site/escape"); !errors.Is(err, system.ErrInvalid) {
		t.Errorf("a link out of the tree: %v", err)
	}
	if r, err := tr.Open("site/home.html"); err != nil {
		t.Errorf("a link inside: %v", err)
	} else {
		r.Close()
	}
	stats, err := system.Pack(ctx, tr, system.PackSpec{Source: "site", Dest: "site.zip", Format: system.FormatZip, Exclude: []string{"escape"}})
	if err != nil || stats.Files != 1 || stats.Links != 1 {
		t.Fatalf("pack = %+v, %v", stats, err)
	}
	if _, err := system.Unpack(ctx, tr, system.UnpackSpec{Archive: "site.zip", Dest: "copy", Format: system.FormatZip, Owner: system.Owner{User: "racksail"}}); err != nil {
		t.Fatal(err)
	}
	info, err := f.Stat(ctx, "/srv/racksail/accounts/blog/copy/index.html")
	if err != nil || info.Owner != "racksail" {
		t.Errorf("unpacked = %+v, %v", info, err)
	}
}

func TestOpenKeepsTheMachine(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	m, err := Open(dir, Options{Hostname: "kept"})
	if err != nil {
		t.Fatal(err)
	}
	b := m.Backend()
	if err := b.Files.Mkdir(ctx, "/srv/racksail/accounts/blog", 0o755, system.Owner{}); err != nil {
		t.Fatal(err)
	}
	if err := b.Files.WriteFile(ctx, "/srv/racksail/accounts/blog/index.html", []byte("<h1>kept</h1>"), 0o644, system.Owner{}); err != nil {
		t.Fatal(err)
	}
	if err := b.Users.CreateUser(ctx, system.User{Name: "blog", UID: 20000, Home: "/srv/racksail/accounts/blog"}); err != nil {
		t.Fatal(err)
	}
	if err := b.Packages.Install(ctx, system.PackageRequest{Name: "knot"}); err != nil {
		t.Fatal(err)
	}
	m.Log("knot.service", system.PriorityInfo, "zone loaded")
	if err := m.Flush(); err != nil {
		t.Fatal(err)
	}
	host := m.HostPath("/srv/racksail/accounts/blog/index.html")
	if data, err := os.ReadFile(host); err != nil || string(data) != "<h1>kept</h1>" {
		t.Errorf("the file on the host = %q, %v", data, err)
	}
	if err := b.Files.Remove(ctx, "/srv/racksail/accounts/blog/index.html"); err != nil {
		t.Fatal(err)
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(host); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a removed file stays on the host: %v", err)
	}

	again, err := Open(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	b2 := again.Backend()
	if u, err := b2.Users.LookupUser(ctx, "blog"); err != nil || u.UID != 20000 {
		t.Errorf("user after reopening = %+v, %v", u, err)
	}
	if pkgs, _ := b2.Packages.Installed(ctx, "knot"); len(pkgs) != 1 {
		t.Error("installed packages are kept")
	}
	if st, _ := b2.Units.Status(ctx, "knot.service"); !st.Active() {
		t.Errorf("running services are kept: %+v", st)
	}
	if facts, _ := b2.Facts.Facts(ctx); facts.Hostname != "kept" {
		t.Errorf("hostname = %s", facts.Hostname)
	}
	page, _ := b2.Journal.Read(ctx, system.JournalQuery{Grep: "zone loaded"})
	if len(page.Entries) != 1 {
		t.Errorf("the journal is kept: %+v", page.Entries)
	}
	if _, err := os.Stat(filepath.Join(dir, "root", "srv", "racksail", "accounts", "blog")); err != nil {
		t.Errorf("the files are copied to the host again: %v", err)
	}
	os.WriteFile(filepath.Join(dir, "machine.json"), []byte(`{"version":99}`), 0o600)
	if _, err := Open(dir, Options{}); err == nil {
		t.Error("a machine saved by another version must be refused")
	}
}

func TestRealisticTimings(t *testing.T) {
	ctx := context.Background()
	fast := New(Options{}).Backend()
	start := time.Now()
	fast.Units.Restart(ctx, "ssh.service")
	if time.Since(start) > 50*time.Millisecond {
		t.Error("tests run without delays")
	}
	slow := New(Options{Realistic: true}).Backend()
	start = time.Now()
	slow.Units.Restart(ctx, "ssh.service")
	if d := time.Since(start); d < timings[opRestart] {
		t.Errorf("a realistic restart took %v", d)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	start = time.Now()
	slow.Packages.UpdateIndex(canceled)
	if time.Since(start) > 100*time.Millisecond {
		t.Error("a canceled context cuts the delay short")
	}
}
