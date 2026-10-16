package sim

import (
	"context"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/system"
)

// diskHealth is what a simulated disk's SMART data changes with.
type diskHealth struct {
	// Failing disks report reallocated and pending sectors and fail their
	// self-tests.
	Failing     bool                  `json:"failing,omitempty"`
	Reallocated int64                 `json:"reallocated,omitempty"`
	Pending     int64                 `json:"pending,omitempty"`
	Test        *runningTest          `json:"test,omitempty"`
	LastTest    *system.SMARTSelfTest `json:"lastTest,omitempty"`
}

type runningTest struct {
	Long     bool          `json:"long"`
	Started  time.Time     `json:"started"`
	Duration time.Duration `json:"duration"`
}

// health returns a disk's health state, creating it. The caller holds m.mu.
func (m *Machine) health(name string) *diskHealth {
	h := m.smart[name]
	if h == nil {
		h = &diskHealth{}
		m.smart[name] = h
	}
	return h
}

// testDuration is how long a self-test of the disk takes.
func testDuration(d *system.Disk, long bool) time.Duration {
	if !long {
		return 2 * time.Minute
	}
	if d.Transport == "nvme" {
		return 10 * time.Minute
	}
	// About 200 MB/s across the whole surface.
	return time.Duration(d.SizeBytes/200e6) * time.Second
}

// DegradeDisk makes a disk start failing: sectors get reallocated, SMART
// reports it unhealthy and self-tests fail, so disk alerts can be tested.
func (m *Machine) DegradeDisk(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	h := m.health(name)
	h.Failing = true
	h.Reallocated += 48
	h.Pending += 8
	m.changed()
}

// settleTest finishes a self-test whose time is up. The caller holds m.mu.
func (m *Machine) settleTest(d *system.Disk, h *diskHealth, hours int) {
	t := h.Test
	if t == nil || m.now().Before(t.Started.Add(t.Duration)) {
		return
	}
	kind := "short"
	if t.Long {
		kind = "long"
	}
	status := "passed"
	if h.Failing {
		status = "failed"
	}
	h.LastTest = &system.SMARTSelfTest{Status: status, Type: kind, LifetimeHours: hours}
	h.Test = nil
}

func (s simDisks) SMART(ctx context.Context, name string) (system.SMARTReport, error) {
	m := s.m
	if err := m.lock(ctx); err != nil {
		return system.SMARTReport{}, err
	}
	defer m.mu.Unlock()
	d, p := m.findDevice(name)
	if d == nil || p != nil {
		return system.SMARTReport{}, notFound("disk " + name)
	}
	h := m.health(d.Name)
	seed := hash(d.Serial)
	hours := int(seed%30000) + int(m.now().Sub(m.facts.BootTime).Hours())
	m.settleTest(d, h, hours)
	temp := 30 + int(seed%15)
	if !d.Rotational {
		temp += 6
	}
	r := system.SMARTReport{
		Supported: true, Healthy: !h.Failing, Model: d.Model, Serial: d.Serial, Firmware: "SIM-1.0",
		TemperatureC: temp, PowerOnHours: hours, PowerCycles: 20 + int(seed%200),
		ReallocatedSectors: h.Reallocated, PendingSectors: h.Pending,
	}
	if h.LastTest != nil {
		last := *h.LastTest
		r.SelfTest = &last
	}
	if t := h.Test; t != nil {
		left := t.Started.Add(t.Duration).Sub(m.now())
		kind := "short"
		if t.Long {
			kind = "long"
		}
		r.SelfTest = &system.SMARTSelfTest{Status: "running", Type: kind, RemainingPercent: max(1, int(100*left/t.Duration))}
	}
	if d.Transport == "nvme" {
		r.Protocol = "NVMe"
		r.AvailableSpare = 100
		r.PercentageUsed = int(seed % 12)
		r.UnsafeShutdowns = int64(seed % 9)
		r.DataReadBytes = int64(hours) * 3_500_000_000
		r.DataWrittenBytes = int64(hours) * 2_100_000_000
		if h.Failing {
			r.AvailableSpare, r.CriticalWarning, r.MediaErrors = 8, 1, h.Reallocated
		}
		return r, nil
	}
	r.Protocol = "ATA"
	value := func(bad int64) int {
		if bad > 0 {
			return int(max(1, 100-bad))
		}
		return 100
	}
	r.Attributes = []system.SMARTAttribute{
		{ID: 1, Name: "Raw_Read_Error_Rate", Value: 100, Worst: 100, Threshold: 16},
		{ID: 5, Name: "Reallocated_Sector_Ct", Value: value(h.Reallocated), Worst: value(h.Reallocated), Threshold: 10, Raw: h.Reallocated, Failing: h.Reallocated > 90},
		{ID: 9, Name: "Power_On_Hours", Value: 99, Worst: 99, Raw: int64(hours)},
		{ID: 12, Name: "Power_Cycle_Count", Value: 100, Worst: 100, Raw: int64(r.PowerCycles)},
		{ID: 194, Name: "Temperature_Celsius", Value: 100 - temp, Worst: 60, Raw: int64(temp)},
		{ID: 197, Name: "Current_Pending_Sector", Value: value(h.Pending), Worst: value(h.Pending), Raw: h.Pending},
		{ID: 198, Name: "Offline_Uncorrectable", Value: 100, Worst: 100},
		{ID: 199, Name: "UDMA_CRC_Error_Count", Value: 200, Worst: 200},
	}
	return r, nil
}

func (s simDisks) SelfTest(ctx context.Context, name string, long bool) error {
	m := s.m
	if err := m.lock(ctx); err != nil {
		return err
	}
	defer m.mu.Unlock()
	kind := "short"
	if long {
		kind = "long"
	}
	if err := m.record("disks", "self_test", name+" "+kind); err != nil {
		return err
	}
	d, p := m.findDevice(name)
	if d == nil || p != nil {
		return notFound("disk " + name)
	}
	h := m.health(d.Name)
	m.settleTest(d, h, 0)
	if h.Test != nil {
		return invalid("a self-test is already running on %s", d.Name)
	}
	h.Test = &runningTest{Long: long, Started: m.now(), Duration: testDuration(d, long)}
	return nil
}
