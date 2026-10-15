package linux

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/system"
)

const clockTicks = 100 // USER_HZ on every architecture Debian supports

type sysctl struct{ h *host }

func sysctlPath(key string) (string, error) {
	if !system.ValidSysctlKey(key) {
		return "", fmt.Errorf("sysctl key %q: %w", key, system.ErrInvalid)
	}
	return "/proc/sys/" + strings.ReplaceAll(key, ".", "/"), nil
}

func (s *sysctl) Get(ctx context.Context, key string) (string, error) {
	p, err := sysctlPath(key)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(s.h.path(p))
	if err != nil {
		return "", mapErr("reading sysctl", key, err)
	}
	return strings.TrimSpace(string(data)), nil
}

func (s *sysctl) Set(ctx context.Context, key, value string) error {
	p, err := sysctlPath(key)
	if err != nil {
		return err
	}
	if strings.ContainsAny(value, "\n\x00") {
		return fmt.Errorf("sysctl value %q: %w", value, system.ErrInvalid)
	}
	// Files under /proc/sys are written in place; they can't be replaced.
	f, err := os.OpenFile(s.h.path(p), os.O_WRONLY|os.O_TRUNC, 0)
	if err != nil {
		return mapErr("setting sysctl", key, err)
	}
	if _, err := f.WriteString(value + "\n"); err != nil {
		f.Close()
		return mapErr("setting sysctl", key, err)
	}
	return mapErr("setting sysctl", key, f.Close())
}

type processes struct{ h *host }

// procStat holds the fields of /proc/<pid>/stat Racksail uses.
type procStat struct {
	comm      string
	state     string
	ppid      int
	cpuTicks  int64
	startTick int64
}

func parseProcStat(data []byte) (procStat, bool) {
	open := bytes.IndexByte(data, '(')
	closing := bytes.LastIndexByte(data, ')')
	if open < 0 || closing < open {
		return procStat{}, false
	}
	rest := strings.Fields(string(data[closing+1:]))
	if len(rest) < 20 {
		return procStat{}, false
	}
	return procStat{
		comm:      string(data[open+1 : closing]),
		state:     rest[0],
		ppid:      atoi(rest[1]),
		cpuTicks:  atoi64(rest[11]) + atoi64(rest[12]),
		startTick: atoi64(rest[19]),
	}, true
}

// parseProcStatus reads the real UID and the resident set size.
func parseProcStatus(data []byte) (uid int, rss int64) {
	eachLine(data, func(line string) {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			return
		}
		f := strings.Fields(value)
		if len(f) == 0 {
			return
		}
		switch key {
		case "Uid":
			uid = atoi(f[0])
		case "VmRSS":
			rss = atoi64(f[0]) * 1024
		}
	})
	return uid, rss
}

// unitFromCgroup finds the systemd unit in a /proc/<pid>/cgroup file.
func unitFromCgroup(data []byte) string {
	unit := ""
	eachLine(data, func(line string) {
		parts := strings.SplitN(line, ":", 3)
		if len(parts) != 3 || (parts[0] != "0" && unit != "") {
			return
		}
		for _, el := range strings.Split(parts[2], "/") {
			if strings.HasSuffix(el, ".service") || strings.HasSuffix(el, ".scope") {
				unit = el
			}
		}
	})
	return unit
}

func (h *host) bootTime() (time.Time, error) {
	data, err := os.ReadFile(h.path("/proc/stat"))
	if err != nil {
		return time.Time{}, err
	}
	var bt time.Time
	eachLine(data, func(line string) {
		if v, ok := strings.CutPrefix(line, "btime "); ok {
			bt = time.Unix(atoi64(strings.TrimSpace(v)), 0).UTC()
		}
	})
	if bt.IsZero() {
		return bt, errors.New("no btime in /proc/stat")
	}
	return bt, nil
}

func (p *processes) List(ctx context.Context, filter system.ProcessFilter) ([]system.Process, error) {
	entries, err := os.ReadDir(p.h.path("/proc"))
	if err != nil {
		return nil, fmt.Errorf("reading /proc: %w", err)
	}
	boot, err := p.h.bootTime()
	if err != nil {
		return nil, err
	}
	db, err := p.h.accounts()
	if err != nil {
		db = &accountDB{}
	}
	var out []system.Process
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		dir := path.Join("/proc", e.Name())
		statData, err1 := os.ReadFile(p.h.path(path.Join(dir, "stat")))
		statusData, err2 := os.ReadFile(p.h.path(path.Join(dir, "status")))
		if err1 != nil || err2 != nil {
			// The process exited while we were looking.
			continue
		}
		st, ok := parseProcStat(statData)
		if !ok {
			continue
		}
		uid, rss := parseProcStatus(statusData)
		proc := system.Process{
			PID: pid, PPID: st.ppid, UID: uid, User: db.userName(uid), Command: st.comm, State: st.state, RSSBytes: rss,
			CPUTime: time.Duration(st.cpuTicks) * time.Second / clockTicks,
			Started: boot.Add(time.Duration(st.startTick) * time.Second / clockTicks),
		}
		if cmdline, err := os.ReadFile(p.h.path(path.Join(dir, "cmdline"))); err == nil && len(cmdline) > 0 {
			proc.Args = strings.Split(strings.TrimRight(string(cmdline), "\x00"), "\x00")
		}
		if cg, err := os.ReadFile(p.h.path(path.Join(dir, "cgroup"))); err == nil {
			proc.Unit = unitFromCgroup(cg)
		}
		if filter.User != "" && proc.User != filter.User {
			continue
		}
		if filter.Unit != "" && proc.Unit != filter.Unit {
			continue
		}
		out = append(out, proc)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PID < out[j].PID })
	return out, nil
}

func (p *processes) Signal(ctx context.Context, pid int, sig system.Signal) error {
	n, ok := signalNumbers[sig]
	if !ok {
		return fmt.Errorf("signal %q: %w", sig, system.ErrInvalid)
	}
	if pid <= 1 {
		return fmt.Errorf("refusing to signal PID %d: %w", pid, system.ErrInvalid)
	}
	if err := syscall.Kill(pid, syscall.Signal(n)); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return fmt.Errorf("process %d: %w", pid, system.ErrNotFound)
		}
		return fmt.Errorf("signalling process %d: %w", pid, err)
	}
	return nil
}

type facts struct{ h *host }

func (f *facts) read(p string) string {
	data, err := os.ReadFile(f.h.path(p))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func (f *facts) exists(p string) bool {
	_, err := os.Stat(f.h.path(p))
	return err == nil
}

func (f *facts) Facts(ctx context.Context) (system.MachineFacts, error) {
	osRelease, err := os.ReadFile(f.h.path("/etc/os-release"))
	if err != nil {
		return system.MachineFacts{}, fmt.Errorf("reading /etc/os-release: %w", err)
	}
	cpuinfo, err := os.ReadFile(f.h.path("/proc/cpuinfo"))
	if err != nil {
		return system.MachineFacts{}, fmt.Errorf("reading /proc/cpuinfo: %w", err)
	}
	meminfo, err := os.ReadFile(f.h.path("/proc/meminfo"))
	if err != nil {
		return system.MachineFacts{}, fmt.Errorf("reading /proc/meminfo: %w", err)
	}
	mem, swap := parseMeminfo(meminfo)
	facts := system.MachineFacts{
		Hostname:    f.read("/etc/hostname"),
		MachineID:   f.read("/etc/machine-id"),
		BootID:      f.read("/proc/sys/kernel/random/boot_id"),
		OS:          parseOSRelease(osRelease),
		Kernel:      f.read("/proc/sys/kernel/osrelease"),
		Arch:        runtime.GOARCH,
		CPU:         parseCPUInfo(cpuinfo),
		MemoryBytes: mem,
		SwapBytes:   swap,
		TPM:         f.exists("/sys/class/tpm/tpm0"),
		Timezone:    f.timezone(),
		CollectedAt: time.Now().UTC(),
	}
	facts.Firmware.UEFI = f.exists("/sys/firmware/efi")
	if data, err := os.ReadFile(f.h.path("/sys/firmware/efi/efivars/SecureBoot-8be4df61-93ca-11d2-aa0d-00e098032b8c")); err == nil && len(data) >= 5 {
		facts.Firmware.SecureBoot = data[4] == 1
	}
	if fi, err := os.Stat(f.h.path("/dev/kvm")); err == nil && fi.Mode()&fs.ModeCharDevice != 0 {
		facts.KVM = true
		facts.CPU.Virtualization = true
	}
	if entries, err := os.ReadDir(f.h.path("/sys/class/iommu")); err == nil && len(entries) > 0 {
		facts.IOMMU = true
	}
	if bt, err := f.h.bootTime(); err == nil {
		facts.BootTime = bt
	}
	if out, err := f.h.run.Run(ctx, system.Command{Path: progDetectVirt, Timeout: 5 * time.Second}); err == nil || len(out.Stdout) > 0 {
		if v := strings.TrimSpace(string(out.Stdout)); v != "none" {
			facts.Virtualization = v
		}
	}
	if out, err := f.h.run.Run(ctx, system.Command{Path: progHostname, Args: []string{"--fqdn"}, Timeout: 5 * time.Second}); err == nil {
		facts.FQDN = strings.TrimSpace(string(out.Stdout))
	}
	return facts, nil
}

func (f *facts) timezone() string {
	if tz := f.read("/etc/timezone"); tz != "" {
		return tz
	}
	if target, err := os.Readlink(f.h.path("/etc/localtime")); err == nil {
		if _, zone, ok := strings.Cut(target, "zoneinfo/"); ok {
			return zone
		}
	}
	return "UTC"
}

func parseOSRelease(data []byte) system.OSRelease {
	var r system.OSRelease
	eachLine(data, func(line string) {
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return
		}
		value = strings.Trim(value, `"'`)
		switch key {
		case "ID":
			r.ID = value
		case "VERSION_ID":
			r.VersionID = value
		case "VERSION_CODENAME":
			r.VersionCodename = value
		case "PRETTY_NAME":
			r.PrettyName = value
		}
	})
	return r
}

func parseCPUInfo(data []byte) system.CPUInfo {
	var c system.CPUInfo
	cores := map[string]bool{}
	physical := ""
	eachLine(data, func(line string) {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			return
		}
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		switch key {
		case "processor":
			c.Threads++
		case "model name", "Model":
			if c.Model == "" {
				c.Model = value
			}
		case "physical id":
			physical = value
		case "core id":
			cores[physical+"/"+value] = true
		case "flags":
			for _, flag := range strings.Fields(value) {
				if flag == "vmx" || flag == "svm" {
					c.Virtualization = true
				}
			}
		}
	})
	c.Cores = len(cores)
	if c.Cores == 0 {
		c.Cores = c.Threads
	}
	return c
}

func parseMeminfo(data []byte) (mem, swap int64) {
	eachLine(data, func(line string) {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			return
		}
		f := strings.Fields(value)
		if len(f) == 0 {
			return
		}
		switch key {
		case "MemTotal":
			mem = atoi64(f[0]) * 1024
		case "SwapTotal":
			swap = atoi64(f[0]) * 1024
		}
	})
	return mem, swap
}
