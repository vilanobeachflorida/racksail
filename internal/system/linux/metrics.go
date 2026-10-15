package linux

import (
	"context"
	"os"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/system"
)

type metrics struct {
	h       *host
	systemd Systemd
}

func (m *metrics) read(p string) string {
	data, err := os.ReadFile(m.h.path(p))
	if err != nil {
		return ""
	}
	return string(data)
}

func (m *metrics) System(ctx context.Context) (system.SystemMetrics, error) {
	s := system.SystemMetrics{CollectedAt: time.Now().UTC()}
	if f := strings.Fields(m.read("/proc/uptime")); len(f) > 0 {
		secs, _ := strconv.ParseFloat(f[0], 64)
		s.Uptime = time.Duration(secs * float64(time.Second))
	}
	s.Load = parseLoadavg(m.read("/proc/loadavg"))
	s.CPU, s.CPUs = parseProcStatCPU(m.read("/proc/stat"))
	s.Memory = parseMemStats([]byte(m.read("/proc/meminfo")))
	s.Pressure = system.Pressure{
		CPU:    parsePressure(m.read("/proc/pressure/cpu")),
		Memory: parsePressure(m.read("/proc/pressure/memory")),
		IO:     parsePressure(m.read("/proc/pressure/io")),
	}
	disks := map[string]bool{}
	if entries, err := os.ReadDir(m.h.path("/sys/block")); err == nil {
		for _, e := range entries {
			if !slices.ContainsFunc(skippedBlockDevices, func(p string) bool { return strings.HasPrefix(e.Name(), p) }) {
				disks[e.Name()] = true
			}
		}
	}
	s.Disks = parseDiskstats(m.read("/proc/diskstats"), disks)
	s.Networks = parseNetDev(m.read("/proc/net/dev"))
	if data, err := os.ReadFile(m.h.path("/proc/self/mountinfo")); err == nil {
		seen := map[string]bool{}
		for _, mt := range parseMountinfo(data) {
			if !system.RealFilesystem(mt.FSType) || seen[mt.Target] {
				continue
			}
			seen[mt.Target] = true
			usage, err := statfs(m.h.path(mt.Target))
			if err != nil {
				continue
			}
			usage.Mount, usage.Device, usage.FSType = mt.Target, mt.Source, mt.FSType
			s.Filesystems = append(s.Filesystems, usage)
		}
	}
	return s, nil
}

func parseLoadavg(s string) system.LoadAverage {
	f := strings.Fields(s)
	var l system.LoadAverage
	if len(f) >= 3 {
		l.One, _ = strconv.ParseFloat(f[0], 64)
		l.Five, _ = strconv.ParseFloat(f[1], 64)
		l.Fifteen, _ = strconv.ParseFloat(f[2], 64)
	}
	return l
}

func ticks(s string) time.Duration {
	return time.Duration(atoi64(s)) * time.Second / clockTicks
}

// parseProcStatCPU reads the aggregate cpu line and counts the CPUs.
func parseProcStatCPU(s string) (system.CPUTimes, int) {
	var c system.CPUTimes
	cpus := 0
	for _, line := range strings.Split(s, "\n") {
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		switch {
		case f[0] == "cpu" && len(f) >= 9:
			c = system.CPUTimes{
				User: ticks(f[1]), Nice: ticks(f[2]), System: ticks(f[3]), Idle: ticks(f[4]),
				IOWait: ticks(f[5]), IRQ: ticks(f[6]), SoftIRQ: ticks(f[7]), Steal: ticks(f[8]),
			}
		case strings.HasPrefix(f[0], "cpu"):
			cpus++
		}
	}
	return c, cpus
}

func parseMemStats(data []byte) system.MemoryStats {
	var m system.MemoryStats
	eachLine(data, func(line string) {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			return
		}
		f := strings.Fields(value)
		if len(f) == 0 {
			return
		}
		n := atoi64(f[0]) * 1024
		switch key {
		case "MemTotal":
			m.TotalBytes = n
		case "MemAvailable":
			m.AvailableBytes = n
		case "MemFree":
			m.FreeBytes = n
		case "Buffers":
			m.BuffersBytes = n
		case "Cached":
			m.CachedBytes = n
		case "SwapTotal":
			m.SwapTotalBytes = n
		case "SwapFree":
			m.SwapFreeBytes = n
		}
	})
	return m
}

// parsePressure reads a pressure stall information file.
func parsePressure(s string) system.PressureStats {
	var p system.PressureStats
	for _, line := range strings.Split(s, "\n") {
		f := strings.Fields(line)
		if len(f) < 4 {
			continue
		}
		vals := map[string]float64{}
		for _, kv := range f[1:] {
			k, v, ok := strings.Cut(kv, "=")
			if ok {
				vals[k], _ = strconv.ParseFloat(v, 64)
			}
		}
		switch f[0] {
		case "some":
			p.Some10, p.Some60, p.Some300 = vals["avg10"], vals["avg60"], vals["avg300"]
		case "full":
			p.Full10, p.Full60, p.Full300 = vals["avg10"], vals["avg60"], vals["avg300"]
		}
	}
	return p
}

// parseDiskstats reads /proc/diskstats for the named disks. Sectors are
// always 512 bytes there.
func parseDiskstats(s string, disks map[string]bool) []system.DiskIO {
	var out []system.DiskIO
	for _, line := range strings.Split(s, "\n") {
		f := strings.Fields(line)
		if len(f) < 14 || !disks[f[2]] {
			continue
		}
		out = append(out, system.DiskIO{
			Name: f[2], ReadOps: atoi64(f[3]), ReadBytes: atoi64(f[5]) * 512,
			WriteOps: atoi64(f[7]), WriteBytes: atoi64(f[9]) * 512,
			BusyTime: time.Duration(atoi64(f[12])) * time.Millisecond,
		})
	}
	return out
}

// parseNetDev reads /proc/net/dev, leaving out the loopback interface.
func parseNetDev(s string) []system.NetIO {
	var out []system.NetIO
	for _, line := range strings.Split(s, "\n") {
		name, rest, ok := strings.Cut(line, ":")
		name = strings.TrimSpace(name)
		if !ok || name == "lo" || strings.Contains(name, "|") {
			continue
		}
		f := strings.Fields(rest)
		if len(f) < 16 {
			continue
		}
		out = append(out, system.NetIO{
			Interface: name,
			RxBytes:   atoi64(f[0]), RxPackets: atoi64(f[1]), RxErrors: atoi64(f[2]), RxDropped: atoi64(f[3]),
			TxBytes: atoi64(f[8]), TxPackets: atoi64(f[9]), TxErrors: atoi64(f[10]), TxDropped: atoi64(f[11]),
		})
	}
	return out
}

func (m *metrics) Units(ctx context.Context, names ...string) ([]system.UnitMetrics, error) {
	var out []system.UnitMetrics
	for _, name := range names {
		if err := checkUnit(name); err != nil {
			return nil, err
		}
		props, err := m.systemd.Properties(ctx, name)
		if err != nil {
			return nil, err
		}
		cg := str(props["ControlGroup"])
		if cg == "" || str(props["ActiveState"]) == "inactive" {
			continue
		}
		out = append(out, m.cgroup(name, cg))
	}
	return out, nil
}

// cgroup reads a unit's control group files in cgroup v2.
func (m *metrics) cgroup(unit, cg string) system.UnitMetrics {
	dir := path.Join("/sys/fs/cgroup", path.Clean("/"+cg))
	u := system.UnitMetrics{Unit: unit}
	for _, line := range strings.Split(m.read(dir+"/cpu.stat"), "\n") {
		if v, ok := strings.CutPrefix(line, "usage_usec "); ok {
			u.CPUUsage = time.Duration(atoi64(strings.TrimSpace(v))) * time.Microsecond
		}
	}
	u.MemoryBytes = atoi64(strings.TrimSpace(m.read(dir + "/memory.current")))
	u.MemoryPeakBytes = atoi64(strings.TrimSpace(m.read(dir + "/memory.peak")))
	u.SwapBytes = atoi64(strings.TrimSpace(m.read(dir + "/memory.swap.current")))
	u.Tasks = atoi(strings.TrimSpace(m.read(dir + "/pids.current")))
	for _, line := range strings.Split(m.read(dir+"/io.stat"), "\n") {
		for _, kv := range strings.Fields(line) {
			k, v, ok := strings.Cut(kv, "=")
			if !ok {
				continue
			}
			switch k {
			case "rbytes":
				u.IOReadBytes += atoi64(v)
			case "wbytes":
				u.IOWriteBytes += atoi64(v)
			}
		}
	}
	u.Pressure = system.Pressure{
		CPU:    parsePressure(m.read(dir + "/cpu.pressure")),
		Memory: parsePressure(m.read(dir + "/memory.pressure")),
		IO:     parsePressure(m.read(dir + "/io.pressure")),
	}
	return u
}
