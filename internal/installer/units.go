package installer

import "github.com/vilanobeachflorida/racksail/internal/system"

// The systemd units of Racksail itself.
const (
	AgentUnit = system.AgentUnit
	PanelUnit = system.PanelUnit
	EdgeUnit  = system.EdgeUnit
	// EdgeSocket is the edge's admin socket, which systemd holds so that the
	// edge starts the first time the panel sends it routes.
	EdgeSocket = system.EdgeSocketUnit
	// PanelSocket is the panel's edge-facing socket, which systemd holds for
	// the edge from early in the boot and which starts the panel when the
	// edge reaches it first.
	PanelSocket = system.PanelSocketUnit
)

// Units renders the slices and services of Racksail itself, with binary as
// the path of the racksail executable. The system layer renders them, so
// the installer, upgrades and packaging/systemd all have the same units.
func Units(binary string) (map[string][]byte, error) { return system.PlatformUnits(binary) }

// UnitNames returns the names of Racksail's own units in install order:
// slices before the services that use them, and the edge's socket before the
// edge.
func UnitNames() []string { return system.PlatformUnitNames() }
