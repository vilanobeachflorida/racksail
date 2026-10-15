package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
allow_from = ["198.51.100.0/24", "2001:db8::1"]

[updates]
channel = "beta"
mirrors = ["https://mirror.example.test/racksail"]
`))
	if err != nil {
		t.Fatal(err)
	}
	if c.Panel.Hostname != "panel.example.test" || c.Updates.Channel != "beta" || c.Agent.Socket != "/run/racksail/agent.sock" {
		t.Errorf("parsed = %+v", c)
	}
	for _, bad := range []string{
		"[panel]\nlistne = \":8443\"\n",
		"[updates]\nchannel = \"edge\"\n",
		"[panel]\nlisten = \"8443\"\n",
		"[panel]\ntls_cert = \"/etc/cert.pem\"\n",
		"[paths]\nstate = \"relative\"\n",
		"[log]\nlevel = \"loud\"\n",
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

func TestDev(t *testing.T) {
	dir := t.TempDir()
	c, err := Dev(dir, DevAddresses{})
	if err != nil {
		t.Fatal(err)
	}
	if !c.Panel.Insecure || c.Panel.Listen != "127.0.0.1:7070" || c.Edge.HTTP != "127.0.0.1:7080" || !strings.HasPrefix(c.Paths.State, dir) || !strings.HasPrefix(c.Agent.Socket, dir) {
		t.Errorf("dev = %+v", c)
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
