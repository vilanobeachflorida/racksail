package config

import (
	"bytes"
	"cmp"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/vilanobeachflorida/racksail/internal/system"
)

// Config is the contents of racksail.toml.
type Config struct {
	Panel Panel `toml:"panel"`
	API   API   `toml:"api"`
	Agent Agent `toml:"agent"`
	Edge  Edge  `toml:"edge"`
	// Paths aren't read from the file: see Paths.
	Paths     Paths     `toml:"-"`
	Retention Retention `toml:"retention"`
	Webhooks  Webhooks  `toml:"webhooks"`
	Updates   Updates   `toml:"updates"`
	Log       Log       `toml:"log"`
}

// Webhooks configures outgoing webhooks.
type Webhooks struct {
	// AllowNetworks are networks webhooks may send events to besides public
	// addresses, such as "10.0.0.0/8" for a receiver on the private network
	// or "127.0.0.1" for one on the server itself. Loopback, private and
	// link-local addresses are refused unless listed, so a webhook can't be
	// used to reach the server's own services or the network behind it.
	AllowNetworks []string `toml:"allow_networks"`
}

// AllowedNetworks returns allow_networks as networks; an address on its own
// is a network of one.
func (w Webhooks) AllowedNetworks() ([]netip.Prefix, error) {
	return networks("webhooks.allow_networks", w.AllowNetworks)
}

// API configures the panel API.
type API struct {
	// RateLimit is how many requests a minute each signed-in user and each
	// API token may make. Zero turns the limit off.
	RateLimit int `toml:"rate_limit"`
	// AnonymousRateLimit is how many requests a minute each address may
	// make without signing in. Zero turns the limit off.
	AnonymousRateLimit int `toml:"anonymous_rate_limit"`
}

// Retention says how long the panel keeps its records.
type Retention struct {
	// Events is how long the event timeline goes back.
	Events Duration `toml:"events"`
	// Audit is how long audit records are kept.
	Audit Duration `toml:"audit"`
	// Jobs is how long finished jobs and their logs are kept.
	Jobs Duration `toml:"jobs"`
	// ChangeSets is how long applied change sets are kept, and so how far
	// back changes can be undone.
	ChangeSets Duration `toml:"change_sets"`
	// Deliveries is how long webhook deliveries are kept.
	Deliveries Duration `toml:"deliveries"`
}

// Duration is a length of time written like "90d", "12h" or "1h30m": a
// number and a unit, d for days, h, m and s, as often as needed.
type Duration time.Duration

// UnmarshalText implements encoding.TextUnmarshaler.
func (d *Duration) UnmarshalText(text []byte) error {
	v, err := ParseDuration(string(text))
	if err != nil {
		return err
	}
	*d = Duration(v)
	return nil
}

// MarshalText implements encoding.TextMarshaler.
func (d Duration) MarshalText() ([]byte, error) { return []byte(d.String()), nil }

// String writes the duration in whole days when it is a number of days.
func (d Duration) String() string {
	v := time.Duration(d)
	if v > 0 && v%(24*time.Hour) == 0 {
		return strconv.FormatInt(int64(v/(24*time.Hour)), 10) + "d"
	}
	return v.String()
}

// ParseDuration reads a duration such as "90d" or "1d12h".
func ParseDuration(s string) (time.Duration, error) {
	var days time.Duration
	rest := s
	if i := strings.IndexByte(s, 'd'); i >= 0 {
		n, err := strconv.ParseInt(s[:i], 10, 64)
		if err != nil || n < 0 || n > 36500 {
			return 0, fmt.Errorf("%q isn't a duration such as 90d, 12h or 30m", s)
		}
		days, rest = time.Duration(n)*24*time.Hour, s[i+1:]
	}
	if rest == "" {
		if days == 0 && s == "" {
			return 0, fmt.Errorf("%q isn't a duration such as 90d, 12h or 30m", s)
		}
		return days, nil
	}
	v, err := time.ParseDuration(rest)
	if err != nil || v < 0 {
		return 0, fmt.Errorf("%q isn't a duration such as 90d, 12h or 30m", s)
	}
	return days + v, nil
}

// Panel configures the panel role.
type Panel struct {
	// Listen is where the panel serves HTTPS, [::]:8443 by default.
	Listen string `toml:"listen"`
	// Hostname is the name the panel is reached at, used for its
	// certificate and links in notifications.
	Hostname string `toml:"hostname"`
	// TLSCert and TLSKey point at a certificate to use instead of the one
	// the panel generates or obtains.
	TLSCert string `toml:"tls_cert"`
	TLSKey  string `toml:"tls_key"`
	// AllowFrom limits who may reach the panel to these addresses and
	// networks, such as "198.51.100.0/24", when it isn't empty. Requests from
	// anywhere else are refused, those from the server itself included, so
	// list 127.0.0.1 and ::1 to use the panel from the server.
	AllowFrom []string `toml:"allow_from"`
	// Insecure serves plain HTTP. Only development mode sets it.
	Insecure bool `toml:"-"`
}

// Agent configures the agent role.
type Agent struct {
	Socket string `toml:"socket"`
}

// Edge configures the edge role.
type Edge struct {
	HTTP   string `toml:"http"`
	HTTPS  string `toml:"https"`
	Socket string `toml:"socket"`
}

// Paths are where Racksail keeps its files. On a server they are always the
// standard locations of decision D1, because the agent's allowed roots, the
// installer, the add-ons' sandboxes and the systemd units are fixed there
// when the binary is built: a path racksail.toml moved would only move the
// panel's half of it, so the file can't set them. racksail dev puts them
// under its data directory (Dev).
type Paths struct {
	Config    string `toml:"config"`
	Generated string `toml:"generated"`
	State     string `toml:"state"`
	Accounts  string `toml:"accounts"`
	Runtimes  string `toml:"runtimes"`
	Modules   string `toml:"modules"`
	Run       string `toml:"run"`
	DataDisks string `toml:"data_disks"`
}

// AllowedNetworks returns allow_from as networks; an address on its own is a
// network of one. IPv4 addresses written as IPv6 ones count as IPv4.
func (p Panel) AllowedNetworks() ([]netip.Prefix, error) {
	return networks("panel.allow_from", p.AllowFrom)
}

// networks reads a list of addresses and networks.
func networks(setting string, list []string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, n := range list {
		if prefix, err := netip.ParsePrefix(n); err == nil {
			if addr := prefix.Addr(); addr.Is4In6() && prefix.Bits() >= 96 {
				prefix = netip.PrefixFrom(addr.Unmap(), prefix.Bits()-96)
			}
			out = append(out, prefix.Masked())
			continue
		}
		addr, err := netip.ParseAddr(n)
		if err != nil || addr.Zone() != "" {
			return nil, fmt.Errorf("%s: %q isn't an address or network", setting, n)
		}
		addr = addr.Unmap()
		out = append(out, netip.PrefixFrom(addr, addr.BitLen()))
	}
	return out, nil
}

// Database returns the path of the panel database.
func (p Paths) Database() string { return filepath.Join(p.State, "state.db") }

// Updates configures self-updates.
type Updates struct {
	// Channel is stable, beta or nightly.
	Channel string `toml:"channel"`
}

// Log configures logging.
type Log struct {
	// Level is debug, info, warn or error.
	Level string `toml:"level"`
	// Format is text or json.
	Format string `toml:"format"`
}

// Default returns the configuration of a server with an empty file.
func Default() Config {
	day := Duration(24 * time.Hour)
	return Config{
		Panel: Panel{Listen: "[::]:8443"},
		API:   API{RateLimit: 600, AnonymousRateLimit: 120},
		Retention: Retention{
			Events: 90 * day, Audit: 400 * day, Jobs: 30 * day, ChangeSets: 30 * day, Deliveries: 30 * day,
		},
		Agent: Agent{Socket: system.AgentSocket},
		Edge:  Edge{HTTP: "[::]:80", HTTPS: "[::]:443", Socket: system.EdgeSocket},
		Paths: Paths{
			Config: system.ConfigDir, Generated: system.GeneratedDir, State: system.StateDir,
			Accounts: system.AccountsDir, Runtimes: system.RuntimesDir, Modules: system.ModulesDir,
			Run: system.RunDir, DataDisks: system.DataDisksDir,
		},
		Updates: Updates{Channel: "stable"},
		Log:     Log{Level: "info", Format: "json"},
	}
}

// DevAddresses are where `racksail dev` listens. Empty fields get the
// defaults: the panel on 127.0.0.1:7070, and the edge on 127.0.0.1:7080 for
// HTTP and 127.0.0.1:7443 for HTTPS.
type DevAddresses struct {
	Panel     string
	EdgeHTTP  string
	EdgeHTTPS string
}

// Default addresses of `racksail dev`.
const (
	DevPanel     = "127.0.0.1:7070"
	DevEdgeHTTP  = "127.0.0.1:7080"
	DevEdgeHTTPS = "127.0.0.1:7443"
)

// Dev returns the configuration of `racksail dev`: plain HTTP on the given
// addresses and every path under dataDir.
func Dev(dataDir string, addrs DevAddresses) (Config, error) {
	abs, err := filepath.Abs(dataDir)
	if err != nil {
		return Config{}, err
	}
	addrs.Panel = cmp.Or(addrs.Panel, DevPanel)
	addrs.EdgeHTTP = cmp.Or(addrs.EdgeHTTP, DevEdgeHTTP)
	addrs.EdgeHTTPS = cmp.Or(addrs.EdgeHTTPS, DevEdgeHTTPS)
	c := Default()
	c.Panel = Panel{Listen: addrs.Panel, Hostname: "localhost", Insecure: true}
	c.Agent = Agent{Socket: filepath.Join(abs, "run", "agent.sock")}
	c.Edge = Edge{HTTP: addrs.EdgeHTTP, HTTPS: addrs.EdgeHTTPS, Socket: filepath.Join(abs, "run", "edge.sock")}
	c.Paths = Paths{
		Config: filepath.Join(abs, "etc"), Generated: filepath.Join(abs, "etc", "generated"), State: filepath.Join(abs, "state"),
		Accounts: filepath.Join(abs, "srv", "accounts"), Runtimes: filepath.Join(abs, "opt", "runtimes"),
		Modules: filepath.Join(abs, "opt", "modules"), Run: filepath.Join(abs, "run"), DataDisks: filepath.Join(abs, "mnt"),
	}
	c.Updates = Updates{Channel: "nightly"}
	c.Log = Log{Level: "debug", Format: "text"}
	// A developer's webhook receivers usually run on the same machine.
	c.Webhooks = Webhooks{AllowNetworks: []string{"127.0.0.0/8", "::1"}}
	// Everyone racksail dev signs in is the same person, the developer's
	// browser tabs and test suites at once, so that person isn't limited.
	c.API.RateLimit = 0
	return c, c.Validate()
}

// Parse reads a configuration file's contents on top of the defaults.
func Parse(data []byte) (Config, error) {
	c := Default()
	md, err := toml.NewDecoder(bytes.NewReader(data)).Decode(&c)
	if err != nil {
		return Config{}, fmt.Errorf("reading racksail.toml: %w", err)
	}
	var unknown, paths []string
	for _, k := range md.Undecoded() {
		switch {
		case len(k) > 0 && k[0] == "paths":
			if len(k) > 1 {
				paths = append(paths, k.String())
			}
		default:
			unknown = append(unknown, k.String())
		}
	}
	var errs []error
	if len(unknown) > 0 {
		errs = append(errs, fmt.Errorf("racksail.toml has unknown settings: %s", strings.Join(unknown, ", ")))
	}
	if len(paths) > 0 || md.IsDefined("paths") {
		if len(paths) == 0 {
			paths = []string{"paths"}
		}
		errs = append(errs, fmt.Errorf("racksail.toml can't set %s: Racksail keeps its files where the agent, the installer and the systemd units expect them, such as %s; remove the setting",
			strings.Join(paths, ", "), system.StateDir))
	}
	if len(errs) > 0 {
		return Config{}, errors.Join(errs...)
	}
	return c, c.Validate()
}

// Load reads the file at path. A missing file gives the defaults.
func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		c := Default()
		return c, c.Validate()
	}
	if err != nil {
		return Config{}, err
	}
	return Parse(data)
}

// Validate checks addresses, paths and choices.
func (c Config) Validate() error {
	var errs []error
	checkAddr := func(name, addr string, optional bool) {
		if addr == "" && optional {
			return
		}
		if _, _, err := net.SplitHostPort(addr); err != nil {
			errs = append(errs, fmt.Errorf("%s: %q isn't a host:port address", name, addr))
		}
	}
	checkAddr("panel.listen", c.Panel.Listen, false)
	checkAddr("edge.http", c.Edge.HTTP, false)
	checkAddr("edge.https", c.Edge.HTTPS, false)
	if _, err := c.Panel.AllowedNetworks(); err != nil {
		errs = append(errs, err)
	}
	if _, err := c.Webhooks.AllowedNetworks(); err != nil {
		errs = append(errs, err)
	}
	if (c.Panel.TLSCert == "") != (c.Panel.TLSKey == "") {
		errs = append(errs, errors.New("panel.tls_cert and panel.tls_key go together"))
	}
	for name, p := range map[string]string{
		"agent.socket": c.Agent.Socket, "edge.socket": c.Edge.Socket, "paths.config": c.Paths.Config,
		"paths.generated": c.Paths.Generated, "paths.state": c.Paths.State, "paths.accounts": c.Paths.Accounts,
		"paths.runtimes": c.Paths.Runtimes, "paths.modules": c.Paths.Modules, "paths.run": c.Paths.Run,
		"paths.data_disks": c.Paths.DataDisks,
	} {
		if !filepath.IsAbs(p) {
			errs = append(errs, fmt.Errorf("%s: %q must be an absolute path", name, p))
		}
	}
	if !slices.Contains([]string{"stable", "beta", "nightly"}, c.Updates.Channel) {
		errs = append(errs, fmt.Errorf("updates.channel: %q must be stable, beta or nightly", c.Updates.Channel))
	}
	if !slices.Contains([]string{"debug", "info", "warn", "error"}, c.Log.Level) {
		errs = append(errs, fmt.Errorf("log.level: %q must be debug, info, warn or error", c.Log.Level))
	}
	if !slices.Contains([]string{"text", "json"}, c.Log.Format) {
		errs = append(errs, fmt.Errorf("log.format: %q must be text or json", c.Log.Format))
	}
	if c.API.RateLimit < 0 || c.API.AnonymousRateLimit < 0 {
		errs = append(errs, errors.New("api.rate_limit and api.anonymous_rate_limit can't be negative; 0 turns a limit off"))
	}
	for _, r := range []struct {
		name string
		v    Duration
		min  time.Duration
	}{
		{"retention.events", c.Retention.Events, 24 * time.Hour},
		{"retention.audit", c.Retention.Audit, 30 * 24 * time.Hour},
		{"retention.jobs", c.Retention.Jobs, time.Hour},
		{"retention.change_sets", c.Retention.ChangeSets, time.Hour},
		{"retention.deliveries", c.Retention.Deliveries, time.Hour},
	} {
		if time.Duration(r.v) < r.min {
			errs = append(errs, fmt.Errorf("%s: keep at least %s", r.name, Duration(r.min)))
		}
	}
	return errors.Join(errs...)
}
