package installer

import (
	"fmt"
	"path"
	"sort"

	"github.com/vilanobeachflorida/racksail/internal/system"
)

// The systemd units of Racksail itself.
const (
	AgentUnit = "racksail-agent.service"
	PanelUnit = "racksail-panel.service"
	EdgeUnit  = "racksail-edge.service"
)

// hardening is applied to the unprivileged platform services.
var hardening = []system.UnitEntry{
	{Key: "NoNewPrivileges", Value: "yes"},
	{Key: "ProtectSystem", Value: "strict"},
	{Key: "ProtectHome", Value: "yes"},
	{Key: "PrivateTmp", Value: "yes"},
	{Key: "PrivateDevices", Value: "yes"},
	{Key: "ProtectKernelTunables", Value: "yes"},
	{Key: "ProtectKernelModules", Value: "yes"},
	{Key: "ProtectKernelLogs", Value: "yes"},
	{Key: "ProtectControlGroups", Value: "yes"},
	{Key: "ProtectClock", Value: "yes"},
	{Key: "ProtectHostname", Value: "yes"},
	{Key: "RestrictSUIDSGID", Value: "yes"},
	{Key: "RestrictNamespaces", Value: "yes"},
	{Key: "RestrictRealtime", Value: "yes"},
	{Key: "LockPersonality", Value: "yes"},
	{Key: "SystemCallArchitectures", Value: "native"},
	{Key: "SystemCallFilter", Value: "@system-service"},
}

// Units renders the slices and services of Racksail itself, with binary as
// the path of the racksail executable.
func Units(binary string) (map[string][]byte, error) {
	if !path.IsAbs(binary) {
		return nil, fmt.Errorf("the racksail binary path %q must be absolute", binary)
	}
	files := map[string]system.UnitFile{}

	var platform system.UnitFile
	platform.Section("Unit").Add("Description", "Racksail platform services").Add("Before", "slices.target")
	platform.Section("Slice")
	files[system.PlatformSlice] = platform

	var accounts system.UnitFile
	accounts.Section("Unit").Add("Description", "Racksail accounts").Add("Before", "slices.target")
	accounts.Section("Slice")
	files[system.AccountsSlice] = accounts

	var agent system.UnitFile
	agent.Section("Unit").Add("Description", "Racksail agent").
		Add("Documentation", "https://github.com/vilanobeachflorida/racksail").
		Add("After", "network-online.target").Add("Wants", "network-online.target")
	agent.Section("Service").
		Add("ExecStart", binary+" agent").
		Add("Slice", system.PlatformSlice).
		Add("Restart", "on-failure").Add("RestartSec", "2s").
		// The agent socket lives here; the panel reaches it through the
		// socket's group.
		Add("RuntimeDirectory", "racksail").Add("RuntimeDirectoryMode", "0755").Add("RuntimeDirectoryPreserve", "yes")
	agent.Section("Install").Add("WantedBy", "multi-user.target")
	files[AgentUnit] = agent

	var panel system.UnitFile
	panel.Section("Unit").Add("Description", "Racksail panel").
		Add("Documentation", "https://github.com/vilanobeachflorida/racksail").
		Add("After", "network-online.target "+AgentUnit).Add("Wants", "network-online.target "+AgentUnit)
	panel.Section("Service").
		Add("ExecStart", binary+" panel").
		Add("User", "racksail").Add("Group", "racksail").
		Add("Slice", system.PlatformSlice).
		Add("Restart", "on-failure").Add("RestartSec", "2s").
		Add("StateDirectory", "racksail").Add("StateDirectoryMode", "0750").
		Add("ReadWritePaths", system.StateDir).
		AddEntries(hardening...)
	panel.Section("Install").Add("WantedBy", "multi-user.target")
	files[PanelUnit] = panel

	var edge system.UnitFile
	edge.Section("Unit").Add("Description", "Racksail edge").
		Add("Documentation", "https://github.com/vilanobeachflorida/racksail").
		Add("After", "network-online.target").Add("Wants", "network-online.target")
	edge.Section("Service").
		Add("ExecStart", binary+" edge").
		Add("User", "racksail-edge").Add("Group", "racksail-edge").
		Add("Slice", system.PlatformSlice).
		Add("Restart", "on-failure").Add("RestartSec", "2s").
		Add("AmbientCapabilities", "CAP_NET_BIND_SERVICE").
		Add("CapabilityBoundingSet", "CAP_NET_BIND_SERVICE").
		Add("StateDirectory", "racksail-edge").Add("StateDirectoryMode", "0750").
		AddEntries(hardening...)
	edge.Section("Install").Add("WantedBy", "multi-user.target")
	files[EdgeUnit] = edge

	out := make(map[string][]byte, len(files))
	for name, u := range files {
		raw, err := u.Render()
		if err != nil {
			return nil, fmt.Errorf("rendering %s: %w", name, err)
		}
		out[name] = raw
	}
	return out, nil
}

// UnitNames returns the names of Racksail's own units in install order:
// slices before the services that use them.
func UnitNames() []string {
	names := []string{system.PlatformSlice, system.AccountsSlice, AgentUnit, PanelUnit, EdgeUnit}
	sort.SliceStable(names, func(i, j int) bool { return path.Ext(names[i]) == ".slice" && path.Ext(names[j]) != ".slice" })
	return names
}
