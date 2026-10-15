package linux

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/armor"
	"golang.org/x/sys/unix"

	"github.com/vilanobeachflorida/racksail/internal/system"
)

func TestFactsHardware(t *testing.T) {
	ctx := context.Background()
	b, _, _, root := newTestBackend(t, nil)
	write(t, root, "/etc/os-release", "ID=debian\nVERSION_ID=\"13\"\n")
	write(t, root, "/proc/meminfo", "MemTotal: 1000 kB\n")
	write(t, root, "/proc/cpuinfo", "processor\t: 0\nvendor_id\t: AuthenticAMD\nmodel name\t: AMD EPYC\nphysical id\t: 0\ncore id\t: 0\nflags\t: fpu sse2 svm aes avx2\n\n"+
		"processor\t: 1\nvendor_id\t: AuthenticAMD\nmodel name\t: AMD EPYC\nphysical id\t: 1\ncore id\t: 0\nflags\t: fpu sse2 svm aes avx2\n")
	write(t, root, "/sys/class/tpm/tpm0/tpm_version_major", "2\n")
	// Disks: an NVMe drive, a SATA disk behind ata, a loop device and a
	// partition-less zram device that aren't disks.
	write(t, root, "/sys/devices/pci0000:00/0000:00:01.0/nvme/nvme0/nvme0n1/size", "1000215216\n")
	write(t, root, "/sys/devices/pci0000:00/0000:00:01.0/nvme/nvme0/nvme0n1/queue/rotational", "0\n")
	write(t, root, "/sys/devices/pci0000:00/0000:00:01.0/nvme/nvme0/nvme0n1/removable", "0\n")
	write(t, root, "/sys/devices/pci0000:00/0000:00:01.0/nvme/nvme0/nvme0n1/device/model", "Samsung SSD 990 PRO\n")
	write(t, root, "/sys/devices/pci0000:00/0000:00:01.0/nvme/nvme0/nvme0n1/device/serial", "S6Z1NJ0W123\n")
	write(t, root, "/sys/devices/pci0000:00/0000:00:17.0/ata1/host0/target0:0:0/0:0:0:0/block/sda/size", "7814037168\n")
	write(t, root, "/sys/devices/pci0000:00/0000:00:17.0/ata1/host0/target0:0:0/0:0:0:0/block/sda/queue/rotational", "1\n")
	write(t, root, "/sys/devices/virtual/block/loop0/size", "100\n")
	os.MkdirAll(filepath.Join(root, "sys/block"), 0o755)
	os.Symlink("../devices/pci0000:00/0000:00:01.0/nvme/nvme0/nvme0n1", filepath.Join(root, "sys/block/nvme0n1"))
	os.Symlink("../devices/pci0000:00/0000:00:17.0/ata1/host0/target0:0:0/0:0:0:0/block/sda", filepath.Join(root, "sys/block/sda"))
	os.Symlink("../devices/virtual/block/loop0", filepath.Join(root, "sys/block/loop0"))
	// NICs: one with a device and a driver, and a bridge without a device.
	write(t, root, "/sys/devices/pci0000:00/0000:00:03.0/net/enp3s0/address", "52:54:00:aa:bb:cc\n")
	write(t, root, "/sys/devices/pci0000:00/0000:00:03.0/net/enp3s0/operstate", "up\n")
	write(t, root, "/sys/devices/pci0000:00/0000:00:03.0/net/enp3s0/speed", "10000\n")
	os.MkdirAll(filepath.Join(root, "sys/devices/pci0000:00/0000:00:03.0/net/enp3s0/device"), 0o755)
	os.MkdirAll(filepath.Join(root, "sys/bus/pci/drivers/ixgbe"), 0o755)
	os.Symlink("../../../../../bus/pci/drivers/ixgbe", filepath.Join(root, "sys/devices/pci0000:00/0000:00:03.0/net/enp3s0/device/driver"))
	write(t, root, "/sys/devices/virtual/net/br0/address", "aa:aa:aa:aa:aa:aa\n")
	os.MkdirAll(filepath.Join(root, "sys/class/net"), 0o755)
	os.Symlink("../../devices/pci0000:00/0000:00:03.0/net/enp3s0", filepath.Join(root, "sys/class/net/enp3s0"))
	os.Symlink("../../devices/virtual/net/br0", filepath.Join(root, "sys/class/net/br0"))

	facts, err := b.Facts.Facts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cpu := facts.CPU
	if cpu.Vendor != "AuthenticAMD" || cpu.Sockets != 2 || !cpu.Virtualization || cpu.VirtualizationType != "svm" || !cpu.HasFlag("avx2") {
		t.Errorf("cpu = %+v", cpu)
	}
	if !facts.TPM || facts.TPMVersion != "2" {
		t.Errorf("tpm = %v %q", facts.TPM, facts.TPMVersion)
	}
	if len(facts.Disks) != 2 {
		t.Fatalf("disks = %+v", facts.Disks)
	}
	nvme, sda := facts.Disks[0], facts.Disks[1]
	if nvme.Name != "nvme0n1" || nvme.Transport != "nvme" || nvme.Model != "Samsung SSD 990 PRO" || nvme.SizeBytes != 1000215216*512 || nvme.Rotational {
		t.Errorf("nvme = %+v", nvme)
	}
	if sda.Transport != "sata" || !sda.Rotational {
		t.Errorf("sda = %+v", sda)
	}
	if len(facts.NICs) != 1 || facts.NICs[0] != (system.NICFact{Name: "enp3s0", MAC: "52:54:00:aa:bb:cc", Driver: "ixgbe", SpeedMbps: 10000, Up: true}) {
		t.Errorf("nics = %+v", facts.NICs)
	}
	arm := parseCPUInfo([]byte("processor\t: 0\nFeatures\t: fp asimd aes\nCPU implementer\t: 0xc0\n\nprocessor\t: 1\nFeatures\t: fp asimd aes\n"))
	if arm.Vendor != "Ampere" || arm.Threads != 2 || arm.Sockets != 1 || !slices.Equal(arm.Flags, []string{"fp", "asimd", "aes"}) || arm.Virtualization {
		t.Errorf("arm64 cpu = %+v", arm)
	}
}

func TestJournalAdapter(t *testing.T) {
	ctx := context.Background()
	out := `{"__REALTIME_TIMESTAMP":"1760000000000000","__CURSOR":"s=a;i=1","_SYSTEMD_UNIT":"init.scope","UNIT":"knot.service","SYSLOG_IDENTIFIER":"systemd","_PID":"1","PRIORITY":"6","MESSAGE":"Started knot.service."}
{"__REALTIME_TIMESTAMP":"1760000001000000","__CURSOR":"s=a;i=2","_SYSTEMD_UNIT":"knot.service","SYSLOG_IDENTIFIER":"knotd","_PID":"321","PRIORITY":"3","MESSAGE":[104,105,255]}
{"__REALTIME_TIMESTAMP":"17600000020000`
	b, runner, _, _ := newTestBackend(t, func(cmd system.Command) (system.Output, error) {
		return system.Output{Stdout: []byte(out)}, nil
	})
	warning := system.PriorityWarning
	page, err := b.Journal.Read(ctx, system.JournalQuery{
		Units: []string{"knot.service"}, Priority: &warning, Grep: "zone (a)", Since: time.Unix(1759999999, 500000000), Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Entries) != 2 || page.Cursor != "s=a;i=2" {
		t.Fatalf("page = %+v", page)
	}
	if e := page.Entries[0]; e.Unit != "knot.service" || e.PID != 1 || e.Priority != 6 || e.Time.Unix() != 1760000000 {
		t.Errorf("systemd's own message about the unit = %+v", e)
	}
	if e := page.Entries[1]; e.Message != "hi�" || e.Identifier != "knotd" || e.Priority != 3 {
		t.Errorf("binary message = %+v", e)
	}
	want := "/usr/bin/journalctl --output=json --no-pager --quiet --unit=knot.service --since=@1759999999.500000 --priority=4 --grep=zone \\(a\\) --case-sensitive=false --lines=10"
	if got := runner.argv(); len(got) != 1 || got[0] != want {
		t.Errorf("command = %v\nwant %s", got, want)
	}
	if _, err := b.Journal.Read(ctx, system.JournalQuery{Units: []string{"--evil"}}); !errors.Is(err, system.ErrInvalid) {
		t.Errorf("a bad unit: %v", err)
	}
	if args := journalArgs(system.JournalQuery{After: "s=a;i=2", Newest: true, Namespace: "blog"}); !slices.Contains(args, "--after-cursor=s=a;i=2") ||
		!slices.Contains(args, "--namespace=blog") || !slices.Contains(args, "--reverse") {
		t.Errorf("paging args = %v", args)
	}
	none, _, _, _ := newTestBackend(t, func(cmd system.Command) (system.Output, error) {
		return system.Output{ExitCode: 1}, &system.ExitError{Program: cmd.Path, ExitCode: 1}
	})
	if page, err := none.Journal.Read(ctx, system.JournalQuery{Grep: "nothing"}); err != nil || len(page.Entries) != 0 {
		t.Errorf("no match = %+v, %v", page, err)
	}
}

func TestMetricsAdapter(t *testing.T) {
	ctx := context.Background()
	b, _, sd, root := newTestBackend(t, nil)
	write(t, root, "/proc/uptime", "3600.50 7000.00\n")
	write(t, root, "/proc/loadavg", "0.50 0.40 0.30 1/200 4242\n")
	write(t, root, "/proc/stat", "cpu  1000 10 500 80000 200 5 15 20 0 0\ncpu0 500 5 250 40000 100 2 7 10 0 0\ncpu1 500 5 250 40000 100 3 8 10 0 0\nbtime 1760000000\n")
	write(t, root, "/proc/meminfo", "MemTotal: 8000000 kB\nMemFree: 1000000 kB\nMemAvailable: 5000000 kB\nBuffers: 100000 kB\nCached: 2000000 kB\nSwapTotal: 1000000 kB\nSwapFree: 900000 kB\n")
	write(t, root, "/proc/pressure/cpu", "some avg10=1.50 avg60=1.00 avg300=0.50 total=123\nfull avg10=0.00 avg60=0.00 avg300=0.00 total=0\n")
	write(t, root, "/proc/pressure/io", "some avg10=4.00 avg60=3.00 avg300=2.00 total=9\nfull avg10=2.00 avg60=1.00 avg300=0.50 total=5\n")
	write(t, root, "/proc/diskstats", "   8       0 sda 100 0 2000 50 300 0 4000 80 0 120 130 0 0 0 0\n   8       1 sda1 1 0 8 0 1 0 8 0 0 1 1 0 0 0 0\n   7       0 loop0 1 0 2 0 0 0 0 0 0 0 0 0 0 0 0\n")
	write(t, root, "/proc/net/dev", "Inter-|   Receive                                                |  Transmit\n face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed\n    lo: 100 1 0 0 0 0 0 0 100 1 0 0 0 0 0 0\n  eth0: 5000 50 1 2 0 0 0 0 9000 90 3 4 0 0 0 0\n")
	write(t, root, "/proc/self/mountinfo", "")
	write(t, root, "/sys/block/sda/size", "100\n")
	write(t, root, "/sys/block/loop0/size", "100\n")
	m, err := b.Metrics.System(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if m.Uptime != 3600500*time.Millisecond || m.CPUs != 2 || m.Load.One != 0.5 || m.CPU.User != 10*time.Second || m.CPU.Idle != 800*time.Second || m.CPU.Steal != 200*time.Millisecond {
		t.Errorf("cpu = %+v %v %v", m.CPU, m.Uptime, m.Load)
	}
	if m.Memory.TotalBytes != 8000000*1024 || m.Memory.UsedBytes() != 3000000*1024 || m.Memory.SwapFreeBytes != 900000*1024 {
		t.Errorf("memory = %+v", m.Memory)
	}
	if m.Pressure.CPU.Some10 != 1.5 || m.Pressure.IO.Full60 != 1 {
		t.Errorf("pressure = %+v", m.Pressure)
	}
	if len(m.Disks) != 1 || m.Disks[0] != (system.DiskIO{Name: "sda", ReadOps: 100, ReadBytes: 2000 * 512, WriteOps: 300, WriteBytes: 4000 * 512, BusyTime: 120 * time.Millisecond}) {
		t.Errorf("disks = %+v", m.Disks)
	}
	if len(m.Networks) != 1 || m.Networks[0].RxBytes != 5000 || m.Networks[0].TxDropped != 4 {
		t.Errorf("networks = %+v", m.Networks)
	}

	cg := "/racksail.slice/racksail-accounts.slice/racksail-accounts-blog.slice/racksail-site-blog.service"
	sd.props["racksail-site-blog.service"] = map[string]any{"LoadState": "loaded", "ActiveState": "active", "ControlGroup": cg}
	dir := "/sys/fs/cgroup" + cg
	write(t, root, dir+"/cpu.stat", "usage_usec 2500000\nuser_usec 2000000\n")
	write(t, root, dir+"/memory.current", "41943040\n")
	write(t, root, dir+"/memory.peak", "52428800\n")
	write(t, root, dir+"/pids.current", "7\n")
	write(t, root, dir+"/io.stat", "8:0 rbytes=1000 wbytes=2000 rios=1 wios=2\n259:0 rbytes=10 wbytes=20 rios=1 wios=1\n")
	write(t, root, dir+"/memory.pressure", "some avg10=0.25 avg60=0 avg300=0 total=1\n")
	units, err := b.Metrics.Units(ctx, "racksail-site-blog.service", "stopped.service")
	if err != nil {
		t.Fatal(err)
	}
	if len(units) != 1 {
		t.Fatalf("units = %+v", units)
	}
	u := units[0]
	if u.CPUUsage != 2500*time.Millisecond || u.MemoryBytes != 40<<20 || u.MemoryPeakBytes != 50<<20 || u.Tasks != 7 || u.IOReadBytes != 1010 || u.IOWriteBytes != 2020 || u.Pressure.Memory.Some10 != 0.25 {
		t.Errorf("unit = %+v", u)
	}
}

func TestSMARTParsing(t *testing.T) {
	nvme := []byte(`{"device":{"protocol":"NVMe"},"model_name":"Samsung SSD 990 PRO","serial_number":"S6Z1","firmware_version":"4B2QJXD7",
"smart_status":{"passed":true},"temperature":{"current":41},"power_on_time":{"hours":1200},"power_cycle_count":33,
"nvme_smart_health_information_log":{"critical_warning":0,"available_spare":100,"percentage_used":3,"data_units_read":1000,"data_units_written":2000,"unsafe_shutdowns":5,"media_errors":0},
"nvme_self_test_log":{"current_self_test_operation":{"value":0},"table":[{"self_test_code":{"string":"Extended"},"self_test_result":{"value":0,"string":"Completed without error"},"power_on_hours":1100}]}}`)
	r, err := parseSmartctl(nvme)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Supported || !r.Healthy || r.Protocol != "NVMe" || r.PercentageUsed != 3 || r.DataWrittenBytes != 2000*512000 || r.PowerCycles != 33 ||
		r.SelfTest == nil || *r.SelfTest != (system.SMARTSelfTest{Status: "passed", Type: "long", LifetimeHours: 1100}) {
		t.Errorf("nvme = %+v %+v", r, r.SelfTest)
	}
	running := []byte(`{"device":{"protocol":"NVMe"},"nvme_smart_health_information_log":{},"nvme_self_test_log":{"current_self_test_operation":{"value":1},"current_self_test_completion_percent":30}}`)
	if r, _ := parseSmartctl(running); r.SelfTest == nil || r.SelfTest.Status != "running" || r.SelfTest.RemainingPercent != 70 || r.SelfTest.Type != "short" {
		t.Errorf("running nvme test = %+v", r.SelfTest)
	}
	ata := []byte(`{"device":{"protocol":"ATA"},"smart_support":{"available":true,"enabled":true},"smart_status":{"passed":false},
"ata_smart_attributes":{"table":[{"id":5,"name":"Reallocated_Sector_Ct","value":90,"worst":90,"thresh":10,"when_failed":"","raw":{"value":120}},
{"id":197,"name":"Current_Pending_Sector","value":100,"worst":100,"thresh":0,"when_failed":"now","raw":{"value":8}}]},
"ata_smart_data":{"self_test":{"status":{"value":0,"passed":true}}},
"ata_smart_self_test_log":{"standard":{"table":[{"type":{"string":"Short offline"},"status":{"passed":false,"string":"Completed: read failure"},"lifetime_hours":30000}]}}}`)
	r, err = parseSmartctl(ata)
	if err != nil {
		t.Fatal(err)
	}
	if r.Healthy || r.ReallocatedSectors != 120 || r.PendingSectors != 8 || !r.Attributes[1].Failing || r.SelfTest.Status != "failed" || r.SelfTest.Type != "short" {
		t.Errorf("ata = %+v %+v", r, r.SelfTest)
	}
	inProgress := []byte(`{"smart_support":{"available":true,"enabled":true},"ata_smart_data":{"self_test":{"status":{"value":249,"remaining_percent":90}}}}`)
	if r, _ := parseSmartctl(inProgress); r.SelfTest == nil || r.SelfTest.Status != "running" || r.SelfTest.RemainingPercent != 90 {
		t.Errorf("ata test in progress = %+v", r.SelfTest)
	}
}

func TestTreeStaysInside(t *testing.T) {
	ctx := context.Background()
	b, _, _, root := newTestBackend(t, nil)
	me := system.Owner{User: "tester", Group: "testers"}
	write(t, root, "/srv/acct/site/index.html", "hello")
	write(t, root, "/etc/shadow-copy", "secret")
	os.Symlink("../../../etc/shadow-copy", filepath.Join(root, "srv/acct/site/escape"))
	os.Symlink("index.html", filepath.Join(root, "srv/acct/site/inside"))
	tr, err := b.Files.OpenTree(ctx, "/srv/acct")
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	// Links are never followed, not even ones that stay inside.
	if _, err := tr.Open("site/inside"); !errors.Is(err, system.ErrInvalid) {
		t.Errorf("a link inside the tree: %v", err)
	}
	if _, err := tr.Open("site/escape"); !errors.Is(err, system.ErrInvalid) {
		t.Errorf("a link out of the tree: %v", err)
	}
	if _, err := tr.Open("../etc/shadow-copy"); !errors.Is(err, system.ErrInvalid) {
		t.Errorf("a name out of the tree: %v", err)
	}
	if info, err := tr.Lstat("site/escape"); err != nil || !info.IsSymlink() || info.Target != "../../../etc/shadow-copy" {
		t.Errorf("lstat of the link = %+v, %v", info, err)
	}
	if err := tr.Mkdir("site/uploads", 0o750, me); err != nil {
		t.Fatal(err)
	}
	if n, err := tr.Create("site/uploads/a.txt", strings.NewReader("data"), 0o640, me); err != nil || n != 4 {
		t.Fatalf("create = %d, %v", n, err)
	}
	// The test root maps tester to the user running the tests.
	if info, _ := tr.Lstat("site/uploads/a.txt"); info.Perm() != 0o640 || info.Size != 4 || info.UID != os.Getuid() {
		t.Errorf("created file = %+v", info)
	}
	if _, err := tr.Create("site/uploads", strings.NewReader("x"), 0o644, me); !errors.Is(err, system.ErrExists) {
		t.Errorf("replacing a directory: %v", err)
	}
	if err := tr.Symlink("../../etc", "site/out", me); !errors.Is(err, system.ErrInvalid) {
		t.Errorf("a link that leaves the tree: %v", err)
	}
	if err := tr.Symlink("../site/index.html", "site/uploads/back", me); err != nil {
		t.Errorf("a relative link inside: %v", err)
	}
	entries, err := tr.ReadDir("site")
	if err != nil || len(entries) != 4 {
		t.Errorf("readdir = %+v, %v", entries, err)
	}
	rr, err := tr.OpenRandom("site/index.html")
	if err != nil || rr.Size() != 5 {
		t.Errorf("random reader = %v, %v", rr, err)
	} else {
		rr.Close()
	}
	if err := tr.Remove("site/uploads"); !errors.Is(err, system.ErrNotEmpty) {
		t.Errorf("removing a full directory: %v", err)
	}
	leftovers, _ := filepath.Glob(filepath.Join(root, "srv/acct/site/uploads", system.TreeTempPrefix+"*"))
	if len(leftovers) != 0 {
		t.Errorf("temporary files left: %v", leftovers)
	}
}

func TestBeneathNeverFollowsLinks(t *testing.T) {
	ctx := context.Background()
	b, _, _, root := newTestBackend(t, nil)
	me := system.Owner{User: "tester", Group: "testers"}
	write(t, root, "/srv/racksail/accounts/a/www/index.html", "a")
	write(t, root, "/srv/racksail/accounts/b/secret.txt", "b's")
	write(t, root, "/etc/outside/secret.txt", "root's")
	os.Symlink(filepath.Join(root, "etc/outside"), filepath.Join(root, "srv/racksail/accounts/a/out"))
	os.Symlink("../b", filepath.Join(root, "srv/racksail/accounts/a/neighbour"))
	os.Symlink("www/index.html", filepath.Join(root, "srv/racksail/accounts/a/home.html"))
	view := b.Files.Beneath("/srv/racksail")

	// Nothing reaches past a link, wherever it points.
	for _, p := range []string{"/srv/racksail/accounts/a/out/secret.txt", "/srv/racksail/accounts/a/neighbour/secret.txt"} {
		if _, err := view.ReadFile(ctx, p, 0); !errors.Is(err, system.ErrInvalid) {
			t.Errorf("reading %s: %v", p, err)
		}
		if _, err := view.Stat(ctx, p); !errors.Is(err, system.ErrInvalid) {
			t.Errorf("stat of %s: %v", p, err)
		}
		if err := view.WriteFile(ctx, p, []byte("x"), 0o644, me); !errors.Is(err, system.ErrInvalid) {
			t.Errorf("writing %s: %v", p, err)
		}
		if _, err := view.ReadDir(ctx, path.Dir(p)); !errors.Is(err, system.ErrInvalid) {
			t.Errorf("listing %s: %v", path.Dir(p), err)
		}
		if _, err := view.OpenTree(ctx, path.Dir(p)); !errors.Is(err, system.ErrInvalid) {
			t.Errorf("a tree at %s: %v", path.Dir(p), err)
		}
	}
	if _, err := view.ReadFile(ctx, "/srv/racksail/accounts/a/home.html", 0); !errors.Is(err, system.ErrInvalid) {
		t.Errorf("reading a link itself: %v", err)
	}
	if _, err := view.ReadFile(ctx, "/etc/outside/secret.txt", 0); !errors.Is(err, system.ErrInvalid) {
		t.Errorf("a path outside the view: %v", err)
	}
	if data, _ := os.ReadFile(filepath.Join(root, "etc/outside/secret.txt")); string(data) != "root's" {
		t.Errorf("the file behind the link changed: %q", data)
	}
	if info, err := view.Stat(ctx, "/srv/racksail/accounts/a/out"); err != nil || !info.IsSymlink() || info.Owner != "root" {
		t.Errorf("stat of the link itself = %+v, %v", info, err)
	}

	// Everything else works as through the plain files.
	if data, err := view.ReadFile(ctx, "/srv/racksail/accounts/a/www/index.html", 0); err != nil || string(data) != "a" {
		t.Errorf("read = %q, %v", data, err)
	}
	if _, err := view.ReadFile(ctx, "/srv/racksail/accounts/a/www/index.html", 0); err != nil {
		t.Fatal(err)
	}
	if err := view.Mkdir(ctx, "/srv/racksail/accounts/a/uploads", fs.ModeSticky|0o770, me); err != nil {
		t.Fatal(err)
	}
	if info, err := view.Stat(ctx, "/srv/racksail/accounts/a/uploads"); err != nil || !info.IsDir() || info.Perm() != 0o770 || info.Mode&fs.ModeSticky == 0 {
		t.Errorf("mkdir = %+v, %v", info, err)
	}
	if err := view.WriteFile(ctx, "/srv/racksail/accounts/a/uploads/f.txt", []byte("data"), 0o640, me); err != nil {
		t.Fatal(err)
	}
	if info, _ := view.Stat(ctx, "/srv/racksail/accounts/a/uploads/f.txt"); info.Perm() != 0o640 || info.Size != 4 || info.Owner != "root" {
		t.Errorf("written = %+v", info)
	}
	if err := view.Chmod(ctx, "/srv/racksail/accounts/a/uploads/f.txt", 0o600); err != nil {
		t.Fatal(err)
	}
	if err := view.Chmod(ctx, "/srv/racksail/accounts/a/out", 0o600); !errors.Is(err, system.ErrInvalid) {
		t.Errorf("chmod of a link: %v", err)
	}
	if err := view.Chown(ctx, "/srv/racksail/accounts/a/uploads/f.txt", me); err != nil {
		t.Fatal(err)
	}
	// A write replaces a link rather than writing where it points.
	if err := view.WriteFile(ctx, "/srv/racksail/accounts/a/home.html", []byte("own"), 0o644, me); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Lstat(filepath.Join(root, "srv/racksail/accounts/a/home.html")); fi.Mode()&fs.ModeSymlink != 0 {
		t.Error("the link should have been replaced")
	}
	if data, _ := os.ReadFile(filepath.Join(root, "srv/racksail/accounts/a/www/index.html")); string(data) != "a" {
		t.Errorf("the link's target changed: %q", data)
	}
	if err := view.Rename(ctx, "/srv/racksail/accounts/a/uploads/f.txt", "/srv/racksail/accounts/a/uploads/g.txt"); err != nil {
		t.Fatal(err)
	}
	if err := view.Symlink(ctx, "g.txt", "/srv/racksail/accounts/a/uploads/h.txt"); err != nil {
		t.Fatal(err)
	}
	entries, err := view.ReadDir(ctx, "/srv/racksail/accounts/a/uploads")
	if err != nil || len(entries) != 2 || entries[0].Path != "/srv/racksail/accounts/a/uploads/g.txt" && entries[1].Path != "/srv/racksail/accounts/a/uploads/g.txt" {
		t.Errorf("readdir = %+v, %v", entries, err)
	}
	if err := view.Remove(ctx, "/srv/racksail/accounts/a/uploads"); !errors.Is(err, system.ErrNotEmpty) {
		t.Errorf("removing a full directory: %v", err)
	}
	// Removing a tree removes links in it, not what they point at.
	os.Symlink(filepath.Join(root, "etc/outside"), filepath.Join(root, "srv/racksail/accounts/a/uploads/out"))
	if err := view.RemoveAll(ctx, "/srv/racksail/accounts/a/uploads"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "etc/outside/secret.txt")); err != nil {
		t.Errorf("removing a tree followed a link: %v", err)
	}
	if _, err := view.Stat(ctx, "/srv/racksail/accounts/a/uploads"); !errors.Is(err, system.ErrNotFound) {
		t.Errorf("after removing: %v", err)
	}

	// A FIFO an account planted where a file was expected is refused at
	// once instead of holding the agent.
	if err := unix.Mkfifo(filepath.Join(root, "srv/racksail/accounts/a/pipe"), 0o644); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := view.ReadFile(ctx, "/srv/racksail/accounts/a/pipe", 0)
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, system.ErrInvalid) {
			t.Errorf("reading a FIFO: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("reading a FIFO blocked")
	}

	// A nested view still resolves from the outer root.
	inner := view.Beneath("/srv/racksail/accounts/a")
	if _, err := inner.ReadFile(ctx, "/srv/racksail/accounts/b/secret.txt", 0); !errors.Is(err, system.ErrInvalid) {
		t.Errorf("a path outside the nested view: %v", err)
	}
	if _, err := inner.ReadFile(ctx, "/srv/racksail/accounts/a/out/secret.txt", 0); !errors.Is(err, system.ErrInvalid) {
		t.Errorf("a link in the nested view: %v", err)
	}
}

func TestArchivesOnATree(t *testing.T) {
	ctx := context.Background()
	b, _, _, root := newTestBackend(t, nil)
	me := system.Owner{User: "tester"}
	write(t, root, "/srv/acct/site/index.html", "<h1>hi</h1>")
	write(t, root, "/srv/acct/site/css/app.css", "body{}")
	write(t, root, "/srv/acct/site/.git/HEAD", "ref")
	os.Symlink("index.html", filepath.Join(root, "srv/acct/site/home.html"))
	tr, err := b.Files.OpenTree(ctx, "/srv/acct")
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	for _, format := range []system.ArchiveFormat{system.FormatTarGz, system.FormatTarZst, system.FormatZip, system.FormatTar} {
		name := "site." + string(format)
		stats, err := system.Pack(ctx, tr, system.PackSpec{Source: "site", Dest: name, Format: format, Exclude: []string{".git"}, Owner: me})
		if err != nil {
			t.Fatalf("%s: %v", format, err)
		}
		if stats.Files != 2 || stats.Links != 1 || stats.Dirs != 1 {
			t.Errorf("%s pack stats = %+v", format, stats)
		}
		dest := "restore-" + strings.ReplaceAll(string(format), ".", "-")
		stats, err = system.Unpack(ctx, tr, system.UnpackSpec{Archive: name, Dest: dest, Format: format, Owner: me})
		if err != nil {
			t.Fatalf("%s: %v", format, err)
		}
		if stats.Files != 2 || stats.Links != 1 {
			t.Errorf("%s unpack stats = %+v", format, stats)
		}
		data, err := os.ReadFile(filepath.Join(root, "srv/acct", dest, "css/app.css"))
		if err != nil || string(data) != "body{}" {
			t.Errorf("%s: unpacked css = %q, %v", format, data, err)
		}
		if target, _ := os.Readlink(filepath.Join(root, "srv/acct", dest, "home.html")); target != "index.html" {
			t.Errorf("%s: link = %q", format, target)
		}
		if _, err := os.Stat(filepath.Join(root, "srv/acct", dest, ".git")); err == nil {
			t.Errorf("%s: excluded files were packed", format)
		}
		if _, err := system.Unpack(ctx, tr, system.UnpackSpec{Archive: name, Dest: dest, Format: format, Owner: me}); !errors.Is(err, system.ErrNotEmpty) {
			t.Errorf("%s: unpacking over files without merging: %v", format, err)
		}
		if _, err := system.Unpack(ctx, tr, system.UnpackSpec{Archive: name, Dest: dest, Format: format, Owner: me, Merge: true}); err != nil {
			t.Errorf("%s: merging: %v", format, err)
		}
	}

	// A zip bomb in miniature and an archive that climbs out.
	var bomb bytes.Buffer
	zw := zip.NewWriter(&bomb)
	w, _ := zw.Create("big.bin")
	w.Write(bytes.Repeat([]byte{0}, 1<<20))
	climb, _ := zw.Create("../outside.txt")
	climb.Write([]byte("x"))
	zw.Close()
	write(t, root, "/srv/acct/bomb.zip", bomb.String())
	if _, err := system.Unpack(ctx, tr, system.UnpackSpec{Archive: "bomb.zip", Dest: "bomb", Format: system.FormatZip, Owner: me, MaxBytes: 1000}); !errors.Is(err, system.ErrTooLarge) {
		t.Errorf("size limit: %v", err)
	}
	if _, err := system.Unpack(ctx, tr, system.UnpackSpec{Archive: "bomb.zip", Dest: "bomb2", Format: system.FormatZip, Owner: me}); !errors.Is(err, system.ErrInvalid) {
		t.Errorf("climbing out: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "srv/outside.txt")); err == nil {
		t.Error("an entry escaped the destination")
	}
}

func signingKey(t *testing.T) (*openpgp.Entity, []byte) {
	t.Helper()
	e, err := openpgp.NewEntity("Vendor", "", "release@vendor.test", nil)
	if err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	w, _ := armor.Encode(&b, openpgp.PublicKeyType, nil)
	e.Serialize(w)
	w.Close()
	return e, b.Bytes()
}

func TestInstallSignedArchive(t *testing.T) {
	ctx := context.Background()
	archive := tarGz(t, map[string]string{"node-v22/bin/node": "ELF"}, nil)
	sum := sha256.Sum256(archive)
	sums := []byte(hex.EncodeToString(sum[:]) + "  node-v22.tar.gz\n")
	key, public := signingKey(t)
	var sig bytes.Buffer
	if err := openpgp.ArmoredDetachSign(&sig, key, bytes.NewReader(sums), nil); err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{"/node-v22.tar.gz": archive, "/SHASUMS256.txt": sums, "/SHASUMS256.txt.asc": sig.Bytes()}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, ok := files[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(data)
	}))
	defer srv.Close()
	root := testRoot(t)
	b := NewWith(Options{Root: root, Runner: &fakeRunner{}, Systemd: &fakeSystemd{}, HTTP: srv.Client()})
	a := system.Archive{
		URL: srv.URL + "/node-v22.tar.gz", Size: int64(len(archive)), Format: "tar.gz", StripComponents: 1,
		Dest:      "/opt/racksail/runtimes/node/22.0.0",
		Signature: &system.ArchiveSignature{Key: "nodejs", URL: srv.URL + "/SHASUMS256.txt.asc", Checksums: srv.URL + "/SHASUMS256.txt", PublicKey: public},
	}
	if err := b.Packages.InstallArchive(ctx, a); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(root, "opt/racksail/runtimes/node/22.0.0/bin/node")); err != nil || string(data) != "ELF" {
		t.Errorf("unpacked = %q, %v", data, err)
	}
	files["/SHASUMS256.txt"] = []byte(strings.Repeat("0", 64) + "  node-v22.tar.gz\n")
	tampered := a
	tampered.Dest = "/opt/racksail/runtimes/node/22.0.1"
	if err := b.Packages.InstallArchive(ctx, tampered); !errors.Is(err, system.ErrInvalid) {
		t.Errorf("a checksums list the signature doesn't cover: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "opt/racksail/runtimes/node/22.0.1")); err == nil {
		t.Error("a rejected archive left its directory behind")
	}
}

func TestFirewallKeepsBans(t *testing.T) {
	ctx := context.Background()
	b, runner, _, _ := newTestBackend(t, func(cmd system.Command) (system.Output, error) {
		args := strings.Join(cmd.Args, " ")
		switch {
		case args == "--json list tables":
			return system.Output{Stdout: []byte(`{"nftables":[{"table":{"family":"inet","name":"racksail"}}]}`)}, nil
		case args == "--json list sets":
			return system.Output{Stdout: []byte(`{"nftables":[{"set":{"family":"inet","table":"racksail","name":"ban_ssh_v4","flags":["timeout"]}},` +
				`{"set":{"family":"inet","table":"racksail","name":"rate_tcp_22_v4","flags":["dynamic","timeout"]}},` +
				`{"set":{"family":"inet","table":"racksail","name":"ban_old_v4","flags":["timeout"]}}]}`)}, nil
		case args == "--json list set inet racksail ban_ssh_v4":
			return system.Output{Stdout: []byte(`{"nftables":[{"set":{"elem":[{"elem":{"val":"192.0.2.9","timeout":3600,"expires":1200}}]}}]}`)}, nil
		}
		return system.Output{}, nil
	})
	ruleset, err := system.FirewallPolicy{Bans: []system.BanSet{{Name: "ssh", Timeout: time.Hour}}}.Render()
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Firewall.Apply(ctx, ruleset); err != nil {
		t.Fatal(err)
	}
	var script string
	for _, c := range runner.calls {
		if c.Path == progNft && slices.Equal(c.Args, []string{"--file", "-"}) {
			script = string(c.Stdin)
		}
	}
	if !strings.HasPrefix(script, "delete table inet racksail\n") || !strings.HasSuffix(script, "add element inet racksail ban_ssh_v4 { 192.0.2.9 timeout 1200s }\n") {
		t.Errorf("script = %s", script)
	}
	if strings.Contains(script, "ban_old_v4 {") || strings.Contains(script, "add element inet racksail rate_") {
		t.Errorf("sets the new ruleset drops, and rate meters, are not carried over:\n%s", script)
	}
}

func TestUpgradesAndHolds(t *testing.T) {
	ctx := context.Background()
	b, runner, _, _ := newTestBackend(t, func(cmd system.Command) (system.Output, error) {
		if cmd.Path == progAptMark && len(cmd.Args) == 1 && cmd.Args[0] == "showhold" {
			return system.Output{Stdout: []byte("mariadb-server\npostgresql-17\n")}, nil
		}
		return system.Output{}, nil
	})
	if err := b.Packages.Upgrade(ctx, true); err != nil {
		t.Fatal(err)
	}
	if err := b.Packages.SetHold(ctx, true, "mariadb-server"); err != nil {
		t.Fatal(err)
	}
	if err := b.Packages.SetHold(ctx, false, "--bad"); !errors.Is(err, system.ErrInvalid) {
		t.Errorf("a bad name: %v", err)
	}
	held, err := b.Packages.Held(ctx)
	if err != nil || !slices.Equal(held, []string{"mariadb-server", "postgresql-17"}) {
		t.Errorf("held = %v, %v", held, err)
	}
	want := []string{
		"/usr/bin/apt-get full-upgrade -y -q -o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold",
		"/usr/bin/apt-mark hold -- mariadb-server",
		"/usr/bin/apt-mark showhold",
	}
	if got := runner.argv(); !slices.Equal(got, want) {
		t.Errorf("commands = %v", got)
	}
}

func TestUpgradablePreviewsTheRightUpgrade(t *testing.T) {
	ctx := context.Background()
	b, runner, _, _ := newTestBackend(t, func(cmd system.Command) (system.Output, error) {
		return system.Output{Stdout: []byte("Inst sudo [1.9.16p2-3] (1.9.17-1 Debian:13/stable [amd64])\n")}, nil
	})
	for _, full := range []bool{false, true} {
		ups, err := b.Packages.Upgradable(ctx, full)
		if err != nil || len(ups) != 1 || ups[0].Name != "sudo" {
			t.Errorf("upgradable(%v) = %+v, %v", full, ups, err)
		}
	}
	got := runner.argv()
	if len(got) != 2 || !strings.Contains(got[0], "apt-get upgrade --simulate") || !strings.Contains(got[1], "apt-get full-upgrade --simulate") {
		t.Errorf("commands = %q", got)
	}
}
