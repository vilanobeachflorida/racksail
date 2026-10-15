package system

import (
	"bytes"
	"errors"
	"fmt"
	"maps"
	"net/netip"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"
)

// joined renders several files into one golden file, sorted by name.
func joined(files map[string][]byte) []byte {
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	var b bytes.Buffer
	for _, n := range names {
		fmt.Fprintf(&b, "### %s\n%s\n", n, files[n])
	}
	return b.Bytes()
}

func blogSandbox() *Sandbox {
	return &Sandbox{
		User: "blog", Group: "blog",
		BindPaths:         []string{"/srv/racksail/accounts/blog/sites/shop"},
		BindReadOnlyPaths: []string{"/opt/racksail/runtimes/node/22.11.0"},
		SocketBindAllow:   []string{"tcp:3000"},
		IPAddressDeny:     []string{"169.254.169.254/32"},
		AllowWriteExecute: true,
	}
}

func TestServiceGolden(t *testing.T) {
	spec := ServiceSpec{
		Description:      "Site shop of blog",
		After:            []string{"network-online.target"},
		Wants:            []string{"network-online.target"},
		ExecStart:        []string{"/opt/racksail/runtimes/node/22.11.0/bin/node", "server.js", "--title", "Blog & shop", "--discount", "50%", "--price", "$5"},
		ExecStartPre:     [][]string{{"/opt/racksail/runtimes/node/22.11.0/bin/node", "migrate.js"}},
		ReloadSignal:     SIGHUP,
		RuntimeDirectory: "racksail-sites/blog",
		Environment:      map[string]string{"NODE_ENV": "production", "GREETING": `say "hi"`, "PORT": "3000"},
		EnvironmentFiles: []string{"/srv/racksail/accounts/blog/sites/shop/.env"},
		WorkingDirectory: "/srv/racksail/accounts/blog/sites/shop",
		Slice:            AccountSlice("blog"),
		Restart:          "on-failure",
		RestartDelay:     2 * time.Second,
		StopTimeout:      1500 * time.Millisecond,
		LogNamespace:     "blog",
		SyslogIdentifier: "blog-shop",
		Limits:           ResourceLimits{MemoryHighBytes: 384 << 20, MemoryMaxBytes: 512 << 20, CPUQuotaPercent: 150, TasksMax: 256},
		Sandbox:          blogSandbox(),
		WantedBy:         []string{"multi-user.target"},
	}
	out, err := spec.Render()
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "shop.service", out)
}

func TestQuoteExec(t *testing.T) {
	for _, tc := range []struct {
		argv []string
		want string
	}{
		{[]string{"/usr/bin/php-fpm", "--nodaemonize"}, "/usr/bin/php-fpm --nodaemonize"},
		{[]string{"/bin/echo", "two words"}, `/bin/echo "two words"`},
		{[]string{"/bin/echo", ""}, `/bin/echo ""`},
		{[]string{"/bin/echo", "100%"}, `/bin/echo "100%%"`},
		{[]string{"/bin/echo", "$HOME"}, `/bin/echo "$$HOME"`},
		{[]string{"/bin/echo", `a"b\c`}, `/bin/echo "a\"b\\c"`},
		{[]string{"/bin/echo", "-n"}, "/bin/echo -n"},
	} {
		got, err := QuoteExec(tc.argv)
		if err != nil || got != tc.want {
			t.Errorf("QuoteExec(%q) = %q, %v; want %q", tc.argv, got, err, tc.want)
		}
	}
	for _, bad := range [][]string{nil, {"php-fpm"}, {"/bin/echo", "line\nbreak"}, {"/usr/../bin/sh"}} {
		if _, err := QuoteExec(bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("QuoteExec(%q) should be refused: %v", bad, err)
		}
	}
	// A program path starting with a prefix systemd interprets is quoted.
	if got, _ := quoteArg("-x", true); got != `"-x"` {
		t.Errorf("program prefix = %q", got)
	}
}

func TestServiceSpecRefusesBadInput(t *testing.T) {
	base := ServiceSpec{Description: "x", ExecStart: []string{"/bin/true"}}
	for _, mutate := range []func(*ServiceSpec){
		func(s *ServiceSpec) { s.Description = "two\nlines" },
		func(s *ServiceSpec) { s.Type = "dbus" },
		func(s *ServiceSpec) { s.Restart = "sometimes" },
		func(s *ServiceSpec) { s.Slice = "not-a-slice.service" },
		func(s *ServiceSpec) { s.Environment = map[string]string{"BAD KEY": "x"} },
		func(s *ServiceSpec) { s.Environment = map[string]string{"KEY": "line\nbreak"} },
		func(s *ServiceSpec) { s.WorkingDirectory = "relative" },
		func(s *ServiceSpec) { s.EnvironmentFiles = []string{"/srv/a b"} },
		func(s *ServiceSpec) { s.After = []string{"../etc/passwd"} },
		func(s *ServiceSpec) { s.Limits.MemoryHighBytes, s.Limits.MemoryMaxBytes = 2, 1 },
		func(s *ServiceSpec) { s.Limits.CPUWeight = 20000 },
		func(s *ServiceSpec) { s.Limits.IODevices = []IODeviceLimit{{Device: "/dev/../etc", ReadBytesSec: 1}} },
		func(s *ServiceSpec) { s.Sandbox = &Sandbox{User: "root"} },
		func(s *ServiceSpec) {
			s.Sandbox = &Sandbox{User: "blog", SocketBindAllow: []string{"unix:/run/x.sock"}}
		},
		func(s *ServiceSpec) { s.Sandbox = &Sandbox{User: "blog", IPAddressDeny: []string{"10.0.0.0/8 any"}} },
		func(s *ServiceSpec) { s.LogNamespace = "Bad Namespace" },
		func(s *ServiceSpec) { s.ReloadSignal = "STOP" },
		func(s *ServiceSpec) { s.ReloadSignal, s.ExecReload = SIGHUP, []string{"/bin/true"} },
		func(s *ServiceSpec) { s.RuntimeDirectory = "../etc" },
	} {
		s := base
		mutate(&s)
		if _, err := s.Render(); !errors.Is(err, ErrInvalid) {
			t.Errorf("Render(%+v) should fail: %v", s, err)
		}
	}
}

func TestSocketTimerAndSliceGolden(t *testing.T) {
	sock, err := SocketSpec{
		Description: "Site shop of blog (socket)", ListenStream: []string{"/run/racksail-sites/blog/shop.sock", "127.0.0.1:8080"},
		SocketUser: "root", SocketGroup: "racksail-edge", SocketMode: 0o660, Service: "racksail-site-blog-shop.service",
		RemoveOnStop: true, Backlog: 1024, WantedBy: []string{"sockets.target"},
	}.Render()
	if err != nil {
		t.Fatal(err)
	}
	timer, err := TimerSpec{
		Description: "Nightly job of blog", OnCalendar: []string{"*-*-* 03:30:00", "Mon..Fri 12:00"},
		RandomizedDelay: 10 * time.Minute, Accuracy: time.Minute, Persistent: true,
		Unit: "racksail-job-blog-nightly.service", WantedBy: []string{"timers.target"},
	}.Render()
	if err != nil {
		t.Fatal(err)
	}
	slice, err := AccountSliceSpec("blog", ResourceLimits{
		MemoryHighBytes: 1536 << 20, MemoryMaxBytes: 2 << 30, MemorySwapMaxBytes: 512 << 20, CPUQuotaPercent: 200,
		CPUWeight: 100, IOWeight: 50, TasksMax: 512,
		IODevices: []IODeviceLimit{{Device: "/dev/nvme0n1", ReadBytesSec: 100 << 20, WriteBytesSec: 50 << 20, ReadIOPS: 2000, WriteIOPS: 1000}},
	}).Render()
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "units.golden", joined(map[string][]byte{
		"racksail-site-blog-shop.socket": sock, "racksail-job-blog-nightly.timer": timer, AccountSlice("blog"): slice,
	}))

	if _, err := (TimerSpec{Description: "x"}).Render(); !errors.Is(err, ErrInvalid) {
		t.Errorf("a timer without a schedule: %v", err)
	}
	if _, err := (TimerSpec{Description: "x", OnCalendar: []string{"daily; rm -rf /"}}).Render(); !errors.Is(err, ErrInvalid) {
		t.Errorf("a bad calendar event: %v", err)
	}
	if _, err := (SocketSpec{Description: "x", ListenStream: []string{"/run/a b.sock"}}).Render(); !errors.Is(err, ErrInvalid) {
		t.Errorf("a bad socket path: %v", err)
	}
	if !strings.HasPrefix(AccountSlice("blog"), strings.TrimSuffix(AccountsSlice, ".slice")+"-") {
		t.Errorf("account slices must nest under %s", AccountsSlice)
	}
}

func TestWakeGolden(t *testing.T) {
	app := ServiceSpec{
		Description: "Site shop of blog", Type: "exec", Restart: "always",
		ExecStart: []string{"/opt/racksail/runtimes/node/22.11.0/bin/node", "server.js"},
		Slice:     AccountSlice("blog"), Sandbox: blogSandbox(),
	}
	units, err := WakeSpec{
		Name: "racksail-site-blog-shop", Description: "Site shop of blog",
		Socket: "/run/racksail/sites/blog-shop.sock", SocketGroup: "racksail-edge",
		Upstream: "/run/racksail-sites/blog/shop.sock", IdleTimeout: 15 * time.Minute, StartTimeout: time.Minute,
		Binary: "/usr/bin/racksail", App: app,
	}.Units()
	if err != nil {
		t.Fatal(err)
	}
	if got := slices.Sorted(maps.Keys(units)); !slices.Equal(got, slices.Sorted(slices.Values(WakeUnitNames("racksail-site-blog-shop")))) {
		t.Errorf("unit names = %v", got)
	}
	golden(t, "wake.golden", joined(units))

	for _, bad := range []WakeSpec{
		{Name: "site", App: app, IdleTimeout: time.Minute, StartTimeout: time.Minute},
		{Name: "racksail-x", App: ServiceSpec{Description: "x", ExecStart: []string{"/bin/true"}}, IdleTimeout: time.Minute, StartTimeout: time.Minute},
		{Name: "racksail-x", App: app, Upstream: "/run/x.sock"},
		{Name: "racksail-x", App: app, Upstream: "not an address", IdleTimeout: time.Minute, StartTimeout: time.Minute},
	} {
		if _, err := bad.Units(); !errors.Is(err, ErrInvalid) {
			t.Errorf("Units(%s) should fail: %v", bad.Name, err)
		}
	}
}

func TestPlatformUnitsGolden(t *testing.T) {
	units, err := PlatformUnits("/usr/bin/racksail")
	if err != nil {
		t.Fatal(err)
	}
	if len(units) != len(PlatformUnitNames()) {
		t.Errorf("units = %d, names = %d", len(units), len(PlatformUnitNames()))
	}
	golden(t, "platform.golden", joined(units))
	for _, name := range []string{PanelUnit, EdgeUnit} {
		for _, want := range []string{"NoNewPrivileges=yes", "ProtectSystem=strict", "MemoryDenyWriteExecute=yes", "SystemCallFilter=@system-service", "RestrictAddressFamilies="} {
			if !bytes.Contains(units[name], []byte(want)) {
				t.Errorf("%s lacks %s", name, want)
			}
		}
	}
	if !bytes.Contains(units[PanelUnit], []byte("ImportCredential="+SecretsCredential)) {
		t.Error("the panel imports the secrets key credential")
	}
	if bytes.Contains(units[AgentUnit], []byte("User=")) {
		t.Error("the agent runs as root")
	}
	for _, bad := range []string{"racksail", "/usr/bin/rack sail", "/usr/bin/../racksail"} {
		if _, err := PlatformUnits(bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("PlatformUnits(%q): %v", bad, err)
		}
	}
}

func TestNetworkdGolden(t *testing.T) {
	no := false
	cfg := NetworkConfig{
		NetDevs: []NetDev{
			{Name: "bond0", Kind: "bond", BondMode: "802.3ad", TransmitHashPolicy: "layer3+4", MTU: 9000},
			{Name: "vlan100", Kind: "vlan", VLANID: 100},
			{Name: "br0", Kind: "bridge", STP: false},
		},
		Networks: []NetworkFile{
			{Name: "enp1s0", MatchName: "enp1s0", Bond: "bond0", Optional: true},
			{Name: "enp2s0", MatchMAC: "52:54:00:12:34:57", Bond: "bond0", Optional: true},
			{
				Name: "bond0", MatchName: "bond0", VLANs: []string{"vlan100"}, IPv6AcceptRA: &no,
				Addresses: []string{"203.0.113.10/24", "2001:db8::10/64"},
				Gateways:  []string{"203.0.113.1", "fe80::1"}, DNS: []string{"9.9.9.9", "2620:fe::fe"},
				Domains: []string{"example.com"},
				Routes: []NetworkRoute{
					{Destination: "198.51.100.0/24", Gateway: "203.0.113.254", Metric: 50},
					{Destination: "0.0.0.0/0", Gateway: "172.31.1.1", GatewayOnLink: true, Table: 100, Source: "203.0.113.11"},
				},
				Rules: []RoutingPolicy{{From: "203.0.113.11/32", Table: 100, Priority: 1000}, {FwMark: 20001, Table: 100}},
			},
			{Name: "vlan100", MatchName: "vlan100", Bridge: "br0"},
			{Name: "br0", MatchName: "br0", DHCP: "ipv4", MTU: 1500},
		},
	}
	files, err := cfg.Render()
	if err != nil {
		t.Fatal(err)
	}
	for name := range files {
		if !strings.HasPrefix(name, "50-racksail-") {
			t.Errorf("file %s isn't Racksail's", name)
		}
	}
	golden(t, "networkd.golden", RenderedNetworkd(files))

	for _, bad := range []NetworkConfig{
		{NetDevs: []NetDev{{Name: "vlan0", Kind: "vlan"}}},
		{NetDevs: []NetDev{{Name: "toolonginterfacename", Kind: "dummy"}}},
		{NetDevs: []NetDev{{Name: "bond0", Kind: "bond", BondMode: "fastest"}}},
		{Networks: []NetworkFile{{Name: "x", MatchName: "eth0", Addresses: []string{"203.0.113.10"}}}},
		{Networks: []NetworkFile{{Name: "x", MatchName: "eth0", Bond: "bond9"}}},
		{Networks: []NetworkFile{{Name: "x"}}},
		{Networks: []NetworkFile{{Name: "x", MatchName: "eth0", DNS: []string{"dns.example"}}}},
		{Networks: []NetworkFile{{Name: "x", MatchName: "eth0", Rules: []RoutingPolicy{{Table: 100}}}}},
	} {
		if _, err := bad.Render(); !errors.Is(err, ErrInvalid) {
			t.Errorf("Render(%+v) should fail: %v", bad, err)
		}
	}
}

func TestFirewallGolden(t *testing.T) {
	p := FirewallPolicy{
		Open: []PortRule{
			{Protocol: "tcp", Ports: []string{"22"}, Comment: "SSH"},
			{Protocol: "tcp", Ports: []string{"80", "443"}, Comment: "Web"},
			{Protocol: "udp", Ports: []string{"443"}, Comment: "HTTP/3"},
			{Protocol: "tcp", Ports: []string{"8443"}, From: []string{"198.51.100.0/24", "2001:db8:1::/48", "192.0.2.7"}, Comment: "Panel"},
			{Protocol: "udp", Ports: []string{"60000-61000"}, Comment: "Mosh"},
		},
		AllowFrom:  []string{"192.0.2.10", "2001:db8:ffff::/48"},
		DenyFrom:   []string{"203.0.113.0/25"},
		Bans:       []BanSet{{Name: "ssh", Timeout: time.Hour}, {Name: "panel", Timeout: 30 * time.Minute}},
		BlockSets:  []string{"countries"},
		RateLimits: []RateLimit{{Protocol: "tcp", Port: 22, Rate: 10, Per: "minute", Burst: 20}},
		Forward:    []ForwardRule{{In: "br0", Out: "enp1s0"}, {In: "enp1s0", Out: "br0"}},
		Masquerade: []MasqueradeRule{{Source: "10.10.0.0/24", Out: "enp1s0"}},
		Outbound:   []OutboundRule{{UID: 20001, Mark: 20001, SNAT4: "203.0.113.11", SNAT6: "2001:db8::11"}},
		LogDrops:   true,
	}
	out, err := p.Render()
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "firewall.nft", out)
	tables, err := CheckRuleset(out)
	if err != nil || len(tables) != 1 || tables[0].Name != FirewallTable {
		t.Errorf("tables = %v, %v", tables, err)
	}
	if ref := BanSetRef("ssh", netip.MustParseAddr("2001:db8::1")); ref.Set != "ban_ssh_v6" || !ref.Valid() {
		t.Errorf("ban set = %+v", ref)
	}

	for _, bad := range []FirewallPolicy{
		{Open: []PortRule{{Protocol: "sctp", Ports: []string{"22"}}}},
		{Open: []PortRule{{Protocol: "tcp", Ports: []string{"70000"}}}},
		{Open: []PortRule{{Protocol: "tcp", Ports: []string{"90-80"}}}},
		{Open: []PortRule{{Protocol: "tcp", Ports: []string{"22"}, Comment: `"; drop`}}},
		{AllowFrom: []string{"example.com"}},
		{Bans: []BanSet{{Name: "ssh"}}},
		{Bans: []BanSet{{Name: "Bad-Name", Timeout: time.Hour}}},
		{RateLimits: []RateLimit{{Protocol: "tcp", Port: 22, Rate: 0, Per: "minute"}}},
		{Forward: []ForwardRule{{In: "br0; drop"}}},
		{Outbound: []OutboundRule{{UID: 1000, Mark: 1, SNAT4: "203.0.113.11"}}},
		{Outbound: []OutboundRule{{UID: 20001, Mark: 1, SNAT4: "2001:db8::1"}}},
	} {
		if _, err := bad.Render(); !errors.Is(err, ErrInvalid) {
			t.Errorf("Render(%+v) should fail: %v", bad, err)
		}
	}
}

func TestTuningGolden(t *testing.T) {
	sysctl, err := RenderSysctl(KernelDefaults(4<<30, true))
	if err != nil {
		t.Fatal(err)
	}
	modules, err := RenderModulesLoad(KernelModules)
	if err != nil {
		t.Fatal(err)
	}
	zram, err := ZramConfig{}.Render()
	if err != nil {
		t.Fatal(err)
	}
	apt, err := UnattendedUpgrades{Enabled: true, Origins: []string{"origin=Racksail,label=Racksail"}, Hold: []string{"mariadb-server"}, AutoReboot: true, RebootTime: "03:15"}.Render()
	if err != nil {
		t.Fatal(err)
	}
	ntp, err := TimeSync{Fallback: []string{"time.cloudflare.com"}}.Render()
	if err != nil {
		t.Fatal(err)
	}
	all := map[string][]byte{KernelSysctlFile: sysctl, ModulesLoadFile: modules, ZramGeneratorFile: zram, TimesyncdDropIn: ntp}
	for k, v := range apt {
		all[k] = v
	}
	golden(t, "tuning.golden", joined(all))

	small := KernelDefaults(1<<30, false)
	if small["net.netfilter.nf_conntrack_max"] != "65536" || small["vm.swappiness"] != "10" || small["net.ipv4.tcp_congestion_control"] != "bbr" {
		t.Errorf("defaults for 1 GiB = %v", small)
	}
	if big := KernelDefaults(1<<40, false); big["net.netfilter.nf_conntrack_max"] != "2097152" {
		t.Errorf("conntrack is capped: %s", big["net.netfilter.nf_conntrack_max"])
	}
	if _, err := RenderSysctl(map[string]string{"net.core.somaxconn": "1\nkernel.x = 1"}); !errors.Is(err, ErrInvalid) {
		t.Errorf("a value with a line break: %v", err)
	}
	if _, err := (UnattendedUpgrades{Origins: []string{`a"; evil`}}).Render(); !errors.Is(err, ErrInvalid) {
		t.Errorf("a bad origin: %v", err)
	}
	if _, err := (UnattendedUpgrades{RebootTime: "25:00"}).Render(); !errors.Is(err, ErrInvalid) {
		t.Errorf("a bad reboot time: %v", err)
	}
	if _, err := (ZramConfig{Algorithm: "gzip"}).Render(); !errors.Is(err, ErrInvalid) {
		t.Errorf("a bad algorithm: %v", err)
	}
	if _, err := (TimeSync{Servers: []string{"pool ntp"}}).Render(); !errors.Is(err, ErrInvalid) {
		t.Errorf("a bad server: %v", err)
	}
}
