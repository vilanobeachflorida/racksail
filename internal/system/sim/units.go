package sim

import (
	"bufio"
	"bytes"
	"context"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/system"
)

type unitRuntime struct {
	active   string
	sub      string
	pid      int
	since    time.Time
	restarts int
	// applied are the limits set-property changed on the running unit since
	// systemd last loaded its configuration. Its drop-ins hold them too, but
	// a drop-in that sorts after them may say otherwise until the next
	// daemon-reload or reboot, which replaces these with what the unit's
	// files say.
	applied map[string]string
}

type simUnits struct{ m *Machine }

func unitPath(name string) string { return path.Join(system.UnitDir, name) }

// unitFile returns the unit's own file, or the template file for an
// instance such as site@blog.service. The caller holds m.mu.
func (m *Machine) unitFile(name string) ([]byte, bool) {
	if n, ok := m.files[unitPath(name)]; ok && !n.mode.IsDir() {
		return n.data, true
	}
	if at := strings.IndexByte(name, '@'); at > 0 {
		dot := strings.LastIndexByte(name, '.')
		if n, ok := m.files[unitPath(name[:at+1]+name[dot:])]; ok {
			return n.data, true
		}
	}
	return nil, false
}

// unitExists reports whether systemd would find the unit. The caller holds
// m.mu.
func (m *Machine) unitExists(name string) bool {
	if _, ok := m.unitFile(name); ok || m.builtin[name] {
		return true
	}
	for pkg := range m.packages {
		for _, u := range catalogEntry(pkg).units {
			if u == name {
				return true
			}
		}
	}
	return false
}

// unitDirective returns the first value of key in a unit file.
func unitDirective(content []byte, key string) string {
	sc := bufio.NewScanner(bytes.NewReader(content))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if v, ok := strings.CutPrefix(line, key+"="); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func hasInstallSection(content []byte) bool {
	return bytes.Contains(content, []byte("[Install]"))
}

func (m *Machine) unitState(name string) system.UnitState {
	st := system.UnitState{Name: name, LoadState: "not-found", ActiveState: "inactive", SubState: "dead"}
	if !m.unitExists(name) {
		return st
	}
	st.LoadState = "loaded"
	content, own := m.unitFile(name)
	if own {
		st.Description = unitDirective(content, "Description")
	}
	switch {
	case m.enabled[name]:
		st.UnitFileState = "enabled"
	case own && !hasInstallSection(content):
		st.UnitFileState = "static"
	default:
		st.UnitFileState = "disabled"
	}
	if rt, ok := m.units[name]; ok {
		st.ActiveState, st.SubState, st.MainPID = rt.active, rt.sub, rt.pid
		st.Restarts = rt.restarts
		if rt.active == "active" {
			st.ActiveSince = rt.since
			if p, ok := m.procs[rt.pid]; ok {
				st.MemoryBytes = p.RSSBytes
				st.CPUTime = p.CPUTime
				st.Tasks = 1
			}
		}
	}
	return st
}

func (u simUnits) WriteUnit(ctx context.Context, name string, content []byte) error {
	m := u.m
	if !system.ValidUnitName(name) {
		return invalid("unit name %q", name)
	}
	if err := m.lock(ctx); err != nil {
		return err
	}
	defer m.mu.Unlock()
	if err := m.record("units", "write", name); err != nil {
		return err
	}
	return m.writeFile(unitPath(name), content, 0o644, system.Owner{})
}

func (u simUnits) ReadUnit(ctx context.Context, name string) ([]byte, error) {
	m := u.m
	if err := m.lock(ctx); err != nil {
		return nil, err
	}
	defer m.mu.Unlock()
	n, ok := m.files[unitPath(name)]
	if !ok {
		return nil, notFound("unit file " + name)
	}
	return bytes.Clone(n.data), nil
}

func (u simUnits) RemoveUnit(ctx context.Context, name string) error {
	m := u.m
	if err := m.lock(ctx); err != nil {
		return err
	}
	defer m.mu.Unlock()
	if err := m.record("units", "remove", name); err != nil {
		return err
	}
	if _, ok := m.files[unitPath(name)]; !ok {
		return notFound("unit file " + name)
	}
	delete(m.files, unitPath(name))
	for _, dir := range []string{system.UnitDir, system.ControlDir, system.RuntimeControlDir} {
		m.removeTree(path.Join(dir, name+".d"))
	}
	delete(m.enabled, name)
	return nil
}

func (u simUnits) WriteDropIn(ctx context.Context, unit, name string, content []byte) error {
	m := u.m
	if !system.ValidUnitName(unit) || strings.ContainsAny(name, "/\x00") || name == "" {
		return invalid("drop-in %s for %s", name, unit)
	}
	if err := m.lock(ctx); err != nil {
		return err
	}
	defer m.mu.Unlock()
	if err := m.record("units", "write_dropin", unit+"/"+name); err != nil {
		return err
	}
	dir := unitPath(unit) + ".d"
	if _, ok := m.files[dir]; !ok {
		if err := m.mkdir(dir, 0o755, system.Owner{}); err != nil {
			return err
		}
	}
	return m.writeFile(path.Join(dir, name+".conf"), content, 0o644, system.Owner{})
}

func (u simUnits) RemoveDropIn(ctx context.Context, unit, name string) error {
	m := u.m
	if err := m.lock(ctx); err != nil {
		return err
	}
	defer m.mu.Unlock()
	if err := m.record("units", "remove_dropin", unit+"/"+name); err != nil {
		return err
	}
	p := path.Join(unitPath(unit)+".d", name+".conf")
	if _, ok := m.files[p]; !ok {
		return notFound("drop-in " + p)
	}
	delete(m.files, p)
	return nil
}

func (u simUnits) DaemonReload(ctx context.Context) error {
	m := u.m
	m.delay(ctx, opDaemon, 0)
	if err := m.lock(ctx); err != nil {
		return err
	}
	defer m.mu.Unlock()
	if err := m.record("units", "daemon_reload", ""); err != nil {
		return err
	}
	m.reloads++
	// Every unit's configuration is read again, so its limits are what its
	// files say from now on.
	for _, rt := range m.units {
		rt.applied = nil
	}
	return nil
}

func (u simUnits) Start(ctx context.Context, name string) error {
	m := u.m
	m.delay(ctx, opStart, 0)
	if err := m.lock(ctx); err != nil {
		return err
	}
	defer m.mu.Unlock()
	if err := m.record("units", "start", name); err != nil {
		return err
	}
	return m.startUnit(name)
}

// startUnit starts a unit that isn't running. The caller holds m.mu.
func (m *Machine) startUnit(name string) error {
	if !m.unitExists(name) {
		return notFound("unit " + name)
	}
	rt := m.units[name]
	if rt == nil {
		rt = &unitRuntime{}
		m.units[name] = rt
	}
	if rt.active == "active" {
		return nil
	}
	rt.active, rt.since = "active", m.now()
	desc := m.describe(name)
	switch path.Ext(name) {
	case ".service":
		content, _ := m.unitFile(name)
		m.logSystemd(name, system.PriorityInfo, "Starting %s...", desc)
		if unitDirective(content, "Type") == "oneshot" {
			rt.sub, rt.pid = "exited", 0
			if unitDirective(content, "RemainAfterExit") != "yes" {
				rt.active, rt.sub = "inactive", "dead"
			}
			m.logSystemd(name, system.PriorityInfo, "Finished %s.", desc)
			return nil
		}
		rt.sub = "running"
		rt.pid = m.spawn(name, content)
		m.logSystemd(name, system.PriorityInfo, "Started %s.", desc)
	case ".socket":
		rt.sub = "listening"
		m.logSystemd(name, system.PriorityInfo, "Listening on %s.", desc)
	case ".timer", ".path":
		rt.sub = "waiting"
		m.logSystemd(name, system.PriorityInfo, "Started %s.", desc)
	case ".mount":
		rt.sub = "mounted"
		m.logSystemd(name, system.PriorityInfo, "Mounted %s.", desc)
	default:
		rt.sub = "active"
		m.logSystemd(name, system.PriorityInfo, "Reached %s.", desc)
	}
	return nil
}

// spawn creates the main process of a service. The caller holds m.mu.
func (m *Machine) spawn(unit string, content []byte) int {
	user := unitDirective(content, "User")
	if user == "" {
		user = "root"
	}
	uid := 0
	if u, ok := m.users[user]; ok {
		uid = u.UID
	}
	command := strings.TrimSuffix(unit, ".service")
	var args []string
	if exec := strings.Fields(strings.TrimLeft(unitDirective(content, "ExecStart"), "-@:+!")); len(exec) > 0 {
		command = path.Base(exec[0])
		args = exec
	}
	m.nextPID++
	pid := m.nextPID
	m.procs[pid] = &system.Process{
		PID: pid, PPID: 1, User: user, UID: uid, Command: command, Args: args, State: "S",
		RSSBytes: int64(16<<20) + int64(hash(unit)%(48<<20)),
		Started:  m.now(), Unit: unit,
	}
	return pid
}

// stopUnit stops a unit and its process. The caller holds m.mu.
func (m *Machine) stopUnit(name string) {
	rt, ok := m.units[name]
	if !ok {
		return
	}
	wasActive := rt.active == "active"
	if wasActive {
		m.logSystemd(name, system.PriorityInfo, "Stopping %s...", m.describe(name))
	}
	if rt.pid != 0 {
		delete(m.procs, rt.pid)
	}
	rt.active, rt.sub, rt.pid = "inactive", "dead", 0
	if wasActive {
		m.logSystemd(name, system.PriorityInfo, "Stopped %s.", m.describe(name))
	}
}

func (u simUnits) Stop(ctx context.Context, name string) error {
	m := u.m
	m.delay(ctx, opStop, 0)
	if err := m.lock(ctx); err != nil {
		return err
	}
	defer m.mu.Unlock()
	if err := m.record("units", "stop", name); err != nil {
		return err
	}
	if !m.unitExists(name) {
		return notFound("unit " + name)
	}
	m.stopUnit(name)
	return nil
}

func (u simUnits) Restart(ctx context.Context, name string) error {
	m := u.m
	m.delay(ctx, opRestart, 0)
	if err := m.lock(ctx); err != nil {
		return err
	}
	defer m.mu.Unlock()
	if err := m.record("units", "restart", name); err != nil {
		return err
	}
	if !m.unitExists(name) {
		return notFound("unit " + name)
	}
	m.stopUnit(name)
	return m.startUnit(name)
}

func (u simUnits) Reload(ctx context.Context, name string) error {
	m := u.m
	m.delay(ctx, opReload, 0)
	if err := m.lock(ctx); err != nil {
		return err
	}
	defer m.mu.Unlock()
	if err := m.record("units", "reload", name); err != nil {
		return err
	}
	rt, ok := m.units[name]
	if !ok || rt.active != "active" {
		return invalid("unit %s is not active, so it can't be reloaded", name)
	}
	m.logSystemd(name, system.PriorityInfo, "Reloading %s...", m.describe(name))
	m.logSystemd(name, system.PriorityInfo, "Reloaded %s.", m.describe(name))
	return nil
}

func (u simUnits) Enable(ctx context.Context, names ...string) error {
	m := u.m
	if err := m.lock(ctx); err != nil {
		return err
	}
	defer m.mu.Unlock()
	for _, name := range names {
		if err := m.record("units", "enable", name); err != nil {
			return err
		}
		if !m.unitExists(name) {
			return notFound("unit " + name)
		}
		m.enabled[name] = true
	}
	return nil
}

func (u simUnits) Disable(ctx context.Context, names ...string) error {
	m := u.m
	if err := m.lock(ctx); err != nil {
		return err
	}
	defer m.mu.Unlock()
	for _, name := range names {
		if err := m.record("units", "disable", name); err != nil {
			return err
		}
		delete(m.enabled, name)
	}
	return nil
}

func (u simUnits) Status(ctx context.Context, name string) (system.UnitState, error) {
	m := u.m
	if err := m.lock(ctx); err != nil {
		return system.UnitState{}, err
	}
	defer m.mu.Unlock()
	return m.unitState(name), nil
}

func (u simUnits) List(ctx context.Context, patterns ...string) ([]system.UnitState, error) {
	m := u.m
	if err := m.lock(ctx); err != nil {
		return nil, err
	}
	defer m.mu.Unlock()
	names := map[string]bool{}
	for n := range m.builtin {
		names[n] = true
	}
	for n := range m.units {
		names[n] = true
	}
	for p, n := range m.files {
		if path.Dir(p) == system.UnitDir && !n.mode.IsDir() && !strings.Contains(path.Base(p), "@.") {
			names[path.Base(p)] = true
		}
	}
	for pkg := range m.packages {
		for _, u := range catalogEntry(pkg).units {
			names[u] = true
		}
	}
	var out []system.UnitState
	for n := range names {
		if matchesAny(n, patterns) {
			out = append(out, m.unitState(n))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func matchesAny(name string, patterns []string) bool {
	if len(patterns) == 0 {
		return true
	}
	for _, p := range patterns {
		if ok, _ := path.Match(p, name); ok {
			return true
		}
	}
	return false
}

var (
	propertyName  = regexp.MustCompile(`^[A-Z][A-Za-z]{1,63}$`)
	propertyValue = regexp.MustCompile(`^[^\n\r\x00\\]{0,128}$`)
)

// setPropertyHeader starts the drop-ins set-property writes, as systemd's.
const setPropertyHeader = "# This is a drop-in unit file extension, created via \"systemctl set-property\"\n# or an equivalent operation. Do not edit.\n"

// SetProperties does what systemctl set-property does: it changes the
// running unit's limits and keeps each in a drop-in of its own,
// 50-<property>.conf under system.control, in /etc or, for a runtime
// change, in /run, which a reboot empties.
func (u simUnits) SetProperties(ctx context.Context, name string, runtime bool, props ...system.UnitProperty) error {
	m := u.m
	if err := m.lock(ctx); err != nil {
		return err
	}
	defer m.mu.Unlock()
	if err := m.record("units", "set_properties", name); err != nil {
		return err
	}
	if !m.unitExists(name) {
		return notFound("unit " + name)
	}
	section, ok := system.ResourceControlSection(name)
	if !ok {
		return invalid("unit %s has no resource limits", name)
	}
	for _, p := range props {
		if !propertyName.MatchString(p.Name) || !propertyValue.MatchString(p.Value) {
			return invalid("unit property %s=%q", p.Name, p.Value)
		}
	}
	dir := system.ControlDir
	if runtime {
		dir = system.RuntimeControlDir
	}
	dir = path.Join(dir, name+".d")
	if err := m.mkdirs(dir); err != nil {
		return err
	}
	rt := m.units[name]
	if rt == nil {
		rt = &unitRuntime{active: "inactive", sub: "dead"}
		m.units[name] = rt
	}
	if rt.applied == nil {
		rt.applied = map[string]string{}
	}
	for _, p := range props {
		content := setPropertyHeader + "[" + section + "]\n" + p.Name + "=" + p.Value + "\n"
		if err := m.writeFile(path.Join(dir, "50-"+p.Name+".conf"), []byte(content), 0o644, system.Owner{}); err != nil {
			return err
		}
		rt.applied[p.Name] = p.Value
	}
	return nil
}

// mkdirs creates a directory and its missing parents for root. The caller
// holds m.mu.
func (m *Machine) mkdirs(dir string) error {
	if n, ok := m.files[dir]; ok {
		if !n.mode.IsDir() {
			return invalid("%s is not a directory", dir)
		}
		return nil
	}
	if parent := path.Dir(dir); parent != dir {
		if err := m.mkdirs(parent); err != nil {
			return err
		}
	}
	return m.mkdir(dir, 0o755, system.Owner{})
}

// ResourceControl returns the unit's limits, each reduced to its base-unit
// form, from its configuration as systemd loads it (system.LoadUnitConfig):
// with runtime, from every directory, /run included, and with the limits
// set-property applied since the last reload on top; without, from the
// directories a reboot leaves, so a value only a runtime change set isn't
// counted as kept.
func (u simUnits) ResourceControl(ctx context.Context, name string, runtime bool) (map[string]string, error) {
	m := u.m
	if err := m.lock(ctx); err != nil {
		return nil, err
	}
	defer m.mu.Unlock()
	if !m.unitExists(name) {
		return map[string]string{}, nil
	}
	dirs := system.PersistentUnitSearchPath()
	if runtime {
		dirs = system.UnitSearchPath
	}
	files, err := system.LoadUnitConfig(name, dirs, configTree{m})
	if err != nil {
		return nil, err
	}
	limits, err := system.UnitResourceLimits(name, files)
	if err != nil {
		return nil, err
	}
	if rt, ok := m.units[name]; ok && runtime {
		for k, v := range rt.applied {
			if canon, ok := system.CanonicalLimit(k, v); ok {
				limits[k] = canon
			} else {
				delete(limits, k)
			}
		}
	}
	return limits, nil
}

// configTree is the machine's files as systemd reads unit configuration
// from them. The caller holds m.mu.
type configTree struct{ m *Machine }

func (t configTree) Lstat(p string) (system.FileInfo, error) {
	n, ok := t.m.files[p]
	if !ok {
		return system.FileInfo{}, notFound(p)
	}
	return t.m.info(p, n), nil
}

func (t configTree) ReadDir(p string) ([]system.FileInfo, error) {
	n, ok := t.m.files[p]
	if !ok {
		return nil, notFound(p)
	}
	if !n.mode.IsDir() {
		return nil, invalid("%s is not a directory", p)
	}
	var out []system.FileInfo
	for child, cn := range t.m.files {
		if child != p && path.Dir(child) == p {
			out = append(out, t.m.info(child, cn))
		}
	}
	return out, nil
}

func (t configTree) ReadFile(p string) ([]byte, error) {
	n, ok := t.m.files[p]
	switch {
	case !ok:
		return nil, notFound(p)
	case n.mode.IsDir():
		return nil, invalid("%s is a directory", p)
	case n.mode&fs.ModeSymlink != 0:
		return nil, invalid("%s is a symbolic link", p)
	}
	return bytes.Clone(n.data), nil
}

// Property returns the value a persistent set-property kept for a unit,
// for tests.
func (m *Machine) Property(unit, name string) (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n, ok := m.files[path.Join(system.ControlDir, unit+".d", "50-"+name+".conf")]
	if !ok {
		return "", false
	}
	u, err := system.ParseUnitFile(n.data)
	if err != nil {
		return "", false
	}
	for _, s := range u.Sections {
		for _, e := range s.Entries {
			if e.Key == name {
				return e.Value, true
			}
		}
	}
	return "", false
}

func (u simUnits) Kill(ctx context.Context, name string, signal system.Signal) error {
	m := u.m
	if !signal.Valid() {
		return invalid("signal %q", signal)
	}
	if err := m.lock(ctx); err != nil {
		return err
	}
	defer m.mu.Unlock()
	if err := m.record("units", "kill", name); err != nil {
		return err
	}
	rt, ok := m.units[name]
	if !ok || rt.pid == 0 {
		return invalid("unit %s has no running process", name)
	}
	m.signal(rt.pid, signal)
	return nil
}

func (u simUnits) ResetFailed(ctx context.Context, name string) error {
	m := u.m
	if err := m.lock(ctx); err != nil {
		return err
	}
	defer m.mu.Unlock()
	if err := m.record("units", "reset_failed", name); err != nil {
		return err
	}
	if rt, ok := m.units[name]; ok && rt.active == "failed" {
		rt.active, rt.sub = "inactive", "dead"
	}
	return nil
}

// signal delivers a signal to a simulated process. The caller holds m.mu.
func (m *Machine) signal(pid int, sig system.Signal) {
	p, ok := m.procs[pid]
	if !ok {
		return
	}
	switch sig {
	case system.SIGTERM, system.SIGINT, system.SIGKILL:
		delete(m.procs, pid)
		if rt, ok := m.units[p.Unit]; ok && rt.pid == pid {
			rt.pid = 0
			if sig == system.SIGKILL {
				rt.active, rt.sub = "failed", "failed"
				m.logSystemd(p.Unit, system.PriorityNotice, "%s: Main process exited, code=killed, status=9/KILL", p.Unit)
				m.logSystemd(p.Unit, system.PriorityWarning, "%s: Failed with result 'signal'.", p.Unit)
			} else {
				rt.active, rt.sub = "inactive", "dead"
				m.logSystemd(p.Unit, system.PriorityInfo, "Stopped %s.", m.describe(p.Unit))
			}
		}
	}
}

// CrashUnit makes a running service fail, as if its process died, so tests
// can exercise restart and drift handling.
func (m *Machine) CrashUnit(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if rt, ok := m.units[name]; ok && rt.pid != 0 {
		m.signal(rt.pid, system.SIGKILL)
		m.changed()
	}
}

func (m *Machine) seedUnitsAndPackages(now time.Time) {
	for _, name := range []string{"base-files", "systemd", "systemd-sysv", "systemd-timesyncd", "openssh-server", "openssh-client",
		"btrfs-progs", "ca-certificates", "sudo", "cron", kernelPackage[m.facts.Arch]} {
		e := catalogEntry(name)
		m.packages[name] = e.pkg(name, m.facts.Arch)
	}
	release := kernelRelease(catalogEntry(kernelPackage[m.facts.Arch]).version, m.facts.Arch)
	m.files["/boot/vmlinuz-"+release] = &node{mode: 0o644, modTime: now.Add(-72 * time.Hour), data: []byte("simulated kernel " + release)}
	for _, name := range []string{
		"racksail.slice", "racksail-accounts.slice", "racksail-panel.service", "racksail-panel.socket", "racksail-agent.service",
		"racksail-edge.service", "racksail-edge.socket", "systemd-networkd.service", "systemd-journald.service",
		"systemd-timesyncd.service", "ssh.service", "cron.service", "multi-user.target",
	} {
		m.builtin[name] = true
	}
	for _, name := range []string{
		"racksail.slice", "racksail-accounts.slice", "racksail-panel.socket", "racksail-panel.service", "racksail-agent.service",
		"racksail-edge.socket", "systemd-networkd.service", "systemd-journald.service", "systemd-timesyncd.service",
		"ssh.service", "cron.service",
	} {
		if err := m.startUnit(name); err == nil && path.Ext(name) != ".slice" {
			m.enabled[name] = true
		}
	}
	if p, ok := m.procs[m.units["racksail-panel.service"].pid]; ok {
		p.User, p.UID = "racksail", 990
		p.Command, p.Args = "racksail", []string{"/usr/bin/racksail", "panel"}
	}
	if p, ok := m.procs[m.units["racksail-agent.service"].pid]; ok {
		p.Command, p.Args = "racksail", []string{"/usr/bin/racksail", "agent"}
	}
	m.procs[1] = &system.Process{PID: 1, User: "root", Command: "systemd", Args: []string{"/sbin/init"}, State: "S", RSSBytes: 12 << 20, Started: now.Add(-72 * time.Hour)}
}
