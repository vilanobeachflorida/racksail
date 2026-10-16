package system

import (
	"bytes"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Files the tuning renderers produce.
const (
	KernelSysctlFile     = "/etc/sysctl.d/90-racksail-kernel.conf"
	ModulesLoadFile      = "/etc/modules-load.d/racksail.conf"
	ZramGeneratorFile    = "/etc/systemd/zram-generator.conf"
	AutoUpgradesFile     = "/etc/apt/apt.conf.d/20auto-upgrades"
	UnattendedConfigFile = "/etc/apt/apt.conf.d/52racksail-unattended-upgrades"
	TimesyncdDropIn      = "/etc/systemd/timesyncd.conf.d/racksail.conf"
	ZramPackage          = "systemd-zram-generator"
	UnattendedPackage    = "unattended-upgrades"
)

// KernelDefaults returns the kernel parameters every Racksail server gets:
// BBR congestion control with the fq scheduler, bigger accept queues and
// buffers (HTTP/3 needs large UDP buffers), a connection tracking table
// sized to the memory with a shorter idle timeout, hardened ICMP and
// routing defaults, more inotify watches, and swap behavior tuned for zram
// when it is used.
func KernelDefaults(memoryBytes int64, zram bool) map[string]string {
	conntrack := memoryBytes / (1 << 20) * 64
	conntrack = min(max(conntrack, 65536), 2097152)
	v := map[string]string{
		"net.core.default_qdisc":                             "fq",
		"net.ipv4.tcp_congestion_control":                    "bbr",
		"net.core.somaxconn":                                 "16384",
		"net.core.netdev_max_backlog":                        "16384",
		"net.ipv4.tcp_max_syn_backlog":                       "16384",
		"net.ipv4.tcp_fastopen":                              "3",
		"net.ipv4.tcp_mtu_probing":                           "1",
		"net.ipv4.tcp_slow_start_after_idle":                 "0",
		"net.ipv4.tcp_notsent_lowat":                         "131072",
		"net.ipv4.tcp_fin_timeout":                           "30",
		"net.ipv4.tcp_keepalive_time":                        "600",
		"net.ipv4.tcp_rfc1337":                               "1",
		"net.ipv4.tcp_syncookies":                            "1",
		"net.ipv4.ip_local_port_range":                       "10000 65535",
		"net.core.rmem_max":                                  "16777216",
		"net.core.wmem_max":                                  "16777216",
		"net.ipv4.tcp_rmem":                                  "4096 131072 16777216",
		"net.ipv4.tcp_wmem":                                  "4096 65536 16777216",
		"net.ipv4.conf.all.rp_filter":                        "2",
		"net.ipv4.conf.default.rp_filter":                    "2",
		"net.ipv4.conf.all.accept_redirects":                 "0",
		"net.ipv4.conf.default.accept_redirects":             "0",
		"net.ipv4.conf.all.send_redirects":                   "0",
		"net.ipv4.conf.all.accept_source_route":              "0",
		"net.ipv6.conf.all.accept_redirects":                 "0",
		"net.ipv6.conf.default.accept_redirects":             "0",
		"net.netfilter.nf_conntrack_max":                     strconv.FormatInt(conntrack, 10),
		"net.netfilter.nf_conntrack_tcp_timeout_established": "86400",
		"fs.inotify.max_user_watches":                        "524288",
		"fs.inotify.max_user_instances":                      "1024",
		"kernel.pid_max":                                     "4194304",
		"vm.max_map_count":                                   "262144",
		"vm.swappiness":                                      "10",
	}
	if zram {
		// Compressed memory is cheap to swap to, so swap early and in
		// single pages.
		v["vm.swappiness"] = "180"
		v["vm.watermark_boost_factor"] = "0"
		v["vm.watermark_scale_factor"] = "125"
		v["vm.page-cluster"] = "0"
	}
	return v
}

// tunableSysctls are the kernel parameters Racksail sets: its defaults, and
// what its services need, such as forwarding for containers and VMs or more
// memory maps for databases. Nothing else can be set, so no operation can
// weaken the kernel's own defenses, as vm.mmap_min_addr,
// vm.unprivileged_userfaultfd or net.core.bpf_jit_harden would.
var tunableSysctls = []string{
	"net.core.default_qdisc", "net.core.somaxconn", "net.core.netdev_max_backlog", "net.core.rmem_default",
	"net.core.rmem_max", "net.core.wmem_default", "net.core.wmem_max", "net.core.optmem_max",
	"net.ipv4.tcp_congestion_control", "net.ipv4.tcp_max_syn_backlog", "net.ipv4.tcp_fastopen", "net.ipv4.tcp_mtu_probing",
	"net.ipv4.tcp_slow_start_after_idle", "net.ipv4.tcp_notsent_lowat", "net.ipv4.tcp_fin_timeout", "net.ipv4.tcp_keepalive_time",
	"net.ipv4.tcp_keepalive_intvl", "net.ipv4.tcp_keepalive_probes", "net.ipv4.tcp_rfc1337", "net.ipv4.tcp_syncookies",
	"net.ipv4.tcp_tw_reuse", "net.ipv4.tcp_max_tw_buckets", "net.ipv4.tcp_rmem", "net.ipv4.tcp_wmem", "net.ipv4.udp_rmem_min",
	"net.ipv4.udp_wmem_min", "net.ipv4.ip_local_port_range", "net.ipv4.ip_forward", "net.ipv4.ip_nonlocal_bind",
	"net.ipv4.ping_group_range", "net.ipv6.ip_nonlocal_bind",
	"net.ipv4.neigh.default.gc_thresh1", "net.ipv4.neigh.default.gc_thresh2", "net.ipv4.neigh.default.gc_thresh3",
	"net.ipv6.neigh.default.gc_thresh1", "net.ipv6.neigh.default.gc_thresh2", "net.ipv6.neigh.default.gc_thresh3",
	"net.netfilter.nf_conntrack_max", "net.netfilter.nf_conntrack_tcp_timeout_established",
	"fs.inotify.max_user_watches", "fs.inotify.max_user_instances", "fs.inotify.max_queued_events", "fs.file-max",
	"fs.aio-max-nr", "kernel.pid_max", "kernel.threads-max", "kernel.keys.maxkeys", "kernel.keys.maxbytes",
	"user.max_user_namespaces",
	"vm.max_map_count", "vm.swappiness", "vm.watermark_boost_factor", "vm.watermark_scale_factor", "vm.page-cluster",
	"vm.overcommit_memory", "vm.dirty_ratio", "vm.dirty_background_ratio", "vm.dirty_bytes", "vm.dirty_background_bytes",
	"vm.vfs_cache_pressure", "vm.min_free_kbytes", "vm.nr_hugepages",
}

// interfaceSysctls are the per-interface parameters Racksail sets, for all
// interfaces, the defaults or one by name.
var interfaceSysctls = map[string][]string{
	"net.ipv4.conf.": {"rp_filter", "accept_redirects", "send_redirects", "accept_source_route", "forwarding",
		"proxy_arp", "arp_ignore", "arp_announce"},
	"net.ipv6.conf.": {"accept_redirects", "forwarding", "accept_ra", "disable_ipv6", "proxy_ndp", "use_tempaddr", "autoconf"},
}

var interfaceNamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,15}$`)

// TunableSysctl reports whether key is a kernel parameter Racksail sets.
func TunableSysctl(key string) bool {
	if !ValidSysctlKey(key) {
		return false
	}
	if slices.Contains(tunableSysctls, key) {
		return true
	}
	for prefix, names := range interfaceSysctls {
		rest, ok := strings.CutPrefix(key, prefix)
		if !ok {
			continue
		}
		iface, name, ok := strings.Cut(rest, ".")
		if ok && interfaceNamePattern.MatchString(iface) && slices.Contains(names, name) {
			return true
		}
	}
	return false
}

// CheckSysctl checks a kernel parameter and its value: one Racksail sets,
// and a value made of numbers and words, which can't break the file it is
// kept in.
func CheckSysctl(key, value string) error {
	if !TunableSysctl(key) {
		return fmt.Errorf("parameter %s isn't one Racksail tunes: %w", key, ErrInvalid)
	}
	if !sysctlValuePattern.MatchString(value) {
		return fmt.Errorf("value %q of %s: %w", value, key, ErrInvalid)
	}
	return nil
}

// KernelModules are loaded at boot before the kernel parameters are set:
// BBR, and connection tracking so its parameters exist.
var KernelModules = []string{"tcp_bbr", "nf_conntrack"}

var sysctlValuePattern = regexp.MustCompile(`^[A-Za-z0-9 ._:,/-]{1,256}$`)

// RenderSysctl renders a sysctl.d file.
func RenderSysctl(values map[string]string) ([]byte, error) {
	var b bytes.Buffer
	b.WriteString(GeneratedHeader + "\n")
	for _, k := range slices.Sorted(maps.Keys(values)) {
		if err := CheckSysctl(k, values[k]); err != nil {
			return nil, err
		}
		fmt.Fprintf(&b, "%s = %s\n", k, values[k])
	}
	return b.Bytes(), nil
}

var moduleNamePattern = regexp.MustCompile(`^[a-z0-9_-]{1,64}$`)

// RenderModulesLoad renders a modules-load.d file.
func RenderModulesLoad(modules []string) ([]byte, error) {
	var b bytes.Buffer
	b.WriteString(GeneratedHeader + "\n")
	for _, m := range modules {
		if !moduleNamePattern.MatchString(m) {
			return nil, fmt.Errorf("kernel module %q: %w", m, ErrInvalid)
		}
		b.WriteString(m + "\n")
	}
	return b.Bytes(), nil
}

// ZramConfig is compressed swap in memory, set up by zram-generator.
type ZramConfig struct {
	// SizePercent is the share of RAM the swap device may hold, 50 by
	// default; MaxMiB caps it.
	SizePercent int `json:"sizePercent,omitempty"`
	MaxMiB      int `json:"maxMiB,omitempty"`
	// Algorithm is zstd by default.
	Algorithm string `json:"algorithm,omitempty" enum:"zstd,lz4,lzo-rle"`
	Priority  int    `json:"priority,omitempty"`
}

// Render returns zram-generator.conf.
func (z ZramConfig) Render() ([]byte, error) {
	pct, maxMiB, algo, prio := z.SizePercent, z.MaxMiB, z.Algorithm, z.Priority
	if pct == 0 {
		pct = 50
	}
	if maxMiB == 0 {
		maxMiB = 8192
	}
	if algo == "" {
		algo = "zstd"
	}
	if prio == 0 {
		prio = 100
	}
	if pct < 1 || pct > 150 || maxMiB < 64 || !slices.Contains([]string{"zstd", "lz4", "lzo-rle"}, algo) || prio < 1 || prio > 32767 {
		return nil, fmt.Errorf("zram settings %+v: %w", z, ErrInvalid)
	}
	var b bytes.Buffer
	b.WriteString(GeneratedHeader + "\n")
	b.WriteString("[zram0]\n")
	fmt.Fprintf(&b, "zram-size = min(ram * %d / 100, %d)\n", pct, maxMiB)
	fmt.Fprintf(&b, "compression-algorithm = %s\n", algo)
	fmt.Fprintf(&b, "swap-priority = %d\n", prio)
	b.WriteString("fs-type = swap\n")
	return b.Bytes(), nil
}

// UnattendedUpgrades configures automatic updates. Security updates from
// Debian are always included; Racksail replaces the distribution's lists so
// nothing else is upgraded unless asked for.
type UnattendedUpgrades struct {
	Enabled bool `json:"enabled"`
	// IncludeStable also takes Debian point-release updates.
	IncludeStable bool `json:"includeStable,omitempty"`
	// Origins are more origin patterns, such as a vendor repository's
	// security suite.
	Origins []string `json:"origins,omitempty"`
	// Hold lists packages never upgraded automatically, such as the
	// database majors gated by tested manifests.
	Hold []string `json:"hold,omitempty"`
	// AutoReboot reboots at RebootTime when an update needs it.
	AutoReboot bool   `json:"autoReboot,omitempty"`
	RebootTime string `json:"rebootTime,omitempty" doc:"Time of day for automatic reboots, such as 04:00"`
}

var (
	originPattern     = regexp.MustCompile(`^[A-Za-z0-9=,.:${}_ +/-]{1,200}$`)
	timeOfDayPattern  = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`)
	aptPackagePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9.+*-]{0,99}$`)
	aptCommentHeader  = "// " + strings.TrimPrefix(GeneratedHeader, "# ")
)

// Render returns the apt configuration files keyed by path.
func (u UnattendedUpgrades) Render() (map[string][]byte, error) {
	for _, o := range u.Origins {
		if !originPattern.MatchString(o) || strings.Contains(o, `"`) {
			return nil, fmt.Errorf("origin pattern %q: %w", o, ErrInvalid)
		}
	}
	for _, p := range u.Hold {
		if !aptPackagePattern.MatchString(p) {
			return nil, fmt.Errorf("package %q: %w", p, ErrInvalid)
		}
	}
	reboot := u.RebootTime
	if reboot == "" {
		reboot = "04:00"
	}
	if !timeOfDayPattern.MatchString(reboot) {
		return nil, fmt.Errorf("reboot time %q: %w", reboot, ErrInvalid)
	}
	flag := func(b bool) string {
		if b {
			return "1"
		}
		return "0"
	}
	var periodic bytes.Buffer
	periodic.WriteString(aptCommentHeader + "\n")
	periodic.WriteString(`APT::Periodic::Update-Package-Lists "1";` + "\n")
	fmt.Fprintf(&periodic, "APT::Periodic::Unattended-Upgrade %q;\n", flag(u.Enabled))
	periodic.WriteString(`APT::Periodic::AutocleanInterval "7";` + "\n")

	var conf bytes.Buffer
	conf.WriteString(aptCommentHeader + "\n")
	conf.WriteString("#clear Unattended-Upgrade::Origins-Pattern;\n")
	conf.WriteString("Unattended-Upgrade::Origins-Pattern {\n")
	conf.WriteString("\t\"origin=Debian,codename=${distro_codename},label=Debian-Security\";\n")
	conf.WriteString("\t\"origin=Debian,codename=${distro_codename}-security,label=Debian-Security\";\n")
	if u.IncludeStable {
		conf.WriteString("\t\"origin=Debian,codename=${distro_codename},label=Debian\";\n")
		conf.WriteString("\t\"origin=Debian,codename=${distro_codename}-updates,label=Debian\";\n")
	}
	for _, o := range u.Origins {
		fmt.Fprintf(&conf, "\t%q;\n", o)
	}
	conf.WriteString("};\n")
	conf.WriteString("#clear Unattended-Upgrade::Package-Blacklist;\n")
	conf.WriteString("Unattended-Upgrade::Package-Blacklist {\n")
	for _, p := range u.Hold {
		fmt.Fprintf(&conf, "\t%q;\n", p)
	}
	conf.WriteString("};\n")
	conf.WriteString("Unattended-Upgrade::AutoFixInterruptedDpkg \"true\";\n")
	conf.WriteString("Unattended-Upgrade::MinimalSteps \"true\";\n")
	conf.WriteString("Unattended-Upgrade::Remove-Unused-Kernel-Packages \"true\";\n")
	conf.WriteString("Unattended-Upgrade::Remove-New-Unused-Dependencies \"true\";\n")
	conf.WriteString("Unattended-Upgrade::SyslogEnable \"true\";\n")
	fmt.Fprintf(&conf, "Unattended-Upgrade::Automatic-Reboot %q;\n", strconv.FormatBool(u.AutoReboot))
	fmt.Fprintf(&conf, "Unattended-Upgrade::Automatic-Reboot-Time %q;\n", reboot)
	return map[string][]byte{AutoUpgradesFile: periodic.Bytes(), UnattendedConfigFile: conf.Bytes()}, nil
}

// TimeSync configures systemd-timesyncd.
type TimeSync struct {
	Servers  []string `json:"servers,omitempty" doc:"NTP servers; Debian's pool when empty"`
	Fallback []string `json:"fallback,omitempty"`
}

var ntpServerPattern = regexp.MustCompile(`^[A-Za-z0-9.:-]{1,253}$`)

// DefaultNTPServers are Debian's pool.
var DefaultNTPServers = []string{"0.debian.pool.ntp.org", "1.debian.pool.ntp.org", "2.debian.pool.ntp.org", "3.debian.pool.ntp.org"}

// Render returns the timesyncd drop-in.
func (t TimeSync) Render() ([]byte, error) {
	servers := t.Servers
	if len(servers) == 0 {
		servers = DefaultNTPServers
	}
	for _, s := range append(slices.Clone(servers), t.Fallback...) {
		if !ntpServerPattern.MatchString(s) {
			return nil, fmt.Errorf("NTP server %q: %w", s, ErrInvalid)
		}
	}
	var u UnitFile
	time := u.Section("Time").Add("NTP", strings.Join(servers, " "))
	if len(t.Fallback) > 0 {
		time.Add("FallbackNTP", strings.Join(t.Fallback, " "))
	}
	return u.Render()
}
