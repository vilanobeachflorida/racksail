package sim

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/system"
)

func newMachine(t *testing.T) (*Machine, *system.Backend) {
	t.Helper()
	m := New(Options{Hostname: "test"})
	b := m.Backend()
	if err := b.Check(); err != nil {
		t.Fatal(err)
	}
	return m, b
}

func TestFreshMachine(t *testing.T) {
	ctx := context.Background()
	_, b := newMachine(t)
	facts, err := b.Facts.Facts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !facts.OS.IsDebian13() || facts.Hostname != "test" || !facts.KVM {
		t.Errorf("facts = %+v", facts)
	}
	panel, err := b.Units.Status(ctx, "racksail-panel.service")
	if err != nil || !panel.Active() || panel.MainPID == 0 {
		t.Errorf("panel unit = %+v, %v", panel, err)
	}
	procs, _ := b.Processes.List(ctx, system.ProcessFilter{User: "racksail"})
	if len(procs) != 1 || procs[0].Unit != "racksail-panel.service" {
		t.Errorf("racksail processes = %+v", procs)
	}
	svs, err := b.Btrfs.Subvolumes(ctx, "/")
	if err != nil || len(svs) != 6 {
		t.Errorf("subvolumes = %d, %v", len(svs), err)
	}
}

func TestFiles(t *testing.T) {
	ctx := context.Background()
	_, b := newMachine(t)
	f := b.Files
	if err := f.WriteFile(ctx, "/nope/file", []byte("x"), 0o644, system.Owner{}); !errors.Is(err, system.ErrNotFound) {
		t.Errorf("missing parent: %v", err)
	}
	if err := f.Mkdir(ctx, "/srv/racksail/accounts/blog", 0o750, system.Owner{User: "racksail"}); err != nil {
		t.Fatal(err)
	}
	if err := f.Mkdir(ctx, "/srv/racksail/accounts/blog", 0o750, system.Owner{}); !errors.Is(err, system.ErrExists) {
		t.Errorf("second mkdir: %v", err)
	}
	if err := f.WriteFile(ctx, "/srv/racksail/accounts/blog/index.html", []byte("hello"), 0o640, system.Owner{User: "racksail", Group: "racksail"}); err != nil {
		t.Fatal(err)
	}
	info, err := f.Stat(ctx, "/srv/racksail/accounts/blog/index.html")
	if err != nil || info.Owner != "racksail" || info.Perm() != 0o640 || info.Size != 5 {
		t.Errorf("stat = %+v, %v", info, err)
	}
	if _, err := f.ReadFile(ctx, "/srv/racksail/accounts/blog/index.html", 2); !errors.Is(err, system.ErrTooLarge) {
		t.Errorf("read limit: %v", err)
	}
	if err := f.Remove(ctx, "/srv/racksail/accounts/blog"); !errors.Is(err, system.ErrNotEmpty) {
		t.Errorf("remove non-empty dir: %v", err)
	}
	if err := f.Rename(ctx, "/srv/racksail/accounts/blog", "/srv/racksail/accounts/shop"); err != nil {
		t.Fatal(err)
	}
	data, err := f.ReadFile(ctx, "/srv/racksail/accounts/shop/index.html", 0)
	if err != nil || string(data) != "hello" {
		t.Errorf("after rename: %q, %v", data, err)
	}
	entries, _ := f.ReadDir(ctx, "/srv/racksail/accounts")
	if len(entries) != 1 || !entries[0].IsDir() {
		t.Errorf("readdir = %+v", entries)
	}
	if err := f.RemoveAll(ctx, "/srv/racksail/accounts/shop"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Stat(ctx, "/srv/racksail/accounts/shop/index.html"); !errors.Is(err, system.ErrNotFound) {
		t.Errorf("after RemoveAll: %v", err)
	}
	if err := f.WriteFile(ctx, "relative", nil, 0o644, system.Owner{}); !errors.Is(err, system.ErrInvalid) {
		t.Errorf("relative path: %v", err)
	}
}

func TestUsers(t *testing.T) {
	ctx := context.Background()
	_, b := newMachine(t)
	u := b.Users
	acct := system.User{Name: "blog", UID: 20001, Home: "/srv/racksail/accounts/blog", SubIDs: &system.IDRange{Start: 1065536, Count: 65536}}
	if err := u.CreateUser(ctx, acct); err != nil {
		t.Fatal(err)
	}
	got, err := u.LookupUser(ctx, "blog")
	if err != nil || got.GID != 20001 || got.Shell != "/usr/sbin/nologin" {
		t.Errorf("lookup = %+v, %v", got, err)
	}
	if g, err := u.LookupGroup(ctx, "blog"); err != nil || g.GID != 20001 {
		t.Errorf("private group = %+v, %v", g, err)
	}
	if err := u.CreateUser(ctx, system.User{Name: "other", UID: 20001}); !errors.Is(err, system.ErrExists) {
		t.Errorf("duplicate uid: %v", err)
	}
	got.Groups = []string{"users"}
	got.Locked = true
	if err := u.UpdateUser(ctx, got); err != nil {
		t.Fatal(err)
	}
	if g, _ := u.LookupGroup(ctx, "users"); len(g.Members) != 1 || g.Members[0] != "blog" {
		t.Errorf("users members = %v", g.Members)
	}
	sys := system.User{Name: "racksail-addon", System: true}
	if err := u.CreateUser(ctx, sys); err != nil {
		t.Fatal(err)
	}
	if su, _ := u.LookupUser(ctx, "racksail-addon"); su.UID < 100 || su.UID > 999 {
		t.Errorf("system uid = %d", su.UID)
	}
	if err := u.DeleteUser(ctx, "blog", false); err != nil {
		t.Fatal(err)
	}
	if _, err := u.LookupGroup(ctx, "blog"); !errors.Is(err, system.ErrNotFound) {
		t.Errorf("private group should go with the user: %v", err)
	}
	if err := u.DeleteUser(ctx, "root", false); err == nil {
		t.Error("deleting root should fail")
	}
}

func TestUnitsAndPackages(t *testing.T) {
	ctx := context.Background()
	m, b := newMachine(t)
	if err := b.Units.Start(ctx, "knot.service"); !errors.Is(err, system.ErrNotFound) {
		t.Errorf("starting a unit of a missing package: %v", err)
	}
	cands, err := b.Packages.Candidates(ctx, "knot")
	if err != nil || len(cands) != 1 || cands[0].DownloadBytes == 0 {
		t.Fatalf("candidates = %+v, %v", cands, err)
	}
	if err := b.Packages.Install(ctx, system.PackageRequest{Name: "knot"}); err != nil {
		t.Fatal(err)
	}
	st, _ := b.Units.Status(ctx, "knot.service")
	if !st.Active() || st.UnitFileState != "enabled" {
		t.Errorf("knot after install = %+v", st)
	}
	if err := b.Packages.Install(ctx, system.PackageRequest{Name: "php8.4-fpm"}); !errors.Is(err, system.ErrNotFound) {
		t.Errorf("a vendor package without its source should fail: %v", err)
	}
	src := system.AptSource{
		Name: "php", URIs: []string{"https://packages.sury.org/php/"}, Suites: []string{"trixie"}, Components: []string{"main"},
		Key: []byte("-----BEGIN PGP PUBLIC KEY BLOCK-----\nx\n-----END PGP PUBLIC KEY BLOCK-----\n"), Packages: []string{"php*"},
	}
	if err := b.Packages.AddSource(ctx, src); err != nil {
		t.Fatal(err)
	}
	if err := b.Packages.Install(ctx, system.PackageRequest{Name: "php8.4-fpm"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := b.Packages.Installed(ctx, "php8.4-fpm", "php8.4-cli", "nginx"); len(got) != 2 {
		t.Errorf("installed = %+v", got)
	}

	unit := []byte("[Unit]\nDescription=Blog\n\n[Service]\nUser=racksail\nExecStart=/usr/bin/sleep infinity\n\n[Install]\nWantedBy=multi-user.target\n")
	if err := b.Units.WriteUnit(ctx, "racksail-site-blog.service", unit); err != nil {
		t.Fatal(err)
	}
	if err := b.Units.Start(ctx, "racksail-site-blog.service"); err != nil {
		t.Fatal(err)
	}
	st, _ = b.Units.Status(ctx, "racksail-site-blog.service")
	if !st.Active() || st.Description != "Blog" || st.UnitFileState != "disabled" {
		t.Errorf("site unit = %+v", st)
	}
	procs, _ := b.Processes.List(ctx, system.ProcessFilter{Unit: "racksail-site-blog.service"})
	if len(procs) != 1 || procs[0].User != "racksail" || procs[0].Command != "sleep" {
		t.Errorf("site process = %+v", procs)
	}
	m.CrashUnit("racksail-site-blog.service")
	if st, _ = b.Units.Status(ctx, "racksail-site-blog.service"); !st.Failed() {
		t.Errorf("after crash = %+v", st)
	}
	if err := b.Units.SetProperties(ctx, "racksail-site-blog.service", false, system.UnitProperty{Name: "MemoryMax", Value: "512M"}); err != nil {
		t.Fatal(err)
	}
	if v, _ := m.Property("racksail-site-blog.service", "MemoryMax"); v != "512M" {
		t.Errorf("MemoryMax = %q", v)
	}
	list, _ := b.Units.List(ctx, "racksail-*")
	if len(list) < 5 {
		t.Errorf("racksail units = %d", len(list))
	}
	if err := b.Packages.Remove(ctx, false, "knot"); err != nil {
		t.Fatal(err)
	}
	if st, _ = b.Units.Status(ctx, "knot.service"); st.LoadState != "not-found" {
		t.Errorf("knot after removal = %+v", st)
	}

	m.OfferUpgrade("sudo", "1.9.17-1")
	up, _ := b.Packages.Upgradable(ctx)
	if len(up) != 1 || up[0].Version != "1.9.17-1" {
		t.Errorf("upgradable = %+v", up)
	}
}

func TestSnapshotsSendReceive(t *testing.T) {
	ctx := context.Background()
	_, b := newMachine(t)
	if err := b.Btrfs.CreateSubvolume(ctx, "/srv/racksail/accounts/blog"); err != nil {
		t.Fatal(err)
	}
	if err := b.Files.WriteFile(ctx, "/srv/racksail/accounts/blog/a.txt", []byte("one"), 0o644, system.Owner{}); err != nil {
		t.Fatal(err)
	}
	if err := b.Btrfs.SetQuota(ctx, "/srv/racksail/accounts/blog", 1<<30); err != nil {
		t.Fatal(err)
	}
	q, _ := b.Btrfs.Quota(ctx, "/srv/racksail/accounts/blog")
	if q.ReferencedBytes != 3 || q.LimitBytes != 1<<30 {
		t.Errorf("quota = %+v", q)
	}
	if err := b.Btrfs.Snapshot(ctx, "/srv/racksail/accounts/blog", "/.snapshots/blog-1", true); err != nil {
		t.Fatal(err)
	}
	if err := b.Files.WriteFile(ctx, "/srv/racksail/accounts/blog/a.txt", []byte("two"), 0o644, system.Owner{}); err != nil {
		t.Fatal(err)
	}
	old, _ := b.Files.ReadFile(ctx, "/.snapshots/blog-1/a.txt", 0)
	if string(old) != "one" {
		t.Errorf("snapshot content = %q", old)
	}

	var stream bytes.Buffer
	if err := b.Btrfs.Send(ctx, "/.snapshots/blog-1", "", &stream); err != nil {
		t.Fatal(err)
	}
	other := New(Options{Hostname: "standby"}).Backend()
	if err := other.Btrfs.Receive(ctx, "/.snapshots", &stream); err != nil {
		t.Fatal(err)
	}
	got, err := other.Files.ReadFile(ctx, "/.snapshots/blog-1/a.txt", 0)
	if err != nil || string(got) != "one" {
		t.Errorf("received content = %q, %v", got, err)
	}

	if err := b.Btrfs.Snapshot(ctx, "/srv/racksail/accounts/blog", "/.snapshots/blog-2", false); err != nil {
		t.Fatal(err)
	}
	if err := b.Btrfs.Send(ctx, "/.snapshots/blog-2", "", &bytes.Buffer{}); err == nil {
		t.Error("sending a writable snapshot should fail")
	}
	if err := b.Btrfs.DeleteSubvolume(ctx, "/srv/racksail/accounts/blog"); err != nil {
		t.Fatal(err)
	}
}

func TestDisksAndPools(t *testing.T) {
	ctx := context.Background()
	_, b := newMachine(t)
	if err := b.Disks.Wipe(ctx, "/dev/nvme0n1"); err == nil {
		t.Error("wiping the system disk must fail")
	}
	if err := b.Disks.Format(ctx, system.FormatSpec{Type: "btrfs", Devices: []string{"/dev/sda", "/dev/sdb"}, Label: "pool", Profiles: system.BtrfsProfiles{Data: "raid1", Metadata: "raid1"}}); err != nil {
		t.Fatal(err)
	}
	if err := b.Files.Mkdir(ctx, "/mnt/racksail/pool", 0o755, system.Owner{}); err != nil {
		t.Fatal(err)
	}
	if err := b.Mounts.Mount(ctx, system.Mount{Source: "/dev/sda", Target: "/mnt/racksail/pool", FSType: "btrfs"}); err != nil {
		t.Fatal(err)
	}
	usage, err := b.Btrfs.Usage(ctx, "/mnt/racksail/pool")
	if err != nil || usage.Data != "raid1" || len(usage.Devices) != 2 {
		t.Errorf("pool usage = %+v, %v", usage, err)
	}
	if err := b.Btrfs.RemoveDevice(ctx, "/mnt/racksail/pool", "/dev/sdb"); err == nil {
		t.Error("removing a device from a two-disk raid1 must fail")
	}
	if err := b.Btrfs.Balance(ctx, "/mnt/racksail/pool", system.BtrfsProfiles{Data: "raid1c3"}); err == nil {
		t.Error("raid1c3 needs three devices")
	}
	if err := b.Btrfs.ScrubStart(ctx, "/mnt/racksail/pool"); err != nil {
		t.Fatal(err)
	}
	report, err := b.Disks.SMART(ctx, "sda")
	if err != nil || !report.Healthy || report.TemperatureC == 0 {
		t.Errorf("smart = %+v, %v", report, err)
	}
	if err := b.Disks.Partition(ctx, "nvme1n1", []system.PartitionSpec{{Number: 1, SizeBytes: 1 << 30, Type: "efi"}, {Number: 2, Type: "linux"}}); err != nil {
		t.Fatal(err)
	}
	disks, _ := b.Disks.List(ctx)
	for _, d := range disks {
		if d.Name == "nvme1n1" && (len(d.Partitions) != 2 || d.Partitions[1].Name != "nvme1n1p2") {
			t.Errorf("partitions = %+v", d.Partitions)
		}
	}
}

func TestFirewallAndNetwork(t *testing.T) {
	ctx := context.Background()
	m, b := newMachine(t)
	if err := b.Firewall.Check(ctx, []byte("table inet filter { }")); err == nil {
		t.Error("a ruleset touching foreign tables must be rejected")
	}
	ruleset := []byte("table inet racksail {\n  set bans {\n    type ipv4_addr\n    flags timeout\n  }\n  chain input {\n    type filter hook input priority 0; policy drop;\n  }\n}\n")
	if err := b.Firewall.Apply(ctx, ruleset); err != nil {
		t.Fatal(err)
	}
	bans := system.SetRef{Family: "inet", Table: "racksail", Set: "bans"}
	if err := b.Firewall.AddElements(ctx, bans, system.SetElement{Value: "198.51.100.7", Timeout: time.Hour}, system.SetElement{Value: "192.0.2.0/24"}); err != nil {
		t.Fatal(err)
	}
	elems, _ := b.Firewall.Elements(ctx, bans)
	if len(elems) != 2 {
		t.Errorf("elements = %+v", elems)
	}
	if err := b.Firewall.AddElements(ctx, system.SetRef{Family: "inet", Table: "racksail", Set: "missing"}, system.SetElement{Value: "192.0.2.1"}); !errors.Is(err, system.ErrNotFound) {
		t.Errorf("unknown set: %v", err)
	}
	if err := b.Network.ApplyConfig(ctx, map[string][]byte{"10-other.network": nil}); err == nil {
		t.Error("files Racksail doesn't own must be refused")
	}
	if err := b.Network.ApplyConfig(ctx, map[string][]byte{"50-racksail-enp1s0.network": []byte("[Match]\nName=enp1s0\n")}); err != nil {
		t.Fatal(err)
	}
	cfg, _ := b.Network.Config(ctx)
	if len(cfg) != 1 {
		t.Errorf("config = %v", cfg)
	}
	if err := b.Network.SetHostname(ctx, "web1"); err != nil {
		t.Fatal(err)
	}
	if facts, _ := b.Facts.Facts(ctx); facts.Hostname != "web1" {
		t.Errorf("hostname fact = %q", facts.Hostname)
	}
	if v, _ := b.Sysctl.Get(ctx, "net.core.default_qdisc"); v != "fq_codel" {
		t.Errorf("sysctl = %q", v)
	}
	if err := b.Sysctl.Set(ctx, "net.core.default_qdisc", "fq"); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range m.Calls() {
		if c.Area == "sysctl" && c.Op == "set" && c.Target == "net.core.default_qdisc" {
			found = true
		}
	}
	if !found {
		t.Error("the sysctl call was not recorded")
	}
}

func TestExecAndFailures(t *testing.T) {
	ctx := context.Background()
	m, b := newMachine(t)
	if _, err := b.Exec.Run(ctx, system.Command{Path: "/usr/bin/knotc", Args: []string{"reload"}}); !errors.Is(err, ErrNotSimulated) {
		t.Errorf("unsimulated program: %v", err)
	}
	m.SetProgram("/usr/bin/knotc", func(ctx context.Context, m *Machine, cmd system.Command) (system.Output, error) {
		if len(cmd.Args) > 0 && cmd.Args[0] == "reload" {
			return system.Output{Stdout: []byte("OK\n")}, nil
		}
		return system.Output{Stderr: []byte("unknown command"), ExitCode: 1}, nil
	})
	out, err := b.Exec.Run(ctx, system.Command{Path: "/usr/bin/knotc", Args: []string{"reload"}})
	if err != nil || strings.TrimSpace(string(out.Stdout)) != "OK" {
		t.Errorf("knotc reload = %q, %v", out.Stdout, err)
	}
	_, err = b.Exec.Run(ctx, system.Command{Path: "/usr/bin/knotc", Args: []string{"bogus"}})
	var exitErr *system.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode != 1 {
		t.Errorf("failing program: %v", err)
	}
	if _, err := b.Exec.Run(ctx, system.Command{Path: "knotc"}); !errors.Is(err, system.ErrInvalid) {
		t.Errorf("relative program path: %v", err)
	}

	boom := errors.New("simulated failure")
	m.FailNext("units.start", "ssh.service", boom)
	if err := b.Units.Start(ctx, "ssh.service"); !errors.Is(err, boom) {
		t.Errorf("injected failure: %v", err)
	}
	if err := b.Units.Start(ctx, "ssh.service"); err != nil {
		t.Errorf("the failure should only happen once: %v", err)
	}
}

func TestCertificates(t *testing.T) {
	ctx := context.Background()
	_, b := newMachine(t)
	chain, key := selfSigned(t, "mail.example.test")
	if err := b.Certs.Install(ctx, "mail", chain, key, system.Owner{Group: "racksail"}); err != nil {
		t.Fatal(err)
	}
	info, err := b.Certs.Get(ctx, "mail")
	if err != nil || len(info.DNSNames) != 1 || info.DNSNames[0] != "mail.example.test" {
		t.Errorf("cert = %+v, %v", info, err)
	}
	keyInfo, _ := b.Files.Stat(ctx, "/etc/racksail/certs/mail/privkey.pem")
	if keyInfo.Perm() != 0o640 || keyInfo.Group != "racksail" {
		t.Errorf("key file = %+v", keyInfo)
	}
	otherChain, _ := selfSigned(t, "other.test")
	if err := b.Certs.Install(ctx, "bad", otherChain, key, system.Owner{}); !errors.Is(err, system.ErrInvalid) {
		t.Errorf("mismatched key: %v", err)
	}
	all, _ := b.Certs.List(ctx)
	if len(all) != 1 {
		t.Errorf("list = %+v", all)
	}
}

func selfSigned(t *testing.T, name string) ([]byte, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: name}, DNSNames: []string{name},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
}
