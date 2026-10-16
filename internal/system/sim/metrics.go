package sim

import (
	"context"
	"math"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/system"
)

// statsState accumulates the machine's counters. Each sample adds what the
// machine did since the previous one at rates that follow what runs on it,
// so counters always grow and rates computed from two samples look like a
// real server's.
type statsState struct {
	Last     time.Time                      `json:"last"`
	CPU      system.CPUTimes                `json:"cpu"`
	Disks    map[string]*system.DiskIO      `json:"disks"`
	Networks map[string]*system.NetIO       `json:"networks"`
	Units    map[string]*system.UnitMetrics `json:"units"`
}

// wave is a smooth daily traffic curve between 0.3 and 1.
func wave(t time.Time) float64 {
	day := float64(t.Unix()%86400) / 86400
	return 0.65 + 0.35*math.Sin(2*math.Pi*(day-0.375))
}

// jitter is a deterministic variation between 0.8 and 1.2 for a key and
// minute.
func jitter(key string, t time.Time) float64 {
	return 0.8 + float64(hash(key+t.Truncate(time.Minute).String())%400)/1000
}

// unitLoad is the share of one CPU a running service uses.
func unitLoad(name string) float64 { return 0.002 + float64(hash(name)%30)/1000 }

// advanceStats brings the counters up to now. The caller holds m.mu.
func (m *Machine) advanceStats() {
	now := m.now()
	if m.stats == nil {
		boot := m.facts.BootTime
		m.stats = &statsState{
			Last:     boot,
			Disks:    map[string]*system.DiskIO{},
			Networks: map[string]*system.NetIO{},
			Units:    map[string]*system.UnitMetrics{},
		}
	}
	s := m.stats
	dt := now.Sub(s.Last)
	if dt <= 0 {
		return
	}
	// Long gaps, such as a machine loaded from disk days later, are
	// accounted in hourly steps so the daily curve still shows.
	step := min(dt, time.Hour)
	for t := s.Last; t.Before(now); t = t.Add(step) {
		d := min(step, now.Sub(t))
		m.advanceBy(t.Add(d), d)
	}
	s.Last = now
}

func (m *Machine) advanceBy(t time.Time, d time.Duration) {
	s := m.stats
	cpus := float64(max(m.facts.CPU.Threads, 1))
	busy := 0.01 * cpus
	var running []string
	for name, rt := range m.units {
		if rt.active == "active" && rt.pid != 0 {
			running = append(running, name)
			busy += unitLoad(name) * wave(t) * jitter(name, t)
		}
	}
	busy = math.Min(busy, cpus*0.95)
	total := time.Duration(cpus) * d
	busyTime := time.Duration(busy * float64(d))
	user, sys, soft, irq, steal, wait := busyTime*6/10, busyTime*3/10, busyTime/20, busyTime/40, busyTime/40, total/200
	s.CPU.User += user
	s.CPU.System += sys
	s.CPU.SoftIRQ += soft
	s.CPU.IRQ += irq
	s.CPU.Steal += steal
	s.CPU.IOWait += wait
	// Idle takes the rest, so the times add up to exactly the CPUs' time.
	s.CPU.Idle += total - user - sys - soft - irq - steal - wait

	secs := d.Seconds()
	for _, disk := range m.disks {
		io := s.Disks[disk.Name]
		if io == nil {
			io = &system.DiskIO{Name: disk.Name}
			s.Disks[disk.Name] = io
		}
		rate := 0.05
		if disk.System {
			rate = 1
		}
		rate *= wave(t) * jitter(disk.Name, t)
		io.ReadBytes += int64(rate * 2e6 * secs)
		io.WriteBytes += int64(rate * 1.2e6 * secs)
		io.ReadOps += int64(rate * 40 * secs)
		io.WriteOps += int64(rate * 25 * secs)
		io.BusyTime += time.Duration(rate * 0.02 * float64(d))
	}
	for _, ifc := range m.ifaces {
		if ifc.Type == "loopback" || !ifc.Up {
			continue
		}
		n := s.Networks[ifc.Name]
		if n == nil {
			n = &system.NetIO{Interface: ifc.Name}
			s.Networks[ifc.Name] = n
		}
		traffic := (0.2 + float64(len(running))*0.05) * wave(t) * jitter(ifc.Name, t)
		n.RxBytes += int64(traffic * 400e3 * secs)
		n.TxBytes += int64(traffic * 1.6e6 * secs)
		n.RxPackets += int64(traffic * 600 * secs)
		n.TxPackets += int64(traffic * 1400 * secs)
	}
	for _, name := range running {
		u := s.Units[name]
		if u == nil {
			u = &system.UnitMetrics{Unit: name}
			s.Units[name] = u
		}
		u.CPUUsage += time.Duration(unitLoad(name) * wave(t) * jitter(name, t) * float64(d))
		u.IOReadBytes += int64(unitLoad(name) * 5e6 * secs)
		u.IOWriteBytes += int64(unitLoad(name) * 2e6 * secs)
	}
}

type simMetrics struct{ m *Machine }

func (s simMetrics) System(ctx context.Context) (system.SystemMetrics, error) {
	m := s.m
	if err := m.lock(ctx); err != nil {
		return system.SystemMetrics{}, err
	}
	defer m.mu.Unlock()
	m.advanceStats()
	now := m.now()
	st := m.stats
	out := system.SystemMetrics{
		CollectedAt: now,
		Uptime:      now.Sub(m.facts.BootTime),
		CPUs:        m.facts.CPU.Threads,
		CPU:         st.CPU,
	}
	var rss int64
	running := 0
	for _, p := range m.procs {
		rss += p.RSSBytes
	}
	for _, rt := range m.units {
		if rt.active == "active" && rt.pid != 0 {
			running++
		}
	}
	busy := 0.01 + float64(running)*0.015*wave(now)
	out.Load = system.LoadAverage{One: busy * float64(out.CPUs) * jitter("load1", now), Five: busy * float64(out.CPUs), Fifteen: busy * float64(out.CPUs) * 0.9}
	total := m.facts.MemoryBytes
	used := int64(320<<20) + rss
	var fileBytes int64
	for _, n := range m.files {
		fileBytes += int64(len(n.data))
	}
	cached := min(total/4, int64(256<<20)+fileBytes)
	out.Memory = system.MemoryStats{
		TotalBytes: total, AvailableBytes: max(total-used, 0), FreeBytes: max(total-used-cached, 0),
		BuffersBytes: 48 << 20, CachedBytes: cached,
		SwapTotalBytes: m.facts.SwapBytes, SwapFreeBytes: m.facts.SwapBytes - min(m.facts.SwapBytes, 16<<20),
	}
	pressure := math.Max(0, (busy-0.5)*20)
	out.Pressure = system.Pressure{
		CPU:    system.PressureStats{Some10: pressure, Some60: pressure * 0.8, Some300: pressure * 0.6},
		Memory: system.PressureStats{Some10: pressure / 10},
		IO:     system.PressureStats{Some10: pressure / 4, Full10: pressure / 8},
	}
	for _, d := range m.disks {
		if io, ok := st.Disks[d.Name]; ok {
			out.Disks = append(out.Disks, *io)
		}
	}
	for _, ifc := range m.ifaces {
		if n, ok := st.Networks[ifc.Name]; ok {
			out.Networks = append(out.Networks, *n)
		}
	}
	seen := map[string]bool{}
	for _, mt := range m.mounts {
		if !system.RealFilesystem(mt.FSType) || seen[mt.Target] {
			continue
		}
		seen[mt.Target] = true
		size := m.deviceSize(mt.Source)
		usedBytes := m.treeSize(mt.Target) + size/50
		out.Filesystems = append(out.Filesystems, system.FSUsage{
			Mount: mt.Target, Device: mt.Source, FSType: mt.FSType, SizeBytes: size,
			UsedBytes: usedBytes, FreeBytes: max(size-usedBytes, 0),
			InodesTotal: size / 16384, InodesFree: size/16384 - int64(len(m.files)),
		})
	}
	sort.Slice(out.Filesystems, func(i, j int) bool { return out.Filesystems[i].Mount < out.Filesystems[j].Mount })
	return out, nil
}

func (s simMetrics) Units(ctx context.Context, names ...string) ([]system.UnitMetrics, error) {
	for _, n := range names {
		if !system.ValidUnitName(n) {
			return nil, invalid("unit name %q", n)
		}
	}
	m := s.m
	if err := m.lock(ctx); err != nil {
		return nil, err
	}
	defer m.mu.Unlock()
	m.advanceStats()
	var out []system.UnitMetrics
	for _, name := range names {
		if strings.HasSuffix(name, ".slice") {
			out = append(out, m.sliceMetrics(name))
			continue
		}
		rt, ok := m.units[name]
		if !ok || rt.active != "active" {
			continue
		}
		u := system.UnitMetrics{Unit: name}
		if c, ok := m.stats.Units[name]; ok {
			u = *c
		}
		if p, ok := m.procs[rt.pid]; ok {
			u.MemoryBytes = p.RSSBytes
			u.MemoryPeakBytes = p.RSSBytes + p.RSSBytes/4
			u.Tasks = 1 + int(hash(name)%8)
		}
		out = append(out, u)
	}
	return out, nil
}

// sliceMetrics sums the services in a slice and the slices below it, as
// the cgroup of a slice does. The caller holds m.mu.
func (m *Machine) sliceMetrics(slice string) system.UnitMetrics {
	out := system.UnitMetrics{Unit: slice}
	prefix := strings.TrimSuffix(slice, ".slice")
	for name, rt := range m.units {
		if rt.active != "active" || rt.pid == 0 {
			continue
		}
		content, _ := m.unitFile(name)
		in := unitDirective(content, "Slice")
		switch {
		case in != "":
		case system.IsPlatformUnit(name):
			in = system.PlatformSlice
		default:
			in = "system.slice"
		}
		inPrefix := strings.TrimSuffix(in, ".slice")
		if in != slice && !strings.HasPrefix(inPrefix, prefix+"-") {
			continue
		}
		if c, ok := m.stats.Units[name]; ok {
			out.CPUUsage += c.CPUUsage
			out.IOReadBytes += c.IOReadBytes
			out.IOWriteBytes += c.IOWriteBytes
		}
		if p, ok := m.procs[rt.pid]; ok {
			out.MemoryBytes += p.RSSBytes
			out.Tasks++
		}
	}
	out.MemoryPeakBytes = out.MemoryBytes
	return out
}

// RunningUnits lists the units that are active, for tests.
func (m *Machine) RunningUnits() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for name, rt := range m.units {
		if rt.active == "active" {
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return out
}
