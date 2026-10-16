package config

import (
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestDefaults(t *testing.T) {
	c := Default()
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if c.Panel.Listen != "[::]:8443" || c.Paths.Database() != "/var/lib/racksail/state.db" || c.Updates.Channel != "stable" {
		t.Errorf("defaults = %+v", c)
	}
	missing, err := Load(filepath.Join(t.TempDir(), "racksail.toml"))
	if err != nil || missing.Panel.Listen != c.Panel.Listen {
		t.Errorf("a missing file should give the defaults: %+v, %v", missing, err)
	}
}

func TestParse(t *testing.T) {
	c, err := Parse([]byte(`
[panel]
listen = "203.0.113.10:8443"
hostname = "panel.example.test"
allow_from = ["198.51.100.0/24", "2001:db8::1", "::ffff:203.0.113.7"]

[updates]
channel = "beta"
`))
	if err != nil {
		t.Fatal(err)
	}
	if c.Panel.Hostname != "panel.example.test" || c.Updates.Channel != "beta" || c.Agent.Socket != "/run/racksail/agent.sock" {
		t.Errorf("parsed = %+v", c)
	}
	nets, err := c.Panel.AllowedNetworks()
	want := []netip.Prefix{netip.MustParsePrefix("198.51.100.0/24"), netip.MustParsePrefix("2001:db8::1/128"), netip.MustParsePrefix("203.0.113.7/32")}
	if err != nil || !slices.Equal(nets, want) {
		t.Errorf("allowed networks = %v, %v", nets, err)
	}
	for _, bad := range []string{
		"[panel]\nlistne = \":8443\"\n",
		"[updates]\nchannel = \"edge\"\n",
		"[panel]\nlisten = \"8443\"\n",
		"[panel]\ntls_cert = \"/etc/cert.pem\"\n",
		"[panel]\nallow_from = [\"office\"]\n",
		"[paths]\nstate = \"relative\"\n",
		"[log]\nlevel = \"loud\"\n",
		// Settings nothing acts on are refused rather than silently
		// ignored.
		"[agent]\nlisten = \"[::]:7443\"\n",
		"[updates]\nmirrors = [\"https://mirror.example.test\"]\n",
		"[updates]\nautomatic = true\n",
		"not toml",
	} {
		if _, err := Parse([]byte(bad)); err == nil {
			t.Errorf("Parse(%q) should fail", bad)
		}
	}
	_, err = Parse([]byte("[panel]\nlistne = \":8443\"\n"))
	if !strings.Contains(err.Error(), "panel.listne") {
		t.Errorf("unknown keys should be named: %v", err)
	}
}

// Where Racksail keeps its files is fixed on a server: the agent's allowed
// roots, the installer and the systemd units would never follow a path the
// file moved, so the file can't move any.
func TestPathsAreFixed(t *testing.T) {
	for _, key := range []string{"config", "generated", "state", "accounts", "runtimes", "modules", "run", "data_disks"} {
		_, err := Parse([]byte("[paths]\n" + key + " = \"/srv/elsewhere\"\n"))
		if err == nil || !strings.Contains(err.Error(), "paths."+key) || !strings.Contains(err.Error(), "can't set") {
			t.Errorf("paths.%s: %v", key, err)
		}
	}
	if _, err := Parse([]byte("[paths]\n")); err == nil {
		t.Error("an empty [paths] table is refused too")
	}
	// Both kinds of problems are named at once.
	_, err := Parse([]byte("[paths]\nstate = \"/srv/state\"\n[panel]\nlistne = \":8443\"\n"))
	if err == nil || !strings.Contains(err.Error(), "paths.state") || !strings.Contains(err.Error(), "panel.listne") {
		t.Errorf("a moved path and an unknown setting: %v", err)
	}
	c, err := Parse([]byte("[log]\nlevel = \"warn\"\n"))
	if err != nil || c.Paths != Default().Paths || c.Paths.State != "/var/lib/racksail" {
		t.Errorf("a file without paths keeps the standard ones: %+v, %v", c.Paths, err)
	}
	dev, err := Dev(t.TempDir(), DevAddresses{})
	if err != nil || dev.Paths.State == Default().Paths.State {
		t.Errorf("racksail dev keeps its files in its data directory: %+v, %v", dev.Paths, err)
	}
}

func TestAPIAndRetention(t *testing.T) {
	c := Default()
	if c.API.RateLimit != 600 || c.API.AnonymousRateLimit != 120 || time.Duration(c.Retention.Audit) != 400*24*time.Hour {
		t.Errorf("defaults = %+v %+v", c.API, c.Retention)
	}
	if _, err := Parse([]byte("[retention]\naudit = \"2y\"\n")); err == nil {
		t.Error("years aren't a unit")
	}
	c, err := Parse([]byte(`
[api]
rate_limit = 0
anonymous_rate_limit = 30

[retention]
events = "7d"
audit = "365d"
jobs = "36h"
change_sets = "1d12h"
deliveries = "90m"
`))
	if err != nil {
		t.Fatal(err)
	}
	if c.API.RateLimit != 0 || c.API.AnonymousRateLimit != 30 {
		t.Errorf("api = %+v", c.API)
	}
	for name, got := range map[string]Duration{"events": c.Retention.Events, "audit": c.Retention.Audit, "jobs": c.Retention.Jobs, "change sets": c.Retention.ChangeSets, "deliveries": c.Retention.Deliveries} {
		want := map[string]time.Duration{"events": 7 * 24 * time.Hour, "audit": 365 * 24 * time.Hour, "jobs": 36 * time.Hour, "change sets": 36 * time.Hour, "deliveries": 90 * time.Minute}[name]
		if time.Duration(got) != want {
			t.Errorf("%s = %s, want %s", name, got, want)
		}
	}
	if c.Retention.Events.String() != "7d" || c.Retention.Jobs.String() != "36h0m0s" {
		t.Errorf("written back = %s, %s", c.Retention.Events, c.Retention.Jobs)
	}
	for _, bad := range []string{
		"[api]\nrate_limit = -1\n",
		"[retention]\nevents = \"1h\"\n",
		"[retention]\naudit = \"7d\"\n",
		"[retention]\njobs = \"soon\"\n",
		"[retention]\njobs = \"d\"\n",
		"[retention]\njobs = \"-3d\"\n",
		"[retention]\njobs = \"\"\n",
	} {
		if _, err := Parse([]byte(bad)); err == nil {
			t.Errorf("Parse(%q) should fail", bad)
		}
	}
}

func TestDev(t *testing.T) {
	dir := t.TempDir()
	c, err := Dev(dir, DevAddresses{})
	if err != nil {
		t.Fatal(err)
	}
	if !c.Panel.Insecure || c.Panel.Listen != "127.0.0.1:7070" || c.Edge.HTTP != "127.0.0.1:7080" || !strings.HasPrefix(c.Paths.State, dir) || !strings.HasPrefix(c.Agent.Socket, dir) {
		t.Errorf("dev = %+v", c)
	}
	if c.API.RateLimit != 0 || c.API.AnonymousRateLimit != Default().API.AnonymousRateLimit {
		t.Errorf("racksail dev limits = %+v, want the signed-in person unlimited", c.API)
	}
	other, err := Dev(dir, DevAddresses{Panel: "127.0.0.1:7170", EdgeHTTP: "127.0.0.1:7180", EdgeHTTPS: "127.0.0.1:0"})
	if err != nil || other.Panel.Listen != "127.0.0.1:7170" || other.Edge.HTTP != "127.0.0.1:7180" || other.Edge.HTTPS != "127.0.0.1:0" {
		t.Errorf("addresses can be chosen: %+v, %v", other, err)
	}
	if _, err := Dev(dir, DevAddresses{EdgeHTTP: "7080"}); err == nil {
		t.Error("addresses are checked")
	}
	rel, err := Dev(".dev", DevAddresses{})
	if err != nil || !filepath.IsAbs(rel.Paths.State) {
		t.Errorf("relative data directories become absolute: %+v, %v", rel.Paths, err)
	}
	path := filepath.Join(dir, "racksail.toml")
	if err := os.WriteFile(path, []byte("[log]\nlevel = \"debug\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil || loaded.Log.Level != "debug" {
		t.Errorf("load = %+v, %v", loaded.Log, err)
	}
}

func TestWebhookNetworks(t *testing.T) {
	if nets, err := Default().Webhooks.AllowedNetworks(); err != nil || len(nets) != 0 {
		t.Errorf("by default webhooks reach public addresses only: %v, %v", nets, err)
	}
	c, err := Parse([]byte("[webhooks]\nallow_networks = [\"10.0.0.0/8\", \"127.0.0.1\", \"fd00::/8\"]\n"))
	if err != nil {
		t.Fatal(err)
	}
	nets, err := c.Webhooks.AllowedNetworks()
	want := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("127.0.0.1/32"), netip.MustParsePrefix("fd00::/8")}
	if err != nil || !slices.Equal(nets, want) {
		t.Errorf("allowed = %v, %v", nets, err)
	}
	if _, err := Parse([]byte("[webhooks]\nallow_networks = [\"the office\"]\n")); err == nil || !strings.Contains(err.Error(), "webhooks.allow_networks") {
		t.Errorf("a bad network: %v", err)
	}
	dev, err := Dev(t.TempDir(), DevAddresses{})
	if err != nil {
		t.Fatal(err)
	}
	if nets, _ := dev.Webhooks.AllowedNetworks(); !slices.Contains(nets, netip.MustParsePrefix("127.0.0.0/8")) {
		t.Errorf("racksail dev reaches receivers on the same machine: %v", nets)
	}
}
