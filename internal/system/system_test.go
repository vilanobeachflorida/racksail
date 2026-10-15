package system

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite golden files")

func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run go test -update to create it)", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s differs from the golden file:\n--- got\n%s\n--- want\n%s", name, got, want)
	}
}

func TestSiteUnitGolden(t *testing.T) {
	sandbox := Sandbox{
		User:              "blog",
		Group:             "blog",
		BindPaths:         []string{"/srv/racksail/accounts/blog/sites/www"},
		BindReadOnlyPaths: []string{"/opt/racksail/runtimes/php/8.4.13"},
		SocketBindAllow:   []string{"tcp:9000"},
	}
	entries, err := sandbox.Entries()
	if err != nil {
		t.Fatal(err)
	}
	var u UnitFile
	u.Section("Unit").Add("Description", "Site www of blog").Add("After", "network.target")
	svc := u.Section("Service")
	svc.Add("Slice", AccountSlice("blog")).Add("ExecStart", "/opt/racksail/runtimes/php/8.4.13/sbin/php-fpm --nodaemonize")
	svc.AddEntries(entries...)
	u.Section("Install").Add("WantedBy", "multi-user.target")
	out, err := u.Render()
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "site.service", out)
}

func TestLegacySandbox(t *testing.T) {
	entries, err := Sandbox{User: "old", AllowWriteExecute: true, Legacy: true}.Entries()
	if err != nil {
		t.Fatal(err)
	}
	have := map[string]string{}
	for _, e := range entries {
		have[e.Key] = e.Value
	}
	for key, want := range map[string]string{
		"MemoryDenyWriteExecute": "yes",
		"ProtectProc":            "invisible",
		"ProtectSystem":          "strict",
		"NoNewPrivileges":        "yes",
		"SocketBindDeny":         "any",
		"InaccessiblePaths":      "-/run/racksail",
	} {
		if have[key] != want {
			t.Errorf("%s = %q, want %q", key, have[key], want)
		}
	}
	jit, _ := Sandbox{User: "node", AllowWriteExecute: true}.Entries()
	for _, e := range jit {
		if e.Key == "MemoryDenyWriteExecute" {
			t.Error("runtimes with a JIT need write-execute memory")
		}
	}
}

func TestSandboxRejectsUnsafeInput(t *testing.T) {
	for _, s := range []Sandbox{
		{},
		{User: "root"},
		{User: "blog", Group: "root"},
		{User: "blog", BindPaths: []string{"relative"}},
		{User: "blog", BindPaths: []string{"/srv/../etc"}},
		{User: "blog", BindPaths: []string{"/srv/a b"}},
		{User: "blog", SocketBindAllow: []string{"tcp:80 tcp:443"}},
	} {
		if _, err := s.Entries(); err == nil {
			t.Errorf("Entries(%+v) should fail", s)
		}
	}
}

func TestUnitRenderRejectsBadValues(t *testing.T) {
	for _, u := range []UnitFile{
		{Sections: []UnitSection{{Name: "Service", Entries: []UnitEntry{{"ExecStart", "/bin/true\nExecStartPre=/bin/evil"}}}}},
		{Sections: []UnitSection{{Name: "Service", Entries: []UnitEntry{{"Environment", `A=1\`}}}}},
		{Sections: []UnitSection{{Name: "Service", Entries: []UnitEntry{{"Bad Key", "x"}}}}},
		{Sections: []UnitSection{{Name: "Ser]vice"}}},
	} {
		if _, err := u.Render(); err == nil {
			t.Errorf("Render should refuse %+v", u)
		}
	}
}

func TestAptSourceGolden(t *testing.T) {
	src := AptSource{
		Name:       "php",
		URIs:       []string{"https://packages.sury.org/php/"},
		Suites:     []string{"trixie"},
		Components: []string{"main"},
		Key:        []byte("-----BEGIN PGP PUBLIC KEY BLOCK-----\n\nexample\n-----END PGP PUBLIC KEY BLOCK-----\n"),
		Packages:   []string{"php*", "libapache2-mod-php*"},
	}
	files, err := src.Render()
	if err != nil {
		t.Fatal(err)
	}
	if files.SourcesPath != "/etc/apt/sources.list.d/racksail-php.sources" || files.KeyPath != "/etc/apt/keyrings/racksail-php.asc" {
		t.Errorf("paths = %+v", files)
	}
	golden(t, "racksail-php.sources", files.Sources)
	golden(t, "racksail-php.pref", files.Prefs)

	bad := src
	bad.URIs = []string{"http://packages.sury.org/php/"}
	if err := bad.Validate(); err == nil {
		t.Error("plain http sources must be refused")
	}
	bad = src
	bad.Packages = nil
	if err := bad.Validate(); err == nil {
		t.Error("a source must say which packages it provides")
	}
}

func TestArchiveValidate(t *testing.T) {
	good := Archive{URL: "https://nodejs.org/dist/v22.11.0/node-v22.11.0-linux-x64.tar.xz", SHA256: strings.Repeat("a", 64), Size: 30 << 20, Format: "tar.xz", Dest: "/opt/racksail/runtimes/node/22.11.0", StripComponents: 1}
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*Archive){
		func(a *Archive) { a.URL = "ftp://example.test/x.tar.gz" },
		func(a *Archive) { a.SHA256 = "ABC" },
		func(a *Archive) { a.Size = 0 },
		func(a *Archive) { a.Format = "rar" },
		func(a *Archive) { a.Dest = "/opt/racksail/../etc" },
	} {
		a := good
		mutate(&a)
		if err := a.Validate(); err == nil {
			t.Errorf("Validate(%+v) should fail", a)
		}
	}
}

func TestBackendCheck(t *testing.T) {
	err := (&Backend{Name: "partial"}).Check()
	if err == nil || !strings.Contains(err.Error(), "facts") || !strings.Contains(err.Error(), "exec") {
		t.Errorf("Check = %v", err)
	}
}

func TestCommandValidate(t *testing.T) {
	if err := (Command{Path: "/usr/sbin/useradd", Args: []string{"--uid", "20001", "blog"}}).Validate(); err != nil {
		t.Error(err)
	}
	for _, c := range []Command{
		{Path: "useradd"},
		{Path: "/usr/sbin/../sbin/useradd"},
		{Path: "/bin/echo", Args: []string{"a\x00b"}},
		{Path: "/bin/echo", Env: []string{"NOEQUALS"}},
	} {
		if err := c.Validate(); err == nil {
			t.Errorf("Validate(%+v) should fail", c)
		}
	}
}

func TestNames(t *testing.T) {
	if !ValidUnitName("racksail-site-blog.service") || !ValidUnitName("racksail-acct-blog.slice") || !ValidUnitName("site@www.service") {
		t.Error("valid unit names rejected")
	}
	if ValidUnitName("../etc/passwd") || ValidUnitName("noext") {
		t.Error("invalid unit names accepted")
	}
	if !(SetRef{Family: "inet", Table: "racksail", Set: "bans"}).Valid() || (SetRef{Family: "inet", Table: "filter", Set: "x"}).Valid() {
		t.Error("set reference validation is wrong")
	}
	if got := AccountSubIDs(20001); got.Start != 1_065_536 || got.Count != 65_536 {
		t.Errorf("AccountSubIDs = %+v", got)
	}
	if !ValidSysctlKey("net.ipv4.tcp_congestion_control") || ValidSysctlKey("../../etc") {
		t.Error("sysctl key validation is wrong")
	}
}
