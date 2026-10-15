package sim

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"path"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/system"
)

// PackageInfo describes a simulated Debian package.
type PackageInfo struct {
	Version string
	// Origin is the host of the repository the package comes from. Packages
	// from a host other than deb.debian.org need a matching apt source.
	Origin         string
	DownloadBytes  int64
	InstalledBytes int64
	// Units are the systemd units the package ships.
	Units []string
	// Depends lists packages installed along with it.
	Depends []string
}

type catalogItem struct {
	version   string
	origin    string
	download  int64
	installed int64
	units     []string
	depends   []string
}

func (c catalogItem) pkg(name, arch string) system.Package {
	return system.Package{Name: name, Version: c.version, Arch: arch, DownloadBytes: c.download, InstalledBytes: c.installed, Origin: c.origin}
}

const debianOrigin = "deb.debian.org"

var (
	catalogMu sync.RWMutex
	// The simulated archive: versions and sizes resemble Debian 13 and the
	// vendor repositories Racksail uses.
	catalog = map[string]catalogItem{
		"base-files":             {version: "13.8", download: 70 << 10, installed: 400 << 10},
		"linux-image-amd64":      {version: "6.12.48-1", download: 1 << 20, installed: 4 << 20},
		"linux-image-arm64":      {version: "6.12.48-1", download: 1 << 20, installed: 4 << 20},
		"systemd-zram-generator": {version: "1.2.1-2", download: 400 << 10, installed: 1 << 20, units: []string{"systemd-zram-setup@zram0.service"}},
		"unattended-upgrades":    {version: "2.12", download: 80 << 10, installed: 400 << 10, units: []string{"unattended-upgrades.service"}},
		"systemd-timesyncd":      {version: "257.8-1", download: 60 << 10, installed: 200 << 10, units: []string{"systemd-timesyncd.service"}},
		"systemd":                {version: "257.8-1", download: 3 << 20, installed: 11 << 20},
		"systemd-sysv":           {version: "257.8-1", download: 60 << 10, installed: 100 << 10},
		"openssh-server":         {version: "1:10.0p1-7", download: 600 << 10, installed: 2 << 20, units: []string{"ssh.service", "ssh.socket"}},
		"openssh-client":         {version: "1:10.0p1-7", download: 1 << 20, installed: 5 << 20},
		"btrfs-progs":            {version: "6.14-1", download: 1 << 20, installed: 6 << 20},
		"ca-certificates":        {version: "20250419", download: 160 << 10, installed: 400 << 10},
		"sudo":                   {version: "1.9.16p2-3", download: 2 << 20, installed: 6 << 20},
		"cron":                   {version: "3.0pl1-197", download: 80 << 10, installed: 300 << 10, units: []string{"cron.service"}},
		"nftables":               {version: "1.1.3-1", download: 80 << 10, installed: 300 << 10, units: []string{"nftables.service"}},
		"apache2":                {version: "2.4.65-2", download: 1 << 20, installed: 6 << 20, units: []string{"apache2.service", "apache2@.service"}},
		"mariadb-server":         {version: "1:11.8.3-0+deb13u1", download: 9 << 20, installed: 120 << 20, units: []string{"mariadb.service"}, depends: []string{"mariadb-client"}},
		"mariadb-client":         {version: "1:11.8.3-0+deb13u1", download: 3 << 20, installed: 30 << 20},
		"postgresql-17":          {version: "17.6-0+deb13u1", download: 18 << 20, installed: 60 << 20, units: []string{"postgresql@17-main.service", "postgresql.service"}},
		"valkey-server":          {version: "8.1.1+dfsg1-3", download: 1 << 20, installed: 5 << 20, units: []string{"valkey-server.service"}},
		"memcached":              {version: "1.6.38-1", download: 200 << 10, installed: 500 << 10, units: []string{"memcached.service"}},
		"knot":                   {version: "3.4.7-1", download: 1 << 20, installed: 4 << 20, units: []string{"knot.service"}},
		"unbound":                {version: "1.22.0-2", download: 1 << 20, installed: 3 << 20, units: []string{"unbound.service"}},
		"postfix":                {version: "3.10.3-1", download: 2 << 20, installed: 5 << 20, units: []string{"postfix.service"}},
		"cyrus-imapd":            {version: "3.10.1-1", download: 4 << 20, installed: 14 << 20, units: []string{"cyrus-imapd.service"}},
		"rspamd":                 {version: "3.12.1-1", download: 4 << 20, installed: 16 << 20, units: []string{"rspamd.service"}},
		"docker.io":              {version: "26.1.5+dfsg1-9", download: 30 << 20, installed: 110 << 20, units: []string{"docker.service", "docker.socket"}},
		"podman":                 {version: "5.4.2+ds1-2", download: 14 << 20, installed: 50 << 20},
		"incus":                  {version: "6.0.4-2", download: 30 << 20, installed: 100 << 20, units: []string{"incus.service", "incus.socket"}},
		"qemu-system-x86":        {version: "1:10.0.3+ds-1", download: 12 << 20, installed: 50 << 20},
		"qemu-system-arm":        {version: "1:10.0.3+ds-1", download: 12 << 20, installed: 50 << 20},
		"ovmf":                   {version: "2025.02-8", download: 7 << 20, installed: 20 << 20},
		"qemu-efi-aarch64":       {version: "2025.02-8", download: 4 << 20, installed: 12 << 20},
		"swtpm":                  {version: "0.7.3-2", download: 150 << 10, installed: 500 << 10},
		"mdadm":                  {version: "4.4-11", download: 500 << 10, installed: 1 << 20, units: []string{"mdmonitor.service"}},
		"snapraid":               {version: "12.4-1", download: 300 << 10, installed: 900 << 10},
		"mergerfs":               {version: "2.40.2-5", download: 300 << 10, installed: 900 << 10},
		"smartmontools":          {version: "7.4-3", download: 600 << 10, installed: 2 << 20, units: []string{"smartmontools.service"}},
		"hd-idle":                {version: "1.21+ds-3", download: 1 << 20, installed: 3 << 20, units: []string{"hd-idle.service"}},
		"zfs-dkms":               {version: "2.3.2-2", origin: debianOrigin, download: 3 << 20, installed: 20 << 20},
		"zfsutils-linux":         {version: "2.3.2-2", download: 600 << 10, installed: 2 << 20, units: []string{"zfs-import-cache.service", "zfs-mount.service"}},
		"samba":                  {version: "2:4.22.4+dfsg-1", download: 4 << 20, installed: 20 << 20, units: []string{"smbd.service", "nmbd.service"}},
		"nfs-kernel-server":      {version: "1:2.8.3-1", download: 200 << 10, installed: 600 << 10, units: []string{"nfs-server.service"}},
		"nut":                    {version: "2.8.1-5", download: 300 << 10, installed: 1 << 20, units: []string{"nut-server.service", "nut-monitor.service"}},
		"wireguard-tools":        {version: "1.0.20210914-1.1", download: 100 << 10, installed: 300 << 10, units: []string{"wg-quick@.service"}},
		"lm-sensors":             {version: "1:3.6.2-2", download: 100 << 10, installed: 400 << 10},
		"restic":                 {version: "0.18.0-1", download: 9 << 20, installed: 26 << 20},
		"rsync":                  {version: "3.4.1+ds1-5", download: 400 << 10, installed: 800 << 10},
		"mmdebstrap":             {version: "1.5.7-1", download: 200 << 10, installed: 700 << 10},
		"clamav":                 {version: "1.4.3+dfsg-1", download: 300 << 10, installed: 1 << 20, depends: []string{"clamav-base", "libclamav12"}},
		"clamav-base":            {version: "1.4.3+dfsg-1", download: 200 << 10, installed: 600 << 10},
		"libclamav12":            {version: "1.4.3+dfsg-1", download: 6 << 20, installed: 20 << 20},
		"clamav-daemon":          {version: "1.4.3+dfsg-1", download: 300 << 10, installed: 1 << 20, units: []string{"clamav-daemon.service"}, depends: []string{"clamav"}},
		"clamav-freshclam":       {version: "1.4.3+dfsg-1", download: 100 << 10, installed: 300 << 10, units: []string{"clamav-freshclam.service"}},
		"php8.4-fpm":             {version: "8.4.13-1+0~20251002", origin: "packages.sury.org", download: 2 << 20, installed: 7 << 20, units: []string{"php8.4-fpm.service"}, depends: []string{"php8.4-cli", "php8.4-common"}},
		"php8.4-cli":             {version: "8.4.13-1+0~20251002", origin: "packages.sury.org", download: 2 << 20, installed: 6 << 20},
		"php8.4-common":          {version: "8.4.13-1+0~20251002", origin: "packages.sury.org", download: 800 << 10, installed: 3 << 20},
		"php8.3-fpm":             {version: "8.3.26-1+0~20251002", origin: "packages.sury.org", download: 2 << 20, installed: 7 << 20, units: []string{"php8.3-fpm.service"}},
		"php8.2-fpm":             {version: "8.2.29-1+0~20250707", origin: "packages.sury.org", download: 2 << 20, installed: 7 << 20, units: []string{"php8.2-fpm.service"}},
		"php7.4-fpm":             {version: "7.4.33-24+0~20250707", origin: "packages.sury.org", download: 2 << 20, installed: 6 << 20, units: []string{"php7.4-fpm.service"}},
	}
)

// RegisterPackage adds or replaces a package in the simulated archive.
// Modules call it from init functions in their simulation code.
func RegisterPackage(name string, info PackageInfo) {
	catalogMu.Lock()
	defer catalogMu.Unlock()
	if info.Origin == "" {
		info.Origin = debianOrigin
	}
	catalog[name] = catalogItem{
		version: info.Version, origin: info.Origin, download: info.DownloadBytes,
		installed: info.InstalledBytes, units: slices.Clone(info.Units), depends: slices.Clone(info.Depends),
	}
}

// catalogEntry returns the archive entry for a package. Unknown names get a
// generated entry, so modules can install anything Debian ships without
// teaching the simulation about it first.
func catalogEntry(name string) catalogItem {
	catalogMu.RLock()
	defer catalogMu.RUnlock()
	if c, ok := catalog[name]; ok {
		if c.origin == "" {
			c.origin = debianOrigin
		}
		return c
	}
	h := hash(name)
	return catalogItem{
		version:   fmt.Sprintf("%d.%d.%d-1", 1+h%4, h%10, h%7),
		origin:    debianOrigin,
		download:  int64(64<<10 + h%(4<<20)),
		installed: int64(256<<10 + h%(16<<20)),
	}
}

type simPackages struct{ m *Machine }

// resolve expands names to the packages Install would add. The caller holds
// m.mu.
func (m *Machine) resolve(names []string) ([]system.Package, error) {
	seen := map[string]bool{}
	var out []system.Package
	var visit func(name string) error
	visit = func(name string) error {
		if seen[name] {
			return nil
		}
		seen[name] = true
		if _, installed := m.packages[name]; installed {
			return nil
		}
		e := catalogEntry(name)
		if e.origin != debianOrigin && !m.hasSourceFor(e.origin, name) {
			return fmt.Errorf("package %s has no installation candidate; it comes from %s: %w", name, e.origin, system.ErrNotFound)
		}
		out = append(out, e.pkg(name, m.facts.Arch))
		for _, d := range e.depends {
			if err := visit(d); err != nil {
				return err
			}
		}
		return nil
	}
	for _, n := range names {
		if err := visit(n); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (m *Machine) hasSourceFor(origin, pkg string) bool {
	for _, src := range m.sources {
		for _, u := range src.URIs {
			parsed, err := url.Parse(u)
			if err != nil || parsed.Hostname() != origin {
				continue
			}
			for _, pattern := range src.Packages {
				if ok, _ := path.Match(pattern, pkg); ok {
					return true
				}
			}
		}
	}
	return false
}

func (p simPackages) Installed(ctx context.Context, names ...string) ([]system.Package, error) {
	m := p.m
	if err := m.lock(ctx); err != nil {
		return nil, err
	}
	defer m.mu.Unlock()
	var out []system.Package
	for _, n := range names {
		if pkg, ok := m.packages[n]; ok {
			out = append(out, pkg)
		}
	}
	return out, nil
}

func (p simPackages) Candidates(ctx context.Context, names ...string) ([]system.Package, error) {
	m := p.m
	if err := m.lock(ctx); err != nil {
		return nil, err
	}
	defer m.mu.Unlock()
	return m.resolve(names)
}

func (p simPackages) UpdateIndex(ctx context.Context) error {
	m := p.m
	m.delay(ctx, opUpdateIndex, 0)
	if err := m.lock(ctx); err != nil {
		return err
	}
	defer m.mu.Unlock()
	return m.record("packages", "update_index", "")
}

func (p simPackages) Install(ctx context.Context, pkgs ...system.PackageRequest) error {
	m := p.m
	var bytes int64
	for _, r := range pkgs {
		e := catalogEntry(r.Name)
		bytes += e.download + e.installed/4
	}
	// About 40 MB/s for downloading and unpacking together.
	m.delay(ctx, opInstall, time.Duration(bytes/(40<<10))*time.Millisecond)
	if err := m.lock(ctx); err != nil {
		return err
	}
	defer m.mu.Unlock()
	names := make([]string, len(pkgs))
	for i, r := range pkgs {
		names[i] = r.Name
		if err := m.record("packages", "install", r.Name); err != nil {
			return err
		}
	}
	add, err := m.resolve(names)
	if err != nil {
		return err
	}
	for _, r := range pkgs {
		if v := m.newestVersion(r.Name); r.Version != "" && r.Version != v {
			return fmt.Errorf("version %s of %s is not available (candidate %s): %w", r.Version, r.Name, v, system.ErrNotFound)
		}
	}
	for _, r := range pkgs {
		if cur, ok := m.packages[r.Name]; ok {
			cur.Version = m.newestVersion(r.Name)
			m.packages[r.Name] = cur
		}
	}
	for _, pkg := range add {
		pkg.Version = m.newestVersion(pkg.Name)
		m.packages[pkg.Name] = pkg
		// Debian starts and enables the services a package ships.
		for _, u := range catalogEntry(pkg.Name).units {
			if strings.Contains(u, "@.") {
				continue
			}
			m.enabled[u] = true
			if path.Ext(u) == ".service" || path.Ext(u) == ".socket" {
				if err := m.startUnit(u); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (p simPackages) Remove(ctx context.Context, purge bool, names ...string) error {
	m := p.m
	m.delay(ctx, opRemove, 0)
	if err := m.lock(ctx); err != nil {
		return err
	}
	defer m.mu.Unlock()
	for _, n := range names {
		if err := m.record("packages", "remove", n); err != nil {
			return err
		}
		if _, ok := m.packages[n]; !ok {
			continue
		}
		for _, u := range catalogEntry(n).units {
			m.stopUnit(u)
			delete(m.units, u)
			delete(m.enabled, u)
		}
		delete(m.packages, n)
	}
	return nil
}

func (p simPackages) Upgradable(ctx context.Context) ([]system.Package, error) {
	m := p.m
	if err := m.lock(ctx); err != nil {
		return nil, err
	}
	defer m.mu.Unlock()
	var out []system.Package
	for name, pkg := range m.packages {
		if v := m.newestVersion(name); v != pkg.Version {
			up := catalogEntry(name).pkg(name, m.facts.Arch)
			up.Version = v
			out = append(out, up)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// kernelPackage is the package whose upgrades install a new kernel.
var kernelPackage = map[string]string{"amd64": "linux-image-amd64", "arm64": "linux-image-arm64"}

// kernelRelease turns a kernel package version such as 6.12.57-1 into the
// release the kernel reports, 6.12.57+deb13-amd64.
func kernelRelease(version, arch string) string {
	upstream, _, _ := strings.Cut(version, "-")
	return upstream + "+deb13-" + arch
}

func (p simPackages) Upgrade(ctx context.Context, full bool) error {
	m := p.m
	m.delay(ctx, opUpgrade, 0)
	if err := m.lock(ctx); err != nil {
		return err
	}
	defer m.mu.Unlock()
	if err := m.record("packages", "upgrade", fmt.Sprint(full)); err != nil {
		return err
	}
	for name, pkg := range m.packages {
		v := m.newestVersion(name)
		if v == pkg.Version || m.holds[name] {
			continue
		}
		// A new kernel brings new packages, which only a full upgrade
		// installs.
		if name == kernelPackage[m.facts.Arch] {
			if !full {
				continue
			}
			release := kernelRelease(v, m.facts.Arch)
			if err := m.writeFile("/boot/vmlinuz-"+release, []byte("simulated kernel "+release), 0o644, system.Owner{}); err != nil {
				return err
			}
		}
		pkg.Version = v
		m.packages[name] = pkg
		for _, u := range catalogEntry(name).units {
			if rt, ok := m.units[u]; ok && rt.active == "active" {
				m.stopUnit(u)
				if err := m.startUnit(u); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (p simPackages) SetHold(ctx context.Context, hold bool, names ...string) error {
	m := p.m
	if err := m.lock(ctx); err != nil {
		return err
	}
	defer m.mu.Unlock()
	for _, n := range names {
		if err := m.record("packages", "hold", n); err != nil {
			return err
		}
		if _, ok := m.packages[n]; !ok {
			return notFound("package " + n)
		}
		if hold {
			m.holds[n] = true
		} else {
			delete(m.holds, n)
		}
	}
	return nil
}

func (p simPackages) Held(ctx context.Context) ([]string, error) {
	m := p.m
	if err := m.lock(ctx); err != nil {
		return nil, err
	}
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.holds))
	for n := range m.holds {
		out = append(out, n)
	}
	sort.Strings(out)
	return out, nil
}

// Reboot restarts the simulated server: the newest installed kernel runs,
// processes are gone, and units enabled at boot start again.
func (m *Machine) Reboot() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rebootLocked()
	m.changed()
}

// rebootLocked does the reboot. The caller holds m.mu.
func (m *Machine) rebootLocked() {
	for name := range m.units {
		m.stopUnit(name)
	}
	for pid := range m.procs {
		if pid != 1 {
			delete(m.procs, pid)
		}
	}
	now := m.now()
	m.facts.BootTime = now
	m.facts.BootID = fmt.Sprintf("%016x%016x", hash(m.hostname+"boot"), hash(now.String()))
	if newest := m.newestKernel(); newest != "" {
		m.facts.Kernel = newest
	}
	m.logSystemd("", system.PriorityInfo, "Booting %s %s.", m.facts.OS.PrettyName, m.facts.Kernel)
	names := make([]string, 0, len(m.enabled))
	for name, on := range m.enabled {
		if on {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, slice := range []string{system.PlatformSlice, system.AccountsSlice} {
		m.startUnit(slice)
	}
	for _, name := range names {
		m.startUnit(name)
	}
}

// newestKernel is the newest kernel in /boot. The caller holds m.mu.
func (m *Machine) newestKernel() string {
	newest := ""
	for p := range m.files {
		if release, ok := strings.CutPrefix(p, "/boot/vmlinuz-"); ok && compareVersions(release, newest) > 0 {
			newest = release
		}
	}
	return newest
}

// compareVersions compares dotted version numbers, as kernel releases use.
func compareVersions(a, b string) int {
	pa, pb := strings.FieldsFunc(a, notDigit), strings.FieldsFunc(b, notDigit)
	for i := 0; i < max(len(pa), len(pb)); i++ {
		var x, y int
		if i < len(pa) {
			fmt.Sscan(pa[i], &x)
		}
		if i < len(pb) {
			fmt.Sscan(pb[i], &y)
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}

func notDigit(r rune) bool { return r < '0' || r > '9' }

// newestVersion is the version an upgrade would install. The caller holds
// m.mu.
func (m *Machine) newestVersion(name string) string {
	if v, ok := m.offers[name]; ok {
		return v
	}
	return catalogEntry(name).version
}

// OfferUpgrade publishes a newer version of a package on this machine's
// mirror, so update flows can be tested.
func (m *Machine) OfferUpgrade(name, version string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.offers[name] = version
	m.changed()
}

func (p simPackages) AddSource(ctx context.Context, src system.AptSource) error {
	files, err := src.Render()
	if err != nil {
		return err
	}
	m := p.m
	if err := m.lock(ctx); err != nil {
		return err
	}
	defer m.mu.Unlock()
	if err := m.record("packages", "add_source", src.Name); err != nil {
		return err
	}
	for _, f := range []struct {
		path string
		data []byte
	}{{files.KeyPath, files.Key}, {files.SourcesPath, files.Sources}, {files.PrefsPath, files.Prefs}} {
		if err := m.writeFile(f.path, f.data, 0o644, system.Owner{}); err != nil {
			return err
		}
	}
	m.sources[src.Name] = src
	return nil
}

func (p simPackages) RemoveSource(ctx context.Context, name string) error {
	m := p.m
	if err := m.lock(ctx); err != nil {
		return err
	}
	defer m.mu.Unlock()
	if err := m.record("packages", "remove_source", name); err != nil {
		return err
	}
	if _, ok := m.sources[name]; !ok {
		return notFound("apt source " + name)
	}
	stem := "racksail-" + name
	delete(m.files, path.Join(system.AptSourceDir, stem+".sources"))
	delete(m.files, path.Join(system.AptKeyDir, stem+".asc"))
	delete(m.files, path.Join(system.AptPrefsDir, stem+".pref"))
	delete(m.sources, name)
	return nil
}

func (p simPackages) Sources(ctx context.Context) ([]string, error) {
	m := p.m
	if err := m.lock(ctx); err != nil {
		return nil, err
	}
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.sources))
	for name := range m.sources {
		out = append(out, name)
	}
	sort.Strings(out)
	return out, nil
}

var (
	downloadsMu sync.RWMutex
	downloads   = map[string][]byte{}
)

// RegisterDownload makes url downloadable on every simulated machine, so a
// module's vendor archives, signatures and checksum lists can be installed
// and verified for real.
func RegisterDownload(url string, data []byte) {
	downloadsMu.Lock()
	defer downloadsMu.Unlock()
	downloads[url] = slices.Clone(data)
}

// SetDownload makes url downloadable on this machine only, taking
// precedence over RegisterDownload.
func (m *Machine) SetDownload(url string, data []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.downloads == nil {
		m.downloads = map[string][]byte{}
	}
	m.downloads[url] = slices.Clone(data)
}

// download returns the simulated contents of url. The caller holds m.mu.
func (m *Machine) download(url string) ([]byte, bool) {
	if data, ok := m.downloads[url]; ok {
		return data, true
	}
	downloadsMu.RLock()
	defer downloadsMu.RUnlock()
	data, ok := downloads[url]
	return data, ok
}

// mkdirParents creates the missing directories above p. The caller holds
// m.mu.
func (m *Machine) mkdirParents(p string) error {
	var missing []string
	for dir := path.Dir(p); ; dir = path.Dir(dir) {
		if _, ok := m.files[dir]; ok {
			break
		}
		missing = append(missing, dir)
	}
	for i := len(missing) - 1; i >= 0; i-- {
		if err := m.mkdir(missing[i], 0o755, system.Owner{}); err != nil {
			return err
		}
	}
	return nil
}

// InstallArchive downloads a vendor archive. Archives whose URL has
// simulated contents (RegisterDownload) are verified and unpacked exactly
// as on a real server; any other archive is taken as verified and becomes
// an empty versioned directory, since a simulated server has no network.
func (p simPackages) InstallArchive(ctx context.Context, a system.Archive) error {
	if err := a.Validate(); err != nil {
		return err
	}
	m := p.m
	m.delay(ctx, opArchive, time.Duration(a.Size/(50<<20))*time.Second)
	if err := m.lock(ctx); err != nil {
		return err
	}
	if err := m.record("packages", "install_archive", a.Dest); err != nil {
		m.mu.Unlock()
		return err
	}
	if _, ok := m.files[a.Dest]; ok {
		m.mu.Unlock()
		return nil
	}
	if err := m.mkdirParents(a.Dest); err != nil {
		m.mu.Unlock()
		return err
	}
	data, simulated := m.download(a.URL)
	if !simulated {
		defer m.mu.Unlock()
		if err := m.mkdir(a.Dest, 0o755, system.Owner{}); err != nil {
			return err
		}
		m.archives[a.Dest] = a
		marker := fmt.Sprintf("url=%s\nsha256=%s\n", a.URL, a.SHA256)
		return m.writeFile(path.Join(a.Dest, ".racksail-archive"), []byte(marker), 0o644, system.Owner{})
	}
	var signature, checksums []byte
	var missing []string
	if s := a.Signature; s != nil {
		var ok bool
		if signature, ok = m.download(s.URL); !ok {
			missing = append(missing, s.URL)
		}
		if s.Checksums != "" {
			if checksums, ok = m.download(s.Checksums); !ok {
				missing = append(missing, s.Checksums)
			}
		}
	}
	parent := path.Dir(a.Dest)
	archiveName := system.TreeTempPrefix + "download-" + path.Base(a.Dest)
	stagingName := system.TreeTempPrefix + "unpack-" + path.Base(a.Dest)
	m.mu.Unlock()

	if len(missing) > 0 {
		return fmt.Errorf("downloading %s: %w", strings.Join(missing, ", "), system.ErrNotFound)
	}
	if int64(len(data)) > a.Size {
		return fmt.Errorf("%s is larger than the expected %d bytes: %w", a.URL, a.Size, system.ErrTooLarge)
	}
	sum := sha256.Sum256(data)
	open := func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(data)), nil }
	if err := a.Verify(hex.EncodeToString(sum[:]), open, signature, checksums); err != nil {
		return err
	}
	if a.Format == "tar.xz" {
		return fmt.Errorf("simulated downloads can't be xz archives; register a tar.gz, tar.zst or zip: %w", system.ErrInvalid)
	}
	t, err := simFiles{m}.OpenTree(ctx, parent)
	if err != nil {
		return err
	}
	defer t.Close()
	defer t.Remove(archiveName)
	if _, err := t.Create(archiveName, bytes.NewReader(data), 0o600, system.Owner{}); err != nil {
		return err
	}
	if _, err := system.Unpack(ctx, t, system.UnpackSpec{
		Archive: archiveName, Dest: stagingName, Format: system.ArchiveFormat(a.Format), StripComponents: a.StripComponents,
	}); err != nil {
		simFiles{m}.RemoveAll(ctx, path.Join(parent, stagingName))
		return fmt.Errorf("unpacking %s: %w", a.URL, err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	staging := path.Join(parent, stagingName)
	m.files[staging].mode = fs.ModeDir | 0o755
	for p, n := range m.files {
		if isUnder(p, staging) {
			delete(m.files, p)
			m.files[a.Dest+p[len(staging):]] = n
		}
	}
	m.archives[a.Dest] = a
	return nil
}
