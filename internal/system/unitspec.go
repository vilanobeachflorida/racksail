package system

import (
	"fmt"
	"io/fs"
	"maps"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// QuoteExec renders an argument vector as a systemd command line. Every
// argument reaches the program exactly as given: specifiers (%) and
// variable expansion ($) are escaped, and arguments that need it are
// quoted. Control characters are refused.
func QuoteExec(argv []string) (string, error) {
	if len(argv) == 0 {
		return "", fmt.Errorf("empty command: %w", ErrInvalid)
	}
	if !path.IsAbs(argv[0]) || path.Clean(argv[0]) != argv[0] {
		return "", fmt.Errorf("program %q must be an absolute path: %w", argv[0], ErrInvalid)
	}
	parts := make([]string, len(argv))
	for i, a := range argv {
		q, err := quoteArg(a, i == 0)
		if err != nil {
			return "", err
		}
		parts[i] = q
	}
	return strings.Join(parts, " "), nil
}

var plainArg = regexp.MustCompile(`^[A-Za-z0-9_@+=:,./-]+$`)

func quoteArg(a string, program bool) (string, error) {
	for _, r := range a {
		if r < 0x20 || r == 0x7f {
			return "", fmt.Errorf("argument %q has control characters: %w", a, ErrInvalid)
		}
	}
	escaped := strings.NewReplacer("%", "%%", "$", "$$").Replace(a)
	// A leading @, -, :, + or ! on the program changes how systemd runs it.
	if plainArg.MatchString(a) && !(program && strings.ContainsAny(a[:1], "@-:+!")) {
		return escaped, nil
	}
	escaped = strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(escaped)
	return `"` + escaped + `"`, nil
}

var envKeyPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)

// quoteEnvironment renders one Environment= assignment.
func quoteEnvironment(key, value string) (string, error) {
	if !envKeyPattern.MatchString(key) {
		return "", fmt.Errorf("environment variable %q: %w", key, ErrInvalid)
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return "", fmt.Errorf("value of %s has control characters: %w", key, ErrInvalid)
		}
	}
	v := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "%", "%%").Replace(key + "=" + value)
	return `"` + v + `"`, nil
}

// Seconds renders a duration as systemd reads it.
func Seconds(d time.Duration) string {
	if d%time.Second == 0 {
		return strconv.FormatInt(int64(d/time.Second), 10) + "s"
	}
	return strconv.FormatInt(int64(d/time.Millisecond), 10) + "ms"
}

// ResourceLimits are cgroup limits for a slice or a service, as plans set
// them for accounts. Zero values are left out.
type ResourceLimits struct {
	MemoryHighBytes    int64 `json:"memoryHighBytes,omitempty"`
	MemoryMaxBytes     int64 `json:"memoryMaxBytes,omitempty"`
	MemorySwapMaxBytes int64 `json:"memorySwapMaxBytes,omitempty"`
	// CPUQuotaPercent is a share of one CPU: 200 is two whole CPUs.
	CPUQuotaPercent int             `json:"cpuQuotaPercent,omitempty"`
	CPUWeight       int             `json:"cpuWeight,omitempty"`
	IOWeight        int             `json:"ioWeight,omitempty"`
	TasksMax        int             `json:"tasksMax,omitempty"`
	IODevices       []IODeviceLimit `json:"ioDevices,omitempty"`
}

// IODeviceLimit caps the bandwidth and operations of one block device.
type IODeviceLimit struct {
	Device        string `json:"device" doc:"Block device, such as /dev/nvme0n1"`
	ReadBytesSec  int64  `json:"readBytesSec,omitempty"`
	WriteBytesSec int64  `json:"writeBytesSec,omitempty"`
	ReadIOPS      int64  `json:"readIops,omitempty"`
	WriteIOPS     int64  `json:"writeIops,omitempty"`
}

var blockDevicePattern = regexp.MustCompile(`^/dev/[A-Za-z0-9][A-Za-z0-9/_.:-]*$`)

// Entries returns the directives for the limits.
func (l ResourceLimits) Entries() ([]UnitEntry, error) {
	var e []UnitEntry
	for _, v := range []struct {
		key string
		n   int64
	}{{"MemoryHigh", l.MemoryHighBytes}, {"MemoryMax", l.MemoryMaxBytes}, {"MemorySwapMax", l.MemorySwapMaxBytes}} {
		if v.n < 0 {
			return nil, fmt.Errorf("%s %d: %w", v.key, v.n, ErrInvalid)
		}
		if v.n > 0 {
			e = append(e, UnitEntry{v.key, strconv.FormatInt(v.n, 10)})
		}
	}
	if l.MemoryHighBytes > 0 && l.MemoryMaxBytes > 0 && l.MemoryHighBytes > l.MemoryMaxBytes {
		return nil, fmt.Errorf("MemoryHigh is above MemoryMax: %w", ErrInvalid)
	}
	if l.CPUQuotaPercent < 0 || l.CPUQuotaPercent > 100*4096 {
		return nil, fmt.Errorf("CPU quota %d%%: %w", l.CPUQuotaPercent, ErrInvalid)
	}
	if l.CPUQuotaPercent > 0 {
		e = append(e, UnitEntry{"CPUQuota", strconv.Itoa(l.CPUQuotaPercent) + "%"})
	}
	for _, w := range []struct {
		key string
		n   int
	}{{"CPUWeight", l.CPUWeight}, {"IOWeight", l.IOWeight}} {
		if w.n < 0 || w.n > 10000 {
			return nil, fmt.Errorf("%s %d must be between 1 and 10000: %w", w.key, w.n, ErrInvalid)
		}
		if w.n > 0 {
			e = append(e, UnitEntry{w.key, strconv.Itoa(w.n)})
		}
	}
	if l.TasksMax < 0 {
		return nil, fmt.Errorf("TasksMax %d: %w", l.TasksMax, ErrInvalid)
	}
	if l.TasksMax > 0 {
		e = append(e, UnitEntry{"TasksMax", strconv.Itoa(l.TasksMax)})
	}
	for _, d := range l.IODevices {
		if !blockDevicePattern.MatchString(d.Device) || strings.Contains(d.Device, "..") {
			return nil, fmt.Errorf("I/O limit device %q: %w", d.Device, ErrInvalid)
		}
		for _, v := range []struct {
			key string
			n   int64
		}{{"IOReadBandwidthMax", d.ReadBytesSec}, {"IOWriteBandwidthMax", d.WriteBytesSec}, {"IOReadIOPSMax", d.ReadIOPS}, {"IOWriteIOPSMax", d.WriteIOPS}} {
			if v.n < 0 {
				return nil, fmt.Errorf("%s %d: %w", v.key, v.n, ErrInvalid)
			}
			if v.n > 0 {
				e = append(e, UnitEntry{v.key, d.Device + " " + strconv.FormatInt(v.n, 10)})
			}
		}
	}
	return e, nil
}

// accounting turns on the counters usage accounting reads.
var accounting = []UnitEntry{
	{"CPUAccounting", "yes"}, {"MemoryAccounting", "yes"}, {"IOAccounting", "yes"}, {"TasksAccounting", "yes"},
}

func checkUnitNames(names []string) error {
	for _, n := range names {
		if !ValidUnitName(n) {
			return fmt.Errorf("unit name %q: %w", n, ErrInvalid)
		}
	}
	return nil
}

func checkText(field, s string) error {
	if strings.ContainsAny(s, "\r\n\x00") || strings.HasSuffix(s, `\`) || len(s) > 256 {
		return fmt.Errorf("%s %q: %w", field, s, ErrInvalid)
	}
	return nil
}

// unitHeader adds a [Unit] section with a description and dependencies.
func unitHeader(u *UnitFile, description string, deps map[string][]string) error {
	if err := checkText("description", description); err != nil {
		return err
	}
	s := u.Section("Unit").Add("Description", description)
	for _, key := range []string{"Documentation", "Requires", "Wants", "BindsTo", "PartOf", "After", "Before", "Conflicts"} {
		values := deps[key]
		if key != "Documentation" {
			if err := checkUnitNames(values); err != nil {
				return err
			}
		}
		if len(values) > 0 {
			s.Add(key, strings.Join(values, " "))
		}
	}
	return nil
}

func installSection(u *UnitFile, wantedBy []string) error {
	if err := checkUnitNames(wantedBy); err != nil {
		return err
	}
	if len(wantedBy) > 0 {
		u.Section("Install").Add("WantedBy", strings.Join(wantedBy, " "))
	}
	return nil
}

// ServiceSpec describes a service unit. Account workloads always have a
// Sandbox; platform services Racksail ships use PlatformUnits instead.
type ServiceSpec struct {
	Description string
	After       []string
	Wants       []string
	Requires    []string
	BindsTo     []string
	PartOf      []string
	// Type is simple, exec, notify, oneshot or forking; exec by default.
	Type string
	// ExecStart is the program and its arguments, never a shell line.
	ExecStart    []string
	ExecStartPre [][]string
	ExecReload   []string
	// ReloadSignal reloads by sending the main process a signal, such as
	// HUP, instead of running ExecReload.
	ReloadSignal Signal
	// RuntimeDirectory is created under /run for the service, owned by its
	// user, such as racksail-sites/blog for a site's socket.
	RuntimeDirectory string
	// Environment is set for the program; EnvironmentFiles are read by
	// systemd, so secrets stay out of the unit file.
	Environment      map[string]string
	EnvironmentFiles []string
	WorkingDirectory string
	Slice            string
	// Restart is no, on-failure or always.
	Restart          string
	RestartDelay     time.Duration
	StopTimeout      time.Duration
	RemainAfterExit  bool
	StopWhenUnneeded bool
	// LogNamespace sends the service's logs to a journal namespace of its
	// own, such as its account's.
	LogNamespace     string
	SyslogIdentifier string
	Limits           ResourceLimits
	Sandbox          *Sandbox
	WantedBy         []string
}

var (
	serviceTypes      = []string{"simple", "exec", "notify", "oneshot", "forking"}
	restartValues     = []string{"no", "on-failure", "on-abnormal", "always"}
	identPattern      = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)
	userNamePattern   = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)
	runtimeDirPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,63}(/[a-z0-9][a-z0-9_.-]{0,63}){0,3}$`)
)

// Render returns the unit file.
func (s ServiceSpec) Render() ([]byte, error) {
	u, err := s.Unit()
	if err != nil {
		return nil, err
	}
	return u.Render()
}

// Unit builds the unit file without rendering it, for callers that add
// sections of their own.
func (s ServiceSpec) Unit() (UnitFile, error) {
	var u UnitFile
	deps := map[string][]string{"Requires": s.Requires, "Wants": s.Wants, "BindsTo": s.BindsTo, "PartOf": s.PartOf, "After": s.After}
	if err := unitHeader(&u, s.Description, deps); err != nil {
		return u, err
	}
	if s.StopWhenUnneeded {
		u.Section("Unit").Add("StopWhenUnneeded", "yes")
	}
	svc := u.Section("Service")
	typ := s.Type
	if typ == "" {
		typ = "exec"
	}
	if !slices.Contains(serviceTypes, typ) {
		return u, fmt.Errorf("service type %q: %w", typ, ErrInvalid)
	}
	svc.Add("Type", typ)
	if s.Slice != "" {
		if !ValidUnitName(s.Slice) || !strings.HasSuffix(s.Slice, ".slice") {
			return u, fmt.Errorf("slice %q: %w", s.Slice, ErrInvalid)
		}
		svc.Add("Slice", s.Slice)
	}
	for _, pre := range s.ExecStartPre {
		line, err := QuoteExec(pre)
		if err != nil {
			return u, err
		}
		svc.Add("ExecStartPre", line)
	}
	start, err := QuoteExec(s.ExecStart)
	if err != nil {
		return u, err
	}
	svc.Add("ExecStart", start)
	switch {
	case len(s.ExecReload) > 0 && s.ReloadSignal != "":
		return u, fmt.Errorf("a service reloads with a program or a signal, not both: %w", ErrInvalid)
	case len(s.ExecReload) > 0:
		reload, err := QuoteExec(s.ExecReload)
		if err != nil {
			return u, err
		}
		svc.Add("ExecReload", reload)
	case s.ReloadSignal != "":
		if !s.ReloadSignal.Valid() {
			return u, fmt.Errorf("reload signal %q: %w", s.ReloadSignal, ErrInvalid)
		}
		// $MAINPID is the one variable this unit expands on purpose.
		svc.Add("ExecReload", "/usr/bin/kill -s "+string(s.ReloadSignal)+" $MAINPID")
	}
	if s.RuntimeDirectory != "" {
		if !runtimeDirPattern.MatchString(s.RuntimeDirectory) || strings.Contains(s.RuntimeDirectory, "..") {
			return u, fmt.Errorf("runtime directory %q: %w", s.RuntimeDirectory, ErrInvalid)
		}
		svc.Add("RuntimeDirectory", s.RuntimeDirectory).Add("RuntimeDirectoryMode", "0750")
	}
	if s.WorkingDirectory != "" {
		if !path.IsAbs(s.WorkingDirectory) || strings.ContainsAny(s.WorkingDirectory, " \t\r\n%$") {
			return u, fmt.Errorf("working directory %q: %w", s.WorkingDirectory, ErrInvalid)
		}
		svc.Add("WorkingDirectory", s.WorkingDirectory)
	}
	for _, k := range slices.Sorted(maps.Keys(s.Environment)) {
		line, err := quoteEnvironment(k, s.Environment[k])
		if err != nil {
			return u, err
		}
		svc.Add("Environment", line)
	}
	for _, f := range s.EnvironmentFiles {
		if !path.IsAbs(f) || path.Clean(f) != f || strings.ContainsAny(f, " \t\r\n%$") {
			return u, fmt.Errorf("environment file %q: %w", f, ErrInvalid)
		}
		svc.Add("EnvironmentFile", f)
	}
	if s.Restart != "" {
		if !slices.Contains(restartValues, s.Restart) {
			return u, fmt.Errorf("restart %q: %w", s.Restart, ErrInvalid)
		}
		svc.Add("Restart", s.Restart)
	}
	if s.RestartDelay > 0 {
		svc.Add("RestartSec", Seconds(s.RestartDelay))
	}
	if s.StopTimeout > 0 {
		svc.Add("TimeoutStopSec", Seconds(s.StopTimeout))
	}
	if s.RemainAfterExit {
		svc.Add("RemainAfterExit", "yes")
	}
	if s.LogNamespace != "" {
		if !namespacePattern.MatchString(s.LogNamespace) {
			return u, fmt.Errorf("log namespace %q: %w", s.LogNamespace, ErrInvalid)
		}
		svc.Add("LogNamespace", s.LogNamespace)
	}
	if s.SyslogIdentifier != "" {
		if !identPattern.MatchString(s.SyslogIdentifier) {
			return u, fmt.Errorf("syslog identifier %q: %w", s.SyslogIdentifier, ErrInvalid)
		}
		svc.Add("SyslogIdentifier", s.SyslogIdentifier)
	}
	limits, err := s.Limits.Entries()
	if err != nil {
		return u, err
	}
	svc.AddEntries(limits...)
	if s.Sandbox != nil {
		sandbox, err := s.Sandbox.Entries()
		if err != nil {
			return u, err
		}
		svc.AddEntries(sandbox...)
	}
	return u, installSection(&u, s.WantedBy)
}

// SocketSpec describes a socket unit, such as the one systemd holds for a
// sleeping site so the first request wakes it.
type SocketSpec struct {
	Description string
	// ListenStream are Unix socket paths or addresses such as
	// 127.0.0.1:8080.
	ListenStream []string
	SocketUser   string
	SocketGroup  string
	SocketMode   fs.FileMode
	// Service is the unit the socket starts; the one with the socket's
	// name by default.
	Service      string
	RemoveOnStop bool
	Backlog      int
	PartOf       []string
	WantedBy     []string
}

var addressPattern = regexp.MustCompile(`^(\[[0-9a-fA-F:.]+\]|[0-9.]+)?:?[0-9]{1,5}$`)

// Render returns the unit file.
func (s SocketSpec) Render() ([]byte, error) {
	var u UnitFile
	if err := unitHeader(&u, s.Description, map[string][]string{"PartOf": s.PartOf}); err != nil {
		return nil, err
	}
	if len(s.ListenStream) == 0 {
		return nil, fmt.Errorf("a socket needs something to listen on: %w", ErrInvalid)
	}
	sock := u.Section("Socket")
	for _, l := range s.ListenStream {
		switch {
		case path.IsAbs(l) && path.Clean(l) == l && !strings.ContainsAny(l, " \t\r\n%$"):
		case addressPattern.MatchString(l):
		default:
			return nil, fmt.Errorf("listen address %q: %w", l, ErrInvalid)
		}
		sock.Add("ListenStream", l)
	}
	for _, owner := range []struct{ key, value string }{{"SocketUser", s.SocketUser}, {"SocketGroup", s.SocketGroup}} {
		if owner.value == "" {
			continue
		}
		if !userNamePattern.MatchString(owner.value) {
			return nil, fmt.Errorf("%s %q: %w", owner.key, owner.value, ErrInvalid)
		}
		sock.Add(owner.key, owner.value)
	}
	if s.SocketMode != 0 {
		if s.SocketMode&^0o777 != 0 {
			return nil, fmt.Errorf("socket mode %o: %w", s.SocketMode, ErrInvalid)
		}
		sock.Add("SocketMode", fmt.Sprintf("%04o", uint32(s.SocketMode)))
	}
	if s.Service != "" {
		if !ValidUnitName(s.Service) || !strings.HasSuffix(s.Service, ".service") {
			return nil, fmt.Errorf("service %q: %w", s.Service, ErrInvalid)
		}
		sock.Add("Service", s.Service)
	}
	if s.RemoveOnStop {
		sock.Add("RemoveOnStop", "yes")
	}
	if s.Backlog < 0 {
		return nil, fmt.Errorf("backlog %d: %w", s.Backlog, ErrInvalid)
	}
	if s.Backlog > 0 {
		sock.Add("Backlog", strconv.Itoa(s.Backlog))
	}
	if err := installSection(&u, s.WantedBy); err != nil {
		return nil, err
	}
	return u.Render()
}

// TimerSpec describes a timer unit, such as a site's scheduled job.
type TimerSpec struct {
	Description string
	// OnCalendar are calendar events in systemd's syntax, such as
	// "*-*-* 03:30:00" or "Mon..Fri 08:00".
	OnCalendar      []string
	OnBoot          time.Duration
	OnUnitActive    time.Duration
	RandomizedDelay time.Duration
	Accuracy        time.Duration
	// Persistent runs a missed event at the next boot.
	Persistent bool
	// Unit is the service the timer starts; the one with the timer's name
	// by default.
	Unit     string
	WantedBy []string
}

var calendarPattern = regexp.MustCompile(`^[A-Za-z0-9*:.,/~ -]{1,128}$`)

// Render returns the unit file.
func (t TimerSpec) Render() ([]byte, error) {
	var u UnitFile
	if err := unitHeader(&u, t.Description, nil); err != nil {
		return nil, err
	}
	timer := u.Section("Timer")
	if len(t.OnCalendar) == 0 && t.OnBoot == 0 && t.OnUnitActive == 0 {
		return nil, fmt.Errorf("a timer needs a schedule: %w", ErrInvalid)
	}
	for _, c := range t.OnCalendar {
		if !calendarPattern.MatchString(c) {
			return nil, fmt.Errorf("calendar event %q: %w", c, ErrInvalid)
		}
		timer.Add("OnCalendar", c)
	}
	for _, d := range []struct {
		key string
		v   time.Duration
	}{{"OnBootSec", t.OnBoot}, {"OnUnitActiveSec", t.OnUnitActive}, {"RandomizedDelaySec", t.RandomizedDelay}, {"AccuracySec", t.Accuracy}} {
		if d.v < 0 {
			return nil, fmt.Errorf("%s: %w", d.key, ErrInvalid)
		}
		if d.v > 0 {
			timer.Add(d.key, Seconds(d.v))
		}
	}
	if t.Persistent {
		timer.Add("Persistent", "yes")
	}
	if t.Unit != "" {
		if !ValidUnitName(t.Unit) {
			return nil, fmt.Errorf("timer unit %q: %w", t.Unit, ErrInvalid)
		}
		timer.Add("Unit", t.Unit)
	}
	if err := installSection(&u, t.WantedBy); err != nil {
		return nil, err
	}
	return u.Render()
}

// SliceSpec describes a slice, such as an account's.
type SliceSpec struct {
	Description string
	Limits      ResourceLimits
}

// Render returns the unit file.
func (s SliceSpec) Render() ([]byte, error) {
	var u UnitFile
	if err := unitHeader(&u, s.Description, map[string][]string{"Before": {"slices.target"}}); err != nil {
		return nil, err
	}
	limits, err := s.Limits.Entries()
	if err != nil {
		return nil, err
	}
	u.Section("Slice").AddEntries(accounting...).AddEntries(limits...)
	return u.Render()
}

// AccountSliceSpec is the slice of an account with its plan's limits. It
// lives under racksail-accounts.slice because of its name.
func AccountSliceSpec(slug string, limits ResourceLimits) SliceSpec {
	return SliceSpec{Description: "Racksail account " + slug, Limits: limits}
}
