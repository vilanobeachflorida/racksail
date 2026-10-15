package system

import (
	"context"
	"regexp"
	"time"
)

// Units manages systemd units: services, slices, sockets, timers, mounts and
// paths. Unit files live in /etc/systemd/system.
type Units interface {
	// WriteUnit writes a unit file. Call DaemonReload before using it.
	WriteUnit(ctx context.Context, name string, content []byte) error
	ReadUnit(ctx context.Context, name string) ([]byte, error)
	RemoveUnit(ctx context.Context, name string) error
	// WriteDropIn writes /etc/systemd/system/<unit>.d/<name>.conf, which
	// adjusts a unit Racksail doesn't own, such as a distribution service.
	WriteDropIn(ctx context.Context, unit, name string, content []byte) error
	RemoveDropIn(ctx context.Context, unit, name string) error
	DaemonReload(ctx context.Context) error
	// Start, Stop, Restart and Reload wait until systemd has finished the job
	// and fail if it didn't succeed.
	Start(ctx context.Context, name string) error
	Stop(ctx context.Context, name string) error
	Restart(ctx context.Context, name string) error
	Reload(ctx context.Context, name string) error
	Enable(ctx context.Context, names ...string) error
	Disable(ctx context.Context, names ...string) error
	Status(ctx context.Context, name string) (UnitState, error)
	// List returns units whose names match any of the glob patterns, such as
	// "racksail-*".
	List(ctx context.Context, patterns ...string) ([]UnitState, error)
	// SetProperties changes resource-control properties of a running unit,
	// such as MemoryMax on an account slice. runtime changes don't persist
	// across reboots.
	SetProperties(ctx context.Context, name string, runtime bool, props ...UnitProperty) error
	Kill(ctx context.Context, name string, signal Signal) error
	ResetFailed(ctx context.Context, name string) error
}

// UnitState is the state of a unit as systemd reports it.
type UnitState struct {
	Name          string `json:"name"`
	Description   string `json:"description,omitempty"`
	LoadState     string `json:"loadState" doc:"loaded, not-found, masked or error"`
	ActiveState   string `json:"activeState" doc:"active, inactive, failed, activating, deactivating or reloading"`
	SubState      string `json:"subState"`
	UnitFileState string `json:"unitFileState,omitempty" doc:"enabled, disabled, static, masked or empty"`
	MainPID       int    `json:"mainPid,omitempty"`
	MemoryBytes   int64  `json:"memoryBytes,omitempty"`
	// CPUTime is the CPU time used since the unit started.
	CPUTime     time.Duration `json:"cpuTime,omitempty"`
	Tasks       int           `json:"tasks,omitempty"`
	Restarts    int           `json:"restarts,omitempty"`
	ActiveSince time.Time     `json:"activeSince,omitzero"`
}

// Active reports whether the unit is running.
func (s UnitState) Active() bool { return s.ActiveState == "active" || s.ActiveState == "reloading" }

// Failed reports whether the unit is in the failed state.
func (s UnitState) Failed() bool { return s.ActiveState == "failed" }

// UnitProperty is one property for SetProperties, such as MemoryMax=512M.
type UnitProperty struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

var unitNamePattern = regexp.MustCompile(`^[A-Za-z0-9:_.@-]{1,250}\.(service|socket|slice|timer|mount|automount|path|target|scope|swap)$`)

// ValidUnitName reports whether name is a well-formed systemd unit name.
func ValidUnitName(name string) bool { return unitNamePattern.MatchString(name) }
