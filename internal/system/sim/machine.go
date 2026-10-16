package sim

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"io/fs"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/system"
)

// ErrNotSimulated is returned when a program has no simulation.
var ErrNotSimulated = errors.New("program is not simulated")

// Options describe the simulated machine.
type Options struct {
	Hostname string
	// Arch is amd64 or arm64.
	Arch        string
	CPUs        int
	MemoryBytes int64
	// NoVirtualization simulates hardware without VT-x/AMD-V, where the VMs
	// module can't be turned on.
	NoVirtualization bool
	// Realistic makes operations take about as long as they would on a
	// real server: package downloads, service starts, formatting disks.
	// Development mode turns it on; tests leave it off.
	Realistic bool
	// Clock returns the current time; it defaults to time.Now.
	Clock func() time.Time
}

// Machine is a simulated server. All methods are safe for concurrent use.
type Machine struct {
	mu        sync.Mutex
	clock     func() time.Time
	realistic bool
	facts     system.MachineFacts

	files     map[string]*node
	users     map[string]*system.User
	groups    map[string]*system.Group
	units     map[string]*unitRuntime
	enabled   map[string]bool
	builtin   map[string]bool
	reloads   int
	packages  map[string]system.Package
	offers    map[string]string
	sources   map[string]system.AptSource
	archives  map[string]system.Archive
	subvols   map[string]*system.Subvolume
	quotas    map[string]int64
	nextSub   uint64
	btrfsFS   map[string]*btrfsFS
	mounts    []system.Mount
	disks     []*system.Disk
	ruleset   []byte
	sets      map[string]map[string]time.Time
	ifaces    []system.Interface
	routes    []system.Route
	hostname  string
	sysctl    map[string]string
	procs     map[int]*system.Process
	nextPID   int
	holds     map[string]bool
	smart     map[string]*diskHealth
	journal   []logEntry
	nextLog   uint64
	activity  map[string]time.Time
	stats     *statsState
	downloads map[string][]byte
	programs  map[string]Program
	calls     []Call
	failures  map[string][]error
	// store keeps the machine on disk when it was opened from a
	// directory; nil for machines that live in memory only.
	store *store
}

// Call records one adapter call, for test assertions.
type Call struct {
	Area   string
	Op     string
	Target string
}

func (c Call) String() string { return c.Area + "." + c.Op + " " + c.Target }

// New returns a machine in the state of a fresh Racksail install.
func New(opts Options) *Machine {
	if opts.Hostname == "" {
		opts.Hostname = "sim"
	}
	if opts.Arch == "" {
		opts.Arch = "amd64"
	}
	if opts.CPUs == 0 {
		opts.CPUs = 4
	}
	if opts.MemoryBytes == 0 {
		opts.MemoryBytes = 8 << 30
	}
	if opts.Clock == nil {
		opts.Clock = time.Now
	}
	m := &Machine{
		clock:     opts.Clock,
		realistic: opts.Realistic,
		files:     map[string]*node{},
		users:     map[string]*system.User{},
		groups:    map[string]*system.Group{},
		units:     map[string]*unitRuntime{},
		enabled:   map[string]bool{},
		builtin:   map[string]bool{},
		packages:  map[string]system.Package{},
		offers:    map[string]string{},
		sources:   map[string]system.AptSource{},
		archives:  map[string]system.Archive{},
		subvols:   map[string]*system.Subvolume{},
		quotas:    map[string]int64{},
		nextSub:   256,
		btrfsFS:   map[string]*btrfsFS{},
		sets:      map[string]map[string]time.Time{},
		sysctl:    map[string]string{},
		procs:     map[int]*system.Process{},
		nextPID:   1000,
		holds:     map[string]bool{},
		smart:     map[string]*diskHealth{},
		activity:  map[string]time.Time{},
		programs:  map[string]Program{},
		failures:  map[string][]error{},
		hostname:  opts.Hostname,
	}
	m.seed(opts)
	return m
}

// Backend returns the machine as a system backend.
func (m *Machine) Backend() *system.Backend {
	return &system.Backend{
		Name:      "sim",
		Facts:     simFacts{m},
		Users:     simUsers{m},
		Files:     simFiles{m},
		Units:     simUnits{m},
		Packages:  simPackages{m},
		Btrfs:     simBtrfs{m},
		Mounts:    simMounts{m},
		Disks:     simDisks{m},
		Firewall:  simFirewall{m},
		Network:   simNetwork{m},
		Sysctl:    simSysctl{m},
		Processes: simProcesses{m},
		Certs:     system.NewCertStore(simFiles{m}, system.CertsDir),
		Journal:   simJournal{m},
		Metrics:   simMetrics{m},
		Exec:      simExec{m},
	}
}

// maxCalls is how many of the latest adapter calls a machine keeps, so a
// long racksail dev session doesn't grow without end.
const maxCalls = 10000

// Calls returns the latest adapter calls, up to maxCalls of them.
func (m *Machine) Calls() []Call {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.calls)
}

// ResetCalls forgets the recorded calls.
func (m *Machine) ResetCalls() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = nil
}

// FailNext makes the next call of op on target fail with err. op is
// "<area>.<operation>" in lower case, as recorded in Calls, for example
// "units.start" with target "racksail-site-blog.service".
func (m *Machine) FailNext(op, target string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := op + " " + target
	m.failures[key] = append(m.failures[key], err)
}

// record notes a call and returns an injected failure, if any. Every call
// that changes the machine is recorded, so it is also when a machine kept
// on disk is saved. The caller must hold m.mu.
func (m *Machine) record(area, op, target string) error {
	m.calls = append(m.calls, Call{Area: area, Op: op, Target: target})
	if len(m.calls) > maxCalls+maxCalls/10 {
		m.calls = slices.Clone(m.calls[len(m.calls)-maxCalls:])
	}
	m.changed()
	key := area + "." + op + " " + target
	if errs := m.failures[key]; len(errs) > 0 {
		m.failures[key] = errs[1:]
		return errs[0]
	}
	return nil
}

// changed schedules a save of a machine kept on disk. The caller holds
// m.mu.
func (m *Machine) changed() {
	if m.store != nil {
		m.store.schedule()
	}
}

func (m *Machine) now() time.Time { return m.clock().UTC() }

func notFound(what string) error { return fmt.Errorf("%s: %w", what, system.ErrNotFound) }

func invalid(format string, args ...any) error {
	return fmt.Errorf(format+": %w", append(args, system.ErrInvalid)...)
}

// lock takes the machine lock unless the context is already done.
func (m *Machine) lock(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	return nil
}

// hash gives stable pseudo-random numbers for simulated values.
func hash(s string) uint64 {
	h := fnv.New64a()
	h.Write([]byte(s))
	return h.Sum64()
}

func (m *Machine) seed(opts Options) {
	now := m.now()
	m.facts = system.MachineFacts{
		Hostname:       opts.Hostname,
		FQDN:           opts.Hostname + ".racksail.test",
		MachineID:      fmt.Sprintf("%016x%016x", hash(opts.Hostname), hash(opts.Hostname+"machine")),
		BootID:         fmt.Sprintf("%016x%016x", hash(opts.Hostname+"boot"), hash(now.String())),
		OS:             system.OSRelease{ID: "debian", VersionID: "13", VersionCodename: "trixie", PrettyName: "Debian GNU/Linux 13 (trixie)"},
		Kernel:         "6.12.48+deb13-" + opts.Arch,
		Arch:           opts.Arch,
		CPU:            simCPU(opts),
		MemoryBytes:    opts.MemoryBytes,
		SwapBytes:      opts.MemoryBytes / 2,
		Firmware:       system.Firmware{UEFI: true, SecureBoot: true},
		TPM:            true,
		TPMVersion:     "2",
		KVM:            !opts.NoVirtualization,
		IOMMU:          !opts.NoVirtualization,
		Timezone:       "UTC",
		BootTime:       now.Add(-72 * time.Hour),
		Virtualization: "",
	}

	for _, g := range []system.Group{
		{Name: "root", GID: 0, System: true},
		{Name: "adm", GID: 4, System: true},
		{Name: "sudo", GID: 27, System: true},
		{Name: "users", GID: 100, System: true},
		{Name: "systemd-journal", GID: 999, System: true},
		{Name: "racksail", GID: 990, System: true},
		{Name: "racksail-edge", GID: 989, System: true},
		{Name: "nogroup", GID: 65534, System: true},
	} {
		g := g
		m.groups[g.Name] = &g
	}
	for _, u := range []system.User{
		{Name: "root", UID: 0, GID: 0, Home: "/root", Shell: "/bin/bash", System: true},
		{Name: "racksail", UID: 990, GID: 990, Home: system.StateDir, Shell: "/usr/sbin/nologin", Comment: "Racksail panel", System: true, Locked: true},
		{Name: "racksail-edge", UID: 989, GID: 989, Home: "/nonexistent", Shell: "/usr/sbin/nologin", Comment: "Racksail edge", System: true, Locked: true},
		{Name: "nobody", UID: 65534, GID: 65534, Home: "/nonexistent", Shell: "/usr/sbin/nologin", System: true, Locked: true},
	} {
		u := u
		m.users[u.Name] = &u
	}

	dirs := []struct {
		path  string
		perm  fs.FileMode
		owner string
	}{
		{"/", 0o755, "root"}, {"/boot", 0o755, "root"}, {"/boot/efi", 0o700, "root"},
		{"/etc", 0o755, "root"}, {"/etc/apt", 0o755, "root"}, {system.AptSourceDir, 0o755, "root"},
		{system.AptKeyDir, 0o755, "root"}, {system.AptPrefsDir, 0o755, "root"},
		{"/etc/systemd", 0o755, "root"}, {system.UnitDir, 0o755, "root"}, {system.NetworkdDir, 0o755, "root"},
		{"/etc/sysctl.d", 0o755, "root"}, {system.ConfigDir, 0o755, "root"}, {system.GeneratedDir, 0o755, "root"},
		{"/home", 0o755, "root"}, {"/mnt", 0o755, "root"}, {system.DataDisksDir, 0o755, "root"},
		{"/opt", 0o755, "root"}, {"/opt/racksail", 0o755, "root"}, {system.RuntimesDir, 0o755, "root"},
		{system.ModulesDir, 0o755, "root"}, {"/root", 0o700, "root"}, {"/run", 0o755, "root"},
		{system.RunDir, 0o750, "racksail"}, {"/srv", 0o755, "root"}, {"/srv/racksail", 0o755, "root"},
		{system.AccountsDir, 0o755, "root"}, {"/tmp", 0o1777, "root"}, {"/usr", 0o755, "root"},
		{"/usr/bin", 0o755, "root"}, {"/usr/sbin", 0o755, "root"}, {"/var", 0o755, "root"},
		{"/var/lib", 0o755, "root"}, {system.StateDir, 0o750, "racksail"}, {"/var/lib/racksail-db", 0o755, "root"},
		{"/var/log", 0o755, "root"}, {"/.snapshots", 0o750, "root"},
	}
	for _, d := range dirs {
		u := m.users[d.owner]
		m.files[d.path] = &node{mode: fs.ModeDir | d.perm, uid: u.UID, gid: u.GID, modTime: now}
	}
	m.files["/etc/os-release"] = &node{mode: 0o644, modTime: now, data: []byte(
		"PRETTY_NAME=\"Debian GNU/Linux 13 (trixie)\"\nNAME=\"Debian GNU/Linux\"\nVERSION_ID=\"13\"\nVERSION=\"13 (trixie)\"\nVERSION_CODENAME=trixie\nID=debian\n")}
	m.files["/etc/hostname"] = &node{mode: 0o644, modTime: now, data: []byte(opts.Hostname + "\n")}
	m.files["/etc/machine-id"] = &node{mode: 0o444, modTime: now, data: []byte(m.facts.MachineID + "\n")}
	// The server runs Racksail, installed where its package puts it.
	m.files["/usr/bin/racksail"] = &node{mode: 0o755, modTime: now, data: []byte("simulated racksail executable\n")}

	m.seedStorage(now)
	m.seedNetwork()
	m.seedUnitsAndPackages(now)

	m.sysctl = map[string]string{
		"kernel.hostname":                 opts.Hostname,
		"net.core.default_qdisc":          "fq_codel",
		"net.core.somaxconn":              "4096",
		"net.ipv4.ip_forward":             "0",
		"net.ipv4.tcp_congestion_control": "cubic",
		"net.ipv6.conf.all.forwarding":    "0",
		"vm.swappiness":                   "60",
		"fs.inotify.max_user_watches":     "65536",
	}
}

// CPU flags of the simulated processors: what a current server CPU reports,
// with the virtualization flag left out when the hardware lacks it.
var (
	amd64Flags = []string{"fpu", "vme", "de", "pse", "tsc", "msr", "pae", "mce", "cx8", "apic", "sep", "mtrr", "pge", "mca",
		"cmov", "pat", "pse36", "clflush", "mmx", "fxsr", "sse", "sse2", "ht", "syscall", "nx", "pdpe1gb", "rdtscp", "lm",
		"constant_tsc", "rep_good", "nopl", "xtopology", "nonstop_tsc", "cpuid", "pni", "pclmulqdq", "vmx", "ssse3", "fma",
		"cx16", "pcid", "sse4_1", "sse4_2", "x2apic", "movbe", "popcnt", "aes", "xsave", "avx", "f16c", "rdrand", "lahf_lm",
		"abm", "3dnowprefetch", "fsgsbase", "bmi1", "avx2", "smep", "bmi2", "erms", "invpcid", "rdseed", "adx", "smap",
		"clflushopt", "clwb", "sha_ni", "xsaveopt", "xsavec", "xgetbv1", "xsaves", "avx512f", "avx512dq", "avx512cd",
		"avx512bw", "avx512vl", "vaes", "vpclmulqdq", "rdpid"}
	arm64Flags = []string{"fp", "asimd", "evtstrm", "aes", "pmull", "sha1", "sha2", "crc32", "atomics", "fphp", "asimdhp",
		"cpuid", "asimdrdm", "jscvt", "fcma", "lrcpc", "dcpop", "sha3", "asimddp", "sha512", "asimdfhm", "dit", "uscat",
		"ilrcpc", "flagm", "ssbs", "sb", "paca", "pacg", "dcpodp", "flagm2", "frint"}
)

func simCPU(opts Options) system.CPUInfo {
	c := system.CPUInfo{Sockets: 1, Cores: opts.CPUs, Threads: opts.CPUs * 2}
	if opts.Arch == "arm64" {
		c.Model, c.Vendor, c.Threads = "Neoverse-N1", "Ampere", opts.CPUs
		c.Flags = slices.Clone(arm64Flags)
		if !opts.NoVirtualization {
			c.Virtualization, c.VirtualizationType = true, "arm64"
		}
		return c
	}
	c.Model, c.Vendor = "Simulated Xeon CPU @ 2.80GHz", "GenuineIntel"
	c.Flags = slices.DeleteFunc(slices.Clone(amd64Flags), func(f string) bool { return f == "vmx" && opts.NoVirtualization })
	if !opts.NoVirtualization {
		c.Virtualization, c.VirtualizationType = true, "vmx"
	}
	return c
}

// pathOwner resolves an Owner to numeric IDs. The caller holds m.mu.
func (m *Machine) pathOwner(o system.Owner) (int, int, error) {
	uid, gid := 0, 0
	if o.User != "" {
		u, ok := m.users[o.User]
		if !ok {
			return 0, 0, notFound("user " + o.User)
		}
		uid, gid = u.UID, u.GID
	}
	if o.Group != "" {
		g, ok := m.groups[o.Group]
		if !ok {
			return 0, 0, notFound("group " + o.Group)
		}
		gid = g.GID
	}
	return uid, gid, nil
}

func (m *Machine) userName(uid int) string {
	for _, u := range m.users {
		if u.UID == uid {
			return u.Name
		}
	}
	return fmt.Sprint(uid)
}

func (m *Machine) groupName(gid int) string {
	for _, g := range m.groups {
		if g.GID == gid {
			return g.Name
		}
	}
	return fmt.Sprint(gid)
}

func isUnder(p, dir string) bool {
	return dir == "/" || p == dir || strings.HasPrefix(p, dir+"/")
}
