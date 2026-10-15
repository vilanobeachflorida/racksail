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
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/vilanobeachflorida/racksail/internal/system"
)

// Config is the contents of racksail.toml.
type Config struct {
	Panel   Panel   `toml:"panel"`
	Agent   Agent   `toml:"agent"`
	Edge    Edge    `toml:"edge"`
	Paths   Paths   `toml:"paths"`
	Updates Updates `toml:"updates"`
	Log     Log     `toml:"log"`
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

// Paths are where Racksail keeps its files.
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
	var out []netip.Prefix
	for _, n := range p.AllowFrom {
		if prefix, err := netip.ParsePrefix(n); err == nil {
			if addr := prefix.Addr(); addr.Is4In6() && prefix.Bits() >= 96 {
				prefix = netip.PrefixFrom(addr.Unmap(), prefix.Bits()-96)
			}
			out = append(out, prefix.Masked())
			continue
		}
		addr, err := netip.ParseAddr(n)
		if err != nil || addr.Zone() != "" {
			return nil, fmt.Errorf("panel.allow_from: %q isn't an address or network", n)
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
	return Config{
		Panel: Panel{Listen: "[::]:8443"},
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
	return c, c.Validate()
}

// Parse reads a configuration file's contents on top of the defaults.
func Parse(data []byte) (Config, error) {
	c := Default()
	md, err := toml.NewDecoder(bytes.NewReader(data)).Decode(&c)
	if err != nil {
		return Config{}, fmt.Errorf("reading racksail.toml: %w", err)
	}
	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		keys := make([]string, len(undecoded))
		for i, k := range undecoded {
			keys[i] = k.String()
		}
		return Config{}, fmt.Errorf("racksail.toml has unknown settings: %s", strings.Join(keys, ", "))
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
	return errors.Join(errs...)
}
