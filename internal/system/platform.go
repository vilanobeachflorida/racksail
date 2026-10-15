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
	AgentUnit      = "racksail-agent.service"
	PanelUnit      = "racksail-panel.service"
	EdgeUnit       = "racksail-edge.service"
	EdgeSocketUnit = "racksail-edge.socket"
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
// what none of that needs. ProtectClock is left out because it implies a
// device allowlist, which would hide the disks.
var agentHardening = []UnitEntry{
	{"PrivateTmp", "yes"},
	{"ProtectKernelLogs", "yes"},
	{"ProtectControlGroups", "yes"},
	{"RestrictRealtime", "yes"},
	{"LockPersonality", "yes"},
	{"KeyringMode", "private"},
	{"UMask", "0022"},
}

// PlatformUnits renders Racksail's own slices and services, with binary as
// the path of the racksail executable. The installer writes them and
// packaging/systemd keeps a copy for /usr/bin/racksail.
func PlatformUnits(binary string) (map[string][]byte, error) {
	if !path.IsAbs(binary) || path.Clean(binary) != binary || strings.ContainsAny(binary, " \t%$\"'\\") {
		return nil, fmt.Errorf("the racksail binary path %q must be absolute: %w", binary, ErrInvalid)
	}
	files := map[string]UnitFile{}

	var platform UnitFile
	platform.Section("Unit").Add("Description", "Racksail platform services").
		Add("Documentation", documentation).Add("Before", "slices.target")
	// The platform outweighs accounts under contention, so the panel stays
	// responsive and the edge keeps answering when a site is busy.
	platform.Section("Slice").AddEntries(accounting...).
		Add("CPUWeight", "500").Add("IOWeight", "500").Add("MemoryLow", "128M")
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
		Add("Restart", "on-failure").Add("RestartSec", "2s").
		// The agent socket lives here; the panel reaches it through the
		// socket's group.
		Add("RuntimeDirectory", "racksail").Add("RuntimeDirectoryMode", "0755").Add("RuntimeDirectoryPreserve", "yes").
		Add("StateDirectory", "racksail-agent").Add("StateDirectoryMode", "0700").
		Add("LimitNOFILE", "65536").
		AddEntries(agentHardening...)
	agent.Section("Install").Add("WantedBy", "multi-user.target")
	files[AgentUnit] = agent

	var panel UnitFile
	panel.Section("Unit").Add("Description", "Racksail panel").
		Add("Documentation", documentation).
		Add("After", "network-online.target "+AgentUnit).Add("Wants", "network-online.target "+AgentUnit)
	panel.Section("Service").
		Add("Type", "exec").
		Add("ExecStart", binary+" panel").
		Add("User", "racksail").Add("Group", "racksail").
		Add("Slice", PlatformSlice).
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
		Add("Restart", "on-failure").Add("RestartSec", "2s").
		Add("AmbientCapabilities", "CAP_NET_BIND_SERVICE").
		Add("CapabilityBoundingSet", "CAP_NET_BIND_SERVICE").
		Add("StateDirectory", "racksail-edge").Add("StateDirectoryMode", "0750").
		Add("LimitNOFILE", "1048576").
		AddEntries(serviceHardening...)
	// The edge isn't started at boot: its socket is, and the panel's first
	// request on it starts the edge.
	edge.Section("Install").Add("Also", EdgeSocketUnit)
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
	return []string{PlatformSlice, AccountsSlice, AgentUnit, PanelUnit, EdgeSocketUnit, EdgeUnit}
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
