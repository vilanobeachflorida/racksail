package system

import (
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"
)

// The systemd units of Racksail itself.
const (
	AgentUnit       = "racksail-agent.service"
	PanelUnit       = "racksail-panel.service"
	PanelSocketUnit = "racksail-panel.socket"
	EdgeUnit        = "racksail-edge.service"
	EdgeSocketUnit  = "racksail-edge.socket"
	// SecretsCredential is the encrypted credential holding the key the
	// panel encrypts secrets with.
	SecretsCredential = "racksail-secrets-key"
)

const documentation = "https://github.com/vilanobeachflorida/racksail"

// serviceHardening confines the unprivileged platform services, the panel
// and the edge. Neither runs anything but itself, so the system call filter
// can be strict.
var serviceHardening = []UnitEntry{
	{"NoNewPrivileges", "yes"},
	{"ProtectSystem", "strict"},
	{"ProtectHome", "yes"},
	{"PrivateTmp", "yes"},
	{"PrivateDevices", "yes"},
	{"ProtectKernelTunables", "yes"},
	{"ProtectKernelModules", "yes"},
	{"ProtectKernelLogs", "yes"},
	{"ProtectControlGroups", "yes"},
	{"ProtectClock", "yes"},
	{"ProtectHostname", "yes"},
	{"ProtectProc", "invisible"},
	{"ProcSubset", "pid"},
	{"RestrictSUIDSGID", "yes"},
	{"RestrictNamespaces", "yes"},
	{"RestrictRealtime", "yes"},
	{"RestrictAddressFamilies", "AF_UNIX AF_INET AF_INET6 AF_NETLINK"},
	{"LockPersonality", "yes"},
	{"MemoryDenyWriteExecute", "yes"},
	{"RemoveIPC", "yes"},
	{"KeyringMode", "private"},
	{"UMask", "0027"},
	{"SystemCallArchitectures", "native"},
	{"SystemCallFilter", "@system-service"},
	{"SystemCallFilter", "~@privileged @resources"},
	{"SystemCallErrorNumber", "EPERM"},
}

// agentHardening is what the agent can bear. It runs as root and runs
// apt, dpkg maintainer scripts, mkfs and the rest, so it keeps its
// capabilities, its devices and can write the system; it is shielded from
// what none of that needs. It must share the host's mount namespace, since
// the filesystems it mounts are for every service: so nothing here may give
// it one of its own, which PrivateTmp, ProtectKernelLogs,
// ProtectControlGroups and the other file system protections all do.
// ProtectClock is left out too because it implies a device allowlist, which
// would hide the disks.
var agentHardening = []UnitEntry{
	{"RestrictRealtime", "yes"},
	{"LockPersonality", "yes"},
	{"KeyringMode", "private"},
	{"UMask", "0022"},
}

// platformMemoryLow is the memory racksail.slice protects: what the agent,
// the panel and the edge claim with platformShare, together.
const platformMemoryLow = "128M"

// platformShare is a platform service's share of racksail.slice: five
// times the weight of all the accounts together, and the memory it keeps
// when the machine runs short.
func platformShare(memoryLow string) []UnitEntry {
	return []UnitEntry{{"CPUWeight", "500"}, {"IOWeight", "500"}, {"MemoryLow", memoryLow}}
}

// mountNamespacing are the settings that give a service a mount namespace
// of its own, which the agent must not have.
var mountNamespacing = []string{
	"PrivateTmp", "PrivateDevices", "PrivateMounts", "ProtectSystem", "ProtectHome", "ProtectKernelTunables",
	"ProtectKernelModules", "ProtectKernelLogs", "ProtectControlGroups", "ProtectProc", "ProcSubset",
	"ReadWritePaths", "ReadOnlyPaths", "InaccessiblePaths", "TemporaryFileSystem", "BindPaths", "BindReadOnlyPaths",
	"RootDirectory", "RootImage", "MountAPIVFS", "PrivateNetwork", "NetworkNamespacePath",
}

// PlatformUnits renders Racksail's own slices and services, with binary as
// the path of the racksail executable. The installer writes them and
// packaging/systemd keeps a copy for /usr/bin/racksail.
func PlatformUnits(binary string) (map[string][]byte, error) {
	if !path.IsAbs(binary) || path.Clean(binary) != binary || strings.ContainsAny(binary, " \t%$\"'\\") {
		return nil, fmt.Errorf("the racksail binary path %q must be absolute: %w", binary, ErrInvalid)
	}
	files := map[string]UnitFile{}

	// racksail-accounts.slice sits inside racksail.slice, since its name
	// extends that one's (decision D4). Weights on racksail.slice would
	// favour the accounts as much as the platform over the system's own
	// services, such as sshd and journald, so it keeps the default weight
	// and shares the machine evenly with system.slice. Inside it, each
	// platform service outweighs all the accounts together, so the panel
	// stays responsive and the edge keeps answering when a site is busy.
	// The slice protects as much memory as its services claim, so none of
	// that protection reaches the accounts.
	var platform UnitFile
	platform.Section("Unit").Add("Description", "Racksail platform services and accounts").
		Add("Documentation", documentation).Add("Before", "slices.target")
	platform.Section("Slice").AddEntries(accounting...).Add("MemoryLow", platformMemoryLow)
	files[PlatformSlice] = platform

	var accounts UnitFile
	accounts.Section("Unit").Add("Description", "Racksail accounts").
		Add("Documentation", documentation).Add("Before", "slices.target")
	accounts.Section("Slice").AddEntries(accounting...).Add("CPUWeight", "100").Add("IOWeight", "100")
	files[AccountsSlice] = accounts

	var agent UnitFile
	agent.Section("Unit").Add("Description", "Racksail agent").
		Add("Documentation", documentation).
		Add("After", "network-online.target").Add("Wants", "network-online.target")
	agent.Section("Service").
		Add("Type", "notify").
		Add("ExecStart", binary+" agent").
		Add("Slice", PlatformSlice).
		AddEntries(platformShare("32M")...).
		Add("Restart", "on-failure").Add("RestartSec", "2s").
		// The agent socket lives here; the panel reaches it through the
		// socket's group.
		Add("RuntimeDirectory", "racksail").Add("RuntimeDirectoryMode", "0755").Add("RuntimeDirectoryPreserve", "yes").
		Add("StateDirectory", "racksail-agent").Add("StateDirectoryMode", "0700").
		// The audit trail, which only root reads.
		Add("LogsDirectory", "racksail-agent").Add("LogsDirectoryMode", "0700").
		Add("LimitNOFILE", "65536").
		AddEntries(agentHardening...)
	agent.Section("Install").Add("WantedBy", "multi-user.target")
	files[AgentUnit] = agent

	// systemd creates the panel's edge-facing socket and hands it to the
	// panel, so it can belong to the edge's group without the panel chowning
	// it. The edge reaches the panel through it; account sandboxes can't,
	// since it lives under /run/racksail. The panel still binds its own TCP
	// port (8443) itself.
	var panelSocket UnitFile
	panelSocket.Section("Unit").Add("Description", "Racksail panel edge socket").Add("Documentation", documentation)
	panelSocket.Section("Socket").
		Add("ListenStream", PanelSocket).
		Add("SocketUser", "root").Add("SocketGroup", "racksail-edge").Add("SocketMode", "0660")
	panelSocket.Section("Install").Add("WantedBy", "sockets.target")
	files[PanelSocketUnit] = panelSocket

	var panel UnitFile
	panel.Section("Unit").Add("Description", "Racksail panel").
		Add("Documentation", documentation).
		Add("Requires", PanelSocketUnit).
		Add("After", "network-online.target "+AgentUnit+" "+PanelSocketUnit).Add("Wants", "network-online.target "+AgentUnit)
	panel.Section("Service").
		Add("Type", "exec").
		Add("ExecStart", binary+" panel").
		Add("User", "racksail").Add("Group", "racksail").
		Add("Slice", PlatformSlice).
		AddEntries(platformShare("64M")...).
		Add("Restart", "on-failure").Add("RestartSec", "2s").
		Add("StateDirectory", "racksail").Add("StateDirectoryMode", "0750").
		Add("CacheDirectory", "racksail").Add("CacheDirectoryMode", "0750").
		Add("ReadWritePaths", StateDir).
		// The key that encrypts secrets, sealed by systemd-creds; when it
		// isn't there the panel falls back to its key file.
		Add("ImportCredential", SecretsCredential).
		Add("CapabilityBoundingSet", "").
		Add("LimitNOFILE", "65536").
		AddEntries(serviceHardening...)
	panel.Section("Install").Add("WantedBy", "multi-user.target")
	files[PanelUnit] = panel

	// systemd creates the edge's admin socket and hands it to the edge, so
	// the socket can belong to the panel's group without the edge joining
	// that group, which would let it reach the agent's socket too. The edge
	// itself can't write to /run/racksail.
	var edgeSocket UnitFile
	edgeSocket.Section("Unit").Add("Description", "Racksail edge admin socket").Add("Documentation", documentation)
	edgeSocket.Section("Socket").
		Add("ListenStream", EdgeSocket).
		Add("SocketUser", "root").Add("SocketGroup", "racksail").Add("SocketMode", "0660")
	edgeSocket.Section("Install").Add("WantedBy", "sockets.target")
	files[EdgeSocketUnit] = edgeSocket

	var edge UnitFile
	edge.Section("Unit").Add("Description", "Racksail edge").
		Add("Documentation", documentation).
		Add("Requires", EdgeSocketUnit).
		Add("After", "network-online.target "+EdgeSocketUnit).Add("Wants", "network-online.target")
	edge.Section("Service").
		Add("Type", "notify").
		Add("ExecStart", binary+" edge").
		Add("User", "racksail-edge").Add("Group", "racksail-edge").
		Add("Slice", PlatformSlice).
		AddEntries(platformShare("32M")...).
		Add("Restart", "on-failure").Add("RestartSec", "2s").
		Add("AmbientCapabilities", "CAP_NET_BIND_SERVICE").
		Add("CapabilityBoundingSet", "CAP_NET_BIND_SERVICE").
		Add("StateDirectory", "racksail-edge").Add("StateDirectoryMode", "0750").
		// Caddy keeps an instance ID in the user's data directory; the
		// edge's user has no home, so it goes with the edge's state.
		Add("Environment", "XDG_DATA_HOME="+EdgeStateDir).
		Add("LimitNOFILE", "1048576").
		AddEntries(serviceHardening...)
	// The edge isn't enabled when Racksail is installed: its socket is,
	// and the panel's first route starts the edge through it. The panel
	// enables the edge once it routes sites, so they come back at boot
	// without waiting for the panel, and disables it when none are left.
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

// PlatformUnitNames returns the names of Racksail's own units in install
// order: slices before the services in them, the edge's socket before the
// edge.
func PlatformUnitNames() []string {
	return []string{PlatformSlice, AccountsSlice, AgentUnit, PanelSocketUnit, PanelUnit, EdgeSocketUnit, EdgeUnit}
}

// WakeSpec describes an application that sleeps when idle. systemd holds
// its socket; the first connection starts Racksail's wake proxy, which
// starts the application, waits until it accepts connections and forwards
// to it. Once nothing has used it for IdleTimeout the proxy exits, and the
// application, which only the proxy needs, stops with it.
type WakeSpec struct {
	// Name is the base of the unit names, such as racksail-site-blog-www.
	Name        string
	Description string
	// Socket is where the edge connects.
	Socket string
	// SocketGroup may connect to it, normally racksail-edge.
	SocketGroup string
	// Upstream is where the application listens: a Unix socket path or
	// host:port.
	Upstream     string
	IdleTimeout  time.Duration
	StartTimeout time.Duration
	// Binary is the racksail executable, whose wake-proxy command proxies.
	Binary string
	// App is the application's service. It needs a sandbox; the proxy runs
	// as the same user in a sandbox of its own.
	App ServiceSpec
}

var wakeNamePattern = regexp.MustCompile(`^racksail-[a-z0-9][a-z0-9-]{0,120}$`)

// Units returns the socket, the proxy service and the application service,
// keyed by unit name.
func (w WakeSpec) Units() (map[string][]byte, error) {
	if !wakeNamePattern.MatchString(w.Name) {
		return nil, fmt.Errorf("unit base name %q must start with racksail-: %w", w.Name, ErrInvalid)
	}
	if w.App.Sandbox == nil {
		return nil, fmt.Errorf("%s needs a sandbox: %w", w.Name, ErrInvalid)
	}
	if w.IdleTimeout <= 0 || w.StartTimeout <= 0 {
		return nil, fmt.Errorf("%s needs idle and start timeouts: %w", w.Name, ErrInvalid)
	}
	upstream := w.Upstream
	unixUpstream := path.IsAbs(upstream)
	if !unixUpstream && !addressPattern.MatchString(upstream) && !strings.HasPrefix(upstream, "localhost:") {
		return nil, fmt.Errorf("upstream %q: %w", upstream, ErrInvalid)
	}
	socketUnit, proxyUnit, appUnit := w.Name+".socket", w.Name+"-proxy.service", w.Name+".service"

	sock, err := SocketSpec{
		Description: w.Description + " (socket)", ListenStream: []string{w.Socket},
		SocketUser: "root", SocketGroup: w.SocketGroup, SocketMode: fs.FileMode(0o660),
		Service: proxyUnit, RemoveOnStop: true, WantedBy: []string{"sockets.target"},
	}.Render()
	if err != nil {
		return nil, err
	}

	proxySandbox := *w.App.Sandbox
	proxySandbox.BindPaths, proxySandbox.BindReadOnlyPaths, proxySandbox.SocketBindAllow = nil, nil, nil
	proxySandbox.AllowWriteExecute = false
	if unixUpstream {
		proxySandbox.BindPaths = []string{path.Dir(upstream)}
	}
	proxy, err := ServiceSpec{
		Description: w.Description + " (wake proxy)",
		Requires:    []string{appUnit, socketUnit},
		After:       []string{appUnit, socketUnit},
		ExecStart: []string{w.Binary, "wake-proxy", "--upstream", upstream,
			"--idle-timeout", Seconds(w.IdleTimeout), "--start-timeout", Seconds(w.StartTimeout)},
		Slice:   w.App.Slice,
		Sandbox: &proxySandbox,
	}.Render()
	if err != nil {
		return nil, err
	}

	app := w.App
	app.StopWhenUnneeded = true
	app.WantedBy = nil
	if app.Restart == "always" {
		// A sleeping application must be allowed to stay stopped.
		app.Restart = "on-failure"
	}
	appFile, err := app.Render()
	if err != nil {
		return nil, err
	}
	return map[string][]byte{socketUnit: sock, proxyUnit: proxy, appUnit: appFile}, nil
}

// WakeUnitNames returns the unit names of a WakeSpec with the given base.
func WakeUnitNames(base string) []string {
	return []string{base + ".socket", base + "-proxy.service", base + ".service"}
}

// IsPlatformUnit reports whether name is one of Racksail's own units.
func IsPlatformUnit(name string) bool { return slices.Contains(PlatformUnitNames(), name) }
