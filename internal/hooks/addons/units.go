package addons

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/netip"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/vilanobeachflorida/racksail/internal/agent"
	"github.com/vilanobeachflorida/racksail/internal/system"
)

// Where add-ons live on a server.
const (
	// Dir holds each add-on's program, one directory per version.
	Dir = system.ModulesDir + "/addons"
	// SocketDir holds the socket each add-on serves the panel on, which
	// systemd creates for the panel's user only.
	SocketDir = system.RunDir + "/addons"
	// GatewaySocket is where add-ons reach the panel's API.
	GatewaySocket = system.RunDir + "/addons.sock"
	// TokenDir holds each add-on's token, which systemd hands the add-on as
	// a credential when it starts.
	TokenDir = system.StateDir + "/addons"
	// ServiceTemplate and SocketTemplate are the systemd templates every
	// add-on runs from; DropIn is the name of each instance's own settings.
	ServiceTemplate = "racksail-addon@.service"
	SocketTemplate  = "racksail-addon@.socket"
	DropIn          = "racksail"
	// Program is the add-on's executable at the top of its directory.
	Program = "addon"
)

// AddonDir is where a version of an add-on's program is unpacked; without
// a version, the directory of all its versions.
func AddonDir(id, version string) string {
	if version == "" {
		return Dir + "/" + id
	}
	return Dir + "/" + id + "/" + version
}

// Socket is where an add-on serves the panel.
func Socket(id string) string { return SocketDir + "/" + id + ".sock" }

// Service and SocketUnit name an add-on's units.
func Service(id string) string    { return "racksail-addon@" + id + ".service" }
func SocketUnit(id string) string { return "racksail-addon@" + id + ".socket" }

// TokenFile is where an add-on's token is kept for systemd to hand over.
func TokenFile(id string) string { return TokenDir + "/" + id + ".token" }

var (
	// Instances are an add-on's id, with part of the account's for
	// account add-ons.
	instancePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,79}$`)
	versionPattern  = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z.-]+)?$`)
	hostNamePattern = regexp.MustCompile(`^(?i)[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)*$`)
)

// Host is a host an add-on may reach and the addresses it resolved to. A
// network given as such has no name.
type Host struct {
	Name      string   `json:"name,omitempty" doc:"The host's name, as the add-on's manifest declares it"`
	Addresses []string `json:"addresses" doc:"The addresses or networks the add-on may reach for it"`
}

// ServiceSpec is what an add-on's systemd service is rendered from.
type ServiceSpec struct {
	ID      string `json:"id" doc:"The add-on's instance: its id, with part of the account's for account add-ons"`
	Version string `json:"version" doc:"The version whose program runs"`
	// MemoryMB and CPUPercent cap the add-on's process.
	MemoryMB   int `json:"memoryMB" doc:"The most memory the add-on may use, in MB"`
	CPUPercent int `json:"cpuPercent" doc:"The most CPU time the add-on may use, in percent of one CPU"`
	// Hosts are what the add-on may reach. Without any it has no network
	// at all; with some, it reaches only their addresses, and their names
	// resolve to those addresses only, from a hosts file of its own.
	Hosts []Host `json:"hosts,omitempty" doc:"The hosts the add-on may reach, with their addresses; none means no network"`
}

// Defaults for add-ons whose manifest sets no limits.
const (
	DefaultMemoryMB   = 64
	DefaultCPUPercent = 20
	maxMemoryMB       = 8192
	maxCPUPercent     = 800
	maxNetwork        = 64
)

// Validate implements agent.Input.
func (s ServiceSpec) Validate() error {
	switch {
	case !instancePattern.MatchString(s.ID):
		return fmt.Errorf("add-on id %q", s.ID)
	case !versionPattern.MatchString(s.Version):
		return fmt.Errorf("add-on version %q", s.Version)
	case s.MemoryMB < 16 || s.MemoryMB > maxMemoryMB:
		return fmt.Errorf("memory limit %d MB must be from 16 to %d", s.MemoryMB, maxMemoryMB)
	case s.CPUPercent < 1 || s.CPUPercent > maxCPUPercent:
		return fmt.Errorf("CPU limit %d%% must be from 1 to %d", s.CPUPercent, maxCPUPercent)
	case len(s.Network()) > maxNetwork:
		return fmt.Errorf("at most %d network addresses", maxNetwork)
	}
	for _, h := range s.Hosts {
		if h.Name != "" && !hostNamePattern.MatchString(h.Name) {
			return fmt.Errorf("host name %q", h.Name)
		}
		if len(h.Addresses) == 0 {
			return fmt.Errorf("host %q has no addresses", h.Name)
		}
		for _, n := range h.Addresses {
			p, err := parseNetwork(n)
			if err != nil {
				return err
			}
			if h.Name != "" && !p.IsSingleIP() {
				return fmt.Errorf("host %s resolves to %s, which isn't one address", h.Name, n)
			}
		}
	}
	return nil
}

// Network lists every address and network the add-on may reach, sorted.
func (s ServiceSpec) Network() []string {
	var out []string
	for _, h := range s.Hosts {
		out = append(out, h.Addresses...)
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// HostsFile is where an add-on's hosts file is kept.
func HostsFile(instance string) string { return AddonDir(instance, "") + "/hosts" }

// RenderHosts returns the hosts file the add-on resolves names with: the
// hosts it may reach and nothing else.
func (s ServiceSpec) RenderHosts() []byte {
	var b strings.Builder
	b.WriteString(system.GeneratedHeader + "\n127.0.0.1 localhost\n::1 localhost\n")
	for _, h := range s.Hosts {
		if h.Name == "" {
			continue
		}
		for _, a := range h.Addresses {
			p, _ := parseNetwork(a)
			fmt.Fprintf(&b, "%s %s\n", p.Addr(), strings.ToLower(h.Name))
		}
	}
	return []byte(b.String())
}

func parseNetwork(n string) (netip.Prefix, error) {
	if p, err := netip.ParsePrefix(n); err == nil && p == p.Masked() {
		return p, nil
	}
	a, err := netip.ParseAddr(n)
	if err != nil || a.Zone() != "" {
		return netip.Prefix{}, fmt.Errorf("network %q isn't an address or a network", n)
	}
	return netip.PrefixFrom(a, a.BitLen()), nil
}

// hardening is the sandbox every add-on runs in: a user of its own, no
// privileges, a read-only view of the system and nothing of the panel's
// but the gateway socket.
var hardening = []system.UnitEntry{
	{Key: "DynamicUser", Value: "yes"},
	{Key: "NoNewPrivileges", Value: "yes"},
	{Key: "CapabilityBoundingSet", Value: ""},
	{Key: "AmbientCapabilities", Value: ""},
	{Key: "ProtectSystem", Value: "strict"},
	{Key: "ProtectHome", Value: "yes"},
	{Key: "PrivateTmp", Value: "yes"},
	{Key: "PrivateDevices", Value: "yes"},
	{Key: "PrivateUsers", Value: "yes"},
	{Key: "ProtectKernelTunables", Value: "yes"},
	{Key: "ProtectKernelModules", Value: "yes"},
	{Key: "ProtectKernelLogs", Value: "yes"},
	{Key: "ProtectControlGroups", Value: "yes"},
	{Key: "ProtectClock", Value: "yes"},
	{Key: "ProtectHostname", Value: "yes"},
	{Key: "ProtectProc", Value: "invisible"},
	{Key: "ProcSubset", Value: "pid"},
	{Key: "RestrictSUIDSGID", Value: "yes"},
	{Key: "RestrictNamespaces", Value: "yes"},
	{Key: "RestrictRealtime", Value: "yes"},
	{Key: "LockPersonality", Value: "yes"},
	{Key: "RemoveIPC", Value: "yes"},
	{Key: "KeyringMode", Value: "private"},
	{Key: "UMask", Value: "0077"},
	{Key: "SystemCallArchitectures", Value: "native"},
	{Key: "SystemCallFilter", Value: "@system-service"},
	{Key: "SystemCallFilter", Value: "~@privileged @resources @mount @clock @debug @module @raw-io @reboot @swap"},
	{Key: "SystemCallErrorNumber", Value: "EPERM"},
	{Key: "InaccessiblePaths", Value: "-" + system.StateDir},
	{Key: "InaccessiblePaths", Value: "-" + system.AgentSocket},
	{Key: "InaccessiblePaths", Value: "-" + system.EdgeSocket},
	{Key: "InaccessiblePaths", Value: "-" + system.ConfigDir},
	{Key: "IPAddressDeny", Value: "any"},
}

// RenderTemplates returns the systemd templates every add-on runs from:
// the socket systemd listens on for the panel, and the sandboxed service
// it starts on the first connection.
func RenderTemplates() (map[string][]byte, error) {
	var svc system.UnitFile
	svc.Section("Unit").
		Add("Description", "Racksail add-on %i").
		Add("Documentation", "https://vilanobeachflorida.github.io/racksail/addons/overview/").
		Add("Requires", "racksail-addon@%i.socket").
		Add("After", "racksail-addon@%i.socket network-online.target").
		Add("StartLimitIntervalSec", "120").
		Add("StartLimitBurst", "5")
	s := svc.Section("Service")
	s.Add("Type", "simple").
		Add("Slice", system.PlatformSlice).
		Add("Restart", "on-failure").
		Add("RestartSec", "3").
		Add("TimeoutStopSec", "15").
		Add("LoadCredential", "token:"+TokenDir+"/%i.token").
		Add("Environment", "RACKSAIL_ADDON=%i", "RACKSAIL_PANEL_SOCKET="+GatewaySocket).
		Add("StateDirectory", "racksail-addon/%i").
		Add("RuntimeDirectory", "racksail-addon/%i").
		Add("ReadWritePaths", GatewaySocket)
	s.AddEntries(hardening...)
	service, err := svc.Render()
	if err != nil {
		return nil, err
	}
	var sock system.UnitFile
	sock.Section("Unit").
		Add("Description", "Racksail add-on %i, for the panel").
		Add("PartOf", "racksail-addon@%i.service")
	sock.Section("Socket").
		Add("ListenStream", SocketDir+"/%i.sock").
		Add("SocketUser", "racksail").
		Add("SocketGroup", "racksail").
		Add("SocketMode", "0600").
		Add("DirectoryMode", "0750").
		Add("Service", "racksail-addon@%i.service")
	sock.Section("Install").Add("WantedBy", "sockets.target")
	socket, err := sock.Render()
	if err != nil {
		return nil, err
	}
	return map[string][]byte{ServiceTemplate: service, SocketTemplate: socket}, nil
}

// RenderDropIn returns the settings of one add-on's service: its program,
// its limits and the network it may reach.
func (s ServiceSpec) RenderDropIn() ([]byte, error) {
	if err := s.Validate(); err != nil {
		return nil, fmt.Errorf("%w: %w", err, system.ErrInvalid)
	}
	var u system.UnitFile
	svc := u.Section("Service")
	svc.Add("ExecStart", path.Join(AddonDir(s.ID, s.Version), Program)).
		Add("WorkingDirectory", AddonDir(s.ID, s.Version)).
		Add("MemoryMax", strconv.Itoa(s.MemoryMB)+"M").
		Add("MemorySwapMax", "0").
		Add("CPUQuota", strconv.Itoa(s.CPUPercent)+"%").
		Add("TasksMax", "64")
	if nets := s.Network(); len(nets) == 0 {
		svc.Add("PrivateNetwork", "yes").Add("RestrictAddressFamilies", "AF_UNIX")
	} else {
		svc.Add("RestrictAddressFamilies", "AF_UNIX AF_INET AF_INET6").
			Add("IPAddressAllow", strings.Join(nets, " ")).
			Add("BindReadOnlyPaths", HostsFile(s.ID)+":/etc/hosts")
	}
	return u.Render()
}

// EnsureAddonService writes the systemd templates add-ons run from and one
// add-on's settings, rendered here from typed input as Racksail's own
// units are, since add-ons run as users systemd makes up for them rather
// than as accounts. It doesn't start the add-on.
var EnsureAddonService = agent.Register(agent.Def[ServiceSpec, agent.Result]{
	Name:    "EnsureAddonService",
	Summary: "Set up an add-on's service",
	Description: "Writes racksail-addon@.service and racksail-addon@.socket, the sandbox every add-on runs in, and the add-on's own drop-in " +
		"with its program, memory and CPU limits and the addresses it may reach, then reloads systemd when anything changed.",
	Effects: func(in ServiceSpec) []system.Effect {
		return []system.Effect{
			{Type: system.EffectFile, Action: "write", Target: system.UnitDir + "/" + Service(in.ID) + ".d/" + DropIn + ".conf"},
			{Type: system.EffectService, Action: "configure", Target: Service(in.ID), MemoryBytes: int64(in.MemoryMB) << 20},
		}
	},
	Run: func(ctx context.Context, env *agent.Env, in ServiceSpec) (agent.Result, error) {
		templates, err := RenderTemplates()
		if err != nil {
			return agent.Result{}, err
		}
		dropIn, err := in.RenderDropIn()
		if err != nil {
			return agent.Result{}, err
		}
		units := env.System.Units
		var effects []system.Effect
		var write []func() error
		for _, name := range []string{ServiceTemplate, SocketTemplate} {
			current, err := units.ReadUnit(ctx, name)
			if err == nil && bytes.Equal(current, templates[name]) {
				continue
			}
			if err != nil && !errors.Is(err, system.ErrNotFound) {
				return agent.Result{}, err
			}
			effects = append(effects, system.Effect{Type: system.EffectFile, Action: "write", Target: system.UnitDir + "/" + name})
			write = append(write, func() error { return units.WriteUnit(ctx, name, templates[name]) })
		}
		files := env.System.Files
		hosts := in.RenderHosts()
		current, err := files.ReadFile(ctx, HostsFile(in.ID), 64<<10)
		if err != nil && !errors.Is(err, system.ErrNotFound) {
			return agent.Result{}, err
		}
		if err != nil || !bytes.Equal(current, hosts) {
			effects = append(effects, system.Effect{Type: system.EffectFile, Action: "write", Target: HostsFile(in.ID), Detail: strings.Join(in.Network(), " ")})
			write = append(write, func() error {
				if err := mkdirAll(ctx, files, AddonDir(in.ID, "")); err != nil {
					return err
				}
				return files.WriteFile(ctx, HostsFile(in.ID), hosts, 0o644, system.Owner{})
			})
		}
		dropInPath := system.UnitDir + "/" + Service(in.ID) + ".d/" + DropIn + ".conf"
		current, err = files.ReadFile(ctx, dropInPath, 64<<10)
		if err != nil && !errors.Is(err, system.ErrNotFound) {
			return agent.Result{}, err
		}
		if err != nil || !bytes.Equal(current, dropIn) {
			effects = append(effects, system.Effect{Type: system.EffectService, Action: "configure", Target: Service(in.ID),
				Detail: fmt.Sprintf("%d MB, %d%% CPU", in.MemoryMB, in.CPUPercent), MemoryBytes: int64(in.MemoryMB) << 20})
			write = append(write, func() error { return units.WriteDropIn(ctx, Service(in.ID), DropIn, dropIn) })
		}
		if len(effects) == 0 {
			return agent.Unchanged(), nil
		}
		if env.Check {
			return agent.Changed(effects...), nil
		}
		for _, w := range write {
			if err := w(); err != nil {
				return agent.Result{}, err
			}
		}
		return agent.Changed(effects...), units.DaemonReload(ctx)
	},
})

// mkdirAll creates a directory and the ones above it that are missing.
func mkdirAll(ctx context.Context, files system.Files, dir string) error {
	var missing []string
	for d := dir; d != "/"; d = path.Dir(d) {
		if _, err := files.Stat(ctx, d); err == nil {
			break
		} else if !errors.Is(err, system.ErrNotFound) {
			return err
		}
		missing = append([]string{d}, missing...)
	}
	for _, d := range missing {
		if err := files.Mkdir(ctx, d, 0o755, system.Owner{}); err != nil && !errors.Is(err, system.ErrExists) {
			return err
		}
	}
	return nil
}

// AddonInput names an add-on's instance.
type AddonInput struct {
	ID string `json:"id" doc:"The add-on's instance"`
}

// Validate implements agent.Input.
func (in AddonInput) Validate() error {
	if !instancePattern.MatchString(in.ID) {
		return fmt.Errorf("add-on id %q", in.ID)
	}
	return nil
}

// RemoveAddonService stops an add-on and removes its settings. The
// templates stay for the other add-ons.
var RemoveAddonService = agent.Register(agent.Def[AddonInput, agent.Result]{
	Name:    "RemoveAddonService",
	Summary: "Remove an add-on's service",
	Effects: func(in AddonInput) []system.Effect {
		return []system.Effect{{Type: system.EffectService, Action: "remove", Target: Service(in.ID)}}
	},
	Run: func(ctx context.Context, env *agent.Env, in AddonInput) (agent.Result, error) {
		units := env.System.Units
		dropInPath := system.UnitDir + "/" + Service(in.ID) + ".d/" + DropIn + ".conf"
		var effects []system.Effect
		for _, name := range []string{SocketUnit(in.ID), Service(in.ID)} {
			st, err := units.Status(ctx, name)
			if err != nil {
				return agent.Result{}, err
			}
			if st.Active() || st.ActiveState == "activating" {
				effects = append(effects, system.Effect{Type: system.EffectService, Action: "stop", Target: name})
				if !env.Check {
					if err := units.Stop(ctx, name); err != nil {
						return agent.Result{}, err
					}
				}
			}
			if st.UnitFileState == "enabled" {
				effects = append(effects, system.Effect{Type: system.EffectService, Action: "disable", Target: name})
				if !env.Check {
					if err := units.Disable(ctx, name); err != nil {
						return agent.Result{}, err
					}
				}
			}
		}
		if _, err := env.System.Files.Stat(ctx, dropInPath); err == nil {
			effects = append(effects, system.Effect{Type: system.EffectFile, Action: "delete", Target: dropInPath})
			if !env.Check {
				if err := units.RemoveDropIn(ctx, Service(in.ID), DropIn); err != nil {
					return agent.Result{}, err
				}
				if err := units.DaemonReload(ctx); err != nil {
					return agent.Result{}, err
				}
			}
		} else if !errors.Is(err, system.ErrNotFound) {
			return agent.Result{}, err
		}
		if len(effects) == 0 {
			return agent.Unchanged(), nil
		}
		return agent.Changed(effects...), nil
	},
})
