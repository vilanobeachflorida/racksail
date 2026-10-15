package system

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseUnitFileReadsLikeSystemd(t *testing.T) {
	u, err := ParseUnitFile([]byte("# comment\n[Unit]\n  Description = A site \nAfter=a.service \\\n# skipped inside a continuation\n  b.service\n; another comment\n\n[Service]\nUser=blog\nExecStart=/bin/echo one\\\\\nEnvironment=X=1\n"))
	if err != nil {
		t.Fatal(err)
	}
	want := []UnitSection{
		{Name: "Unit", Entries: []UnitEntry{{"Description", "A site"}, {"After", "a.service    b.service"}}},
		{Name: "Service", Entries: []UnitEntry{{"User", "blog"}, {"ExecStart", `/bin/echo one\\`}, {"Environment", "X=1"}}},
	}
	if len(u.Sections) != len(want) {
		t.Fatalf("sections = %+v", u.Sections)
	}
	for i, s := range want {
		got := u.Sections[i]
		if got.Name != s.Name || len(got.Entries) != len(s.Entries) {
			t.Fatalf("section %d = %+v, want %+v", i, got, s)
		}
		for j, e := range s.Entries {
			if got.Entries[j] != e {
				t.Errorf("entry %s = %+v, want %+v", e.Key, got.Entries[j], e)
			}
		}
	}

	for _, bad := range []string{
		"[Service]\nUser=blog\rUser=root\n",
		"[Service]\nUser=blog\x00\n",
		"\xef\xbb\xbf[Service]\n",
		"User=root\n[Service]\n",
		"[Service]\njust words\n",
		"[Service\nUser=blog\n",
		"[Service]\n=value\n",
		"[Service]\nUser=\xff\n",
	} {
		if _, err := ParseUnitFile([]byte(bad)); !errors.Is(err, ErrInvalid) {
			t.Errorf("ParseUnitFile(%q) should fail: %v", bad, err)
		}
	}
}

func testUnitRules() UnitRules {
	return UnitRules{
		Dependency: func(name string) bool { return name == "mariadb.service" },
		MountPoint: func(p string) bool { return strings.HasPrefix(p, DataDisksDir+"/") },
	}
}

const goodService = "[Unit]\nDescription=Blog\nAfter=network-online.target mariadb.service\n\n[Service]\nUser=blog\nGroup=blog\nExecStart=/usr/bin/php -S 127.0.0.1:3000\nExecStartPre=-/usr/bin/true\n\n[Install]\nWantedBy=multi-user.target\n"

func TestCheckUnitFileAcceptsWhatRacksailRenders(t *testing.T) {
	id, err := CheckUnitFile("racksail-site-blog.service", []byte(goodService), testUnitRules())
	if err != nil {
		t.Fatal(err)
	}
	if id != (UnitIdentity{User: "blog", Group: "blog"}) {
		t.Errorf("identity = %+v", id)
	}

	// Every unit the renderers produce for accounts passes the checks.
	rendered := map[string][]byte{}
	for _, name := range []string{"shop.service"} {
		data, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			t.Fatal(err)
		}
		rendered["racksail-site-blog-shop.service"] = data
	}
	app := ServiceSpec{Description: "Site shop of blog", ExecStart: []string{"/opt/racksail/runtimes/node/22.11.0/bin/node", "server.js"}, Slice: AccountSlice("blog"), Sandbox: blogSandbox()}
	wake, err := WakeSpec{
		Name: "racksail-site-blog-shop", Description: "Site shop of blog", Socket: "/run/racksail/sites/blog-shop.sock",
		SocketGroup: "racksail-edge", Upstream: "/run/racksail-sites/blog/shop.sock", IdleTimeout: time.Minute,
		StartTimeout: time.Minute, Binary: "/usr/bin/racksail", App: app,
	}.Units()
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range wake {
		rendered[name] = data
	}
	sock, err := SocketSpec{Description: "x", ListenStream: []string{"/run/racksail-sites/blog/shop.sock", "127.0.0.1:8080"},
		SocketUser: "root", SocketGroup: "racksail-edge", SocketMode: 0o660, Service: "racksail-site-blog-shop.service",
		RemoveOnStop: true, Backlog: 1024, WantedBy: []string{"sockets.target"}}.Render()
	if err != nil {
		t.Fatal(err)
	}
	rendered["racksail-site-blog-shop2.socket"] = sock
	timer, err := TimerSpec{Description: "x", OnCalendar: []string{"*-*-* 03:30:00"}, RandomizedDelay: time.Minute,
		Persistent: true, Unit: "racksail-job-blog.service", WantedBy: []string{"timers.target"}}.Render()
	if err != nil {
		t.Fatal(err)
	}
	rendered["racksail-job-blog.timer"] = timer
	slice, err := AccountSliceSpec("blog", ResourceLimits{MemoryMaxBytes: 1 << 30, CPUQuotaPercent: 200, TasksMax: 512,
		IODevices: []IODeviceLimit{{Device: "/dev/nvme0n1", ReadBytesSec: 1 << 20, WriteIOPS: 100}}}).Render()
	if err != nil {
		t.Fatal(err)
	}
	rendered[AccountSlice("blog")] = slice
	for name, data := range rendered {
		if _, err := CheckUnitFile(name, data, testUnitRules()); err != nil {
			t.Errorf("%s: %v\n%s", name, err, data)
		}
	}

	mount := "[Unit]\nDescription=Data disk\n[Mount]\nWhat=/dev/disk/by-uuid/0a1b2c3d-1111-2222-3333-444455556666\nWhere=/mnt/racksail/data\nType=btrfs\nOptions=noatime,compress=zstd:3,nosuid,nodev\n[Install]\nWantedBy=multi-user.target\n"
	if _, err := CheckUnitFile("mnt-racksail-data.mount", []byte(mount), testUnitRules()); err != nil {
		t.Errorf("mount: %v", err)
	}
	automount := "[Automount]\nWhere=/mnt/racksail/data\nTimeoutIdleSec=10min\n"
	if _, err := CheckUnitFile("mnt-racksail-data.automount", []byte(automount), testUnitRules()); err != nil {
		t.Errorf("automount: %v", err)
	}
}

func TestCheckUnitFileRefusesWhatCouldTakeTheServer(t *testing.T) {
	service := func(lines string) string { return "[Unit]\nDescription=x\n[Service]\n" + lines }
	for _, tc := range []struct {
		name, unit, content string
	}{
		{"no user", "racksail-x.service", service("ExecStart=/bin/true\n")},
		{"root", "racksail-x.service", service("User=root\nExecStart=/bin/true\n")},
		{"user overridden by root", "racksail-x.service", service("User=blog\nExecStart=/bin/true\nUser = root\n")},
		{"user reset", "racksail-x.service", service("User=blog\nUser=\nExecStart=/bin/true\n")},
		{"numeric user", "racksail-x.service", service("User=0\nExecStart=/bin/true\n")},
		{"panel user", "racksail-x.service", service("User=racksail\nExecStart=/bin/true\n")},
		{"edge user", "racksail-x.service", service("User=racksail-edge\nExecStart=/bin/true\n")},
		{"user from the instance", "racksail-x@.service", service("User=%i\nExecStart=/bin/true\n")},
		{"user outside [Service]", "racksail-x.service", "[Unit]\nUser=blog\n[Service]\nExecStart=/bin/true\n"},
		{"plus prefix", "racksail-x.service", service("User=blog\nExecStart=+/bin/sh -c id\n")},
		{"bang prefix", "racksail-x.service", service("User=blog\nExecStartPre=!/bin/sh\nExecStart=/bin/true\n")},
		{"double bang", "racksail-x.service", service("User=blog\nExecStart=-!!/bin/sh\n")},
		{"prefix after others", "racksail-x.service", service("User=blog\nExecStart=@-+/bin/sh sh\n")},
		{"quoted prefix", "racksail-x.service", service("User=blog\nExecStart=\"+/bin/sh\"\n")},
		{"escaped prefix", "racksail-x.service", service("User=blog\nExecStart=\\x2b/bin/sh\n")},
		{"second command", "racksail-x.service", service("User=blog\nExecStartPre=/bin/true ; +/bin/sh\nExecStart=/bin/true\n")},
		{"continued prefix", "racksail-x.service", service("User=blog\nExecStart=/bin/true \\\n x ; !/bin/sh\n")},
		{"supplementary groups", "racksail-x.service", service("User=blog\nSupplementaryGroups=racksail\nExecStart=/bin/true\n")},
		{"ambient capabilities", "racksail-x.service", service("User=blog\nAmbientCapabilities=CAP_SYS_ADMIN\nExecStart=/bin/true\n")},
		{"privileged start only", "racksail-x.service", service("User=blog\nPermissionsStartOnly=yes\nExecStart=/bin/true\n")},
		{"output to a root file", "racksail-x.service", service("User=blog\nStandardOutput=file:/etc/shadow\nExecStart=/bin/true\n")},
		{"platform state", "racksail-x.service", service("User=blog\nStateDirectory=racksail\nExecStart=/bin/true\n")},
		{"agent runtime", "racksail-x.service", service("User=blog\nRuntimeDirectory=racksail-agent\nExecStart=/bin/true\n")},
		{"account env file", "racksail-x.service", service("User=blog\nEnvironmentFile=/srv/racksail/accounts/blog/.env\nExecStart=/bin/true\n")},
		{"root slice", "racksail-x.service", service("User=blog\nSlice=system.slice\nExecStart=/bin/true\n")},
		{"images", "racksail-x.service", service("User=blog\nRootImage=/srv/racksail/accounts/blog/x.img\nExecStart=/bin/true\n")},
		{"reboot dependency", "racksail-x.service", "[Unit]\nWants=reboot.target\n[Service]\nUser=blog\nExecStart=/bin/true\n"},
		{"failure reboots", "racksail-x.service", "[Unit]\nOnFailure=poweroff.target\n[Service]\nUser=blog\nExecStart=/bin/true\n"},
		{"failure action", "racksail-x.service", "[Unit]\nFailureAction=reboot-force\n[Service]\nUser=blog\nExecStart=/bin/true\n"},
		{"conflicts with the agent", "racksail-x.service", "[Unit]\nConflicts=racksail-agent.service\n[Service]\nUser=blog\nExecStart=/bin/true\n"},
		{"alias", "racksail-x.service", service("User=blog\nExecStart=/bin/true\n[Install]\nAlias=ssh.service\n")},
		{"unknown section", "racksail-x.service", service("User=blog\nExecStart=/bin/true\n[X-Extra]\nA=b\n")},
		{"socket runs commands", "racksail-x.socket", "[Socket]\nListenStream=/run/racksail/sites/x.sock\nExecStartPre=/bin/sh -c id\n"},
		{"socket runs as a user", "racksail-x.socket", "[Socket]\nListenStream=/run/racksail/sites/x.sock\nUser=root\n"},
		{"socket on the agent's", "racksail-x.socket", "[Socket]\nListenStream=/run/racksail/agent.sock\n"},
		{"socket on a system port", "racksail-x.socket", "[Socket]\nListenStream=443\n"},
		{"socket on the panel's port", "racksail-x.socket", "[Socket]\nListenStream=127.0.0.1:8443\n"},
		{"socket starts another", "racksail-x.socket", "[Socket]\nListenStream=8080\nService=racksail-agent.service\n"},
		{"socket symlinks", "racksail-x.socket", "[Socket]\nListenStream=8080\nSymlinks=/etc/x\n"},
		{"timer starts the agent", "racksail-x.timer", "[Timer]\nOnCalendar=daily\nUnit=racksail-agent.service\n"},
		{"timer reboots", "racksail-x.timer", "[Timer]\nOnCalendar=daily\nUnit=reboot.target\n"},
		{"path makes directories", "racksail-x.path", "[Path]\nPathExists=/etc/x\nMakeDirectory=yes\n"},
		{"loop mount", "mnt-racksail-data.mount", "[Mount]\nWhat=/srv/racksail/accounts/blog/x.img\nWhere=/mnt/racksail/data\nType=ext4\nOptions=loop,nosuid,nodev\n"},
		{"setuid mount", "mnt-racksail-data.mount", "[Mount]\nWhat=/dev/sdb\nWhere=/mnt/racksail/data\nType=ext4\nOptions=suid\n"},
		{"mount without nosuid", "mnt-racksail-data.mount", "[Mount]\nWhat=/dev/sdb\nWhere=/mnt/racksail/data\nType=ext4\nOptions=noatime\n"},
		{"mount elsewhere", "mnt-racksail-data.mount", "[Mount]\nWhat=/dev/sdb\nWhere=/etc\nType=ext4\nOptions=nosuid,nodev\n"},
		{"mount over /etc", "etc.mount", "[Mount]\nWhat=/dev/sdb\nWhere=/etc\nType=ext4\nOptions=nosuid,nodev\n"},
		{"swap", "racksail-x.swap", "[Swap]\nWhat=/dev/sdb\n"},
	} {
		if _, err := CheckUnitFile(tc.unit, []byte(tc.content), testUnitRules()); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: should be refused: %v", tc.name, err)
		}
	}
}

func TestCheckDropIn(t *testing.T) {
	for _, good := range []string{
		"[Service]\nMemoryMax=1G\nCPUQuota=50%\nLimitNOFILE=65536\nRestart=on-failure\n",
		"[Unit]\nRequiresMountsFor=/mnt/racksail/data\nAfter=mnt-racksail-data.mount\n",
		"[Slice]\nMemoryHigh=512M\n",
	} {
		if err := CheckDropIn("mariadb.service", []byte(good), testUnitRules()); err != nil {
			t.Errorf("CheckDropIn(%q): %v", good, err)
		}
	}
	for _, bad := range []string{
		"[Service]\nExecStart=\nExecStart=/bin/sh\n",
		"[Service]\nExecStart =/bin/sh\n",
		"[Service]\nUser = root\n",
		"[Service]\nEnvironment=LD_PRELOAD=/srv/racksail/accounts/x/evil.so\n",
		"[Service]\nEnvironmentFile=/srv/racksail/accounts/x/env\n",
		"[Service]\nBindPaths=/srv/racksail/accounts/x/bin:/usr/sbin/mariadbd\n",
		"[Service]\nMemoryMax=1G\r\nUser=root\n",
		"[Service]\nMemoryMax=1G \\\nUser=root\n",
		"[Install]\nWantedBy=multi-user.target\n",
		"[Unit]\nWants=reboot.target\n",
	} {
		if err := CheckDropIn("mariadb.service", []byte(bad), testUnitRules()); !errors.Is(err, ErrInvalid) {
			t.Errorf("CheckDropIn(%q) should be refused: %v", bad, err)
		}
	}
}

func TestCheckMount(t *testing.T) {
	m, err := CheckMount(Mount{Source: "/dev/sdb1", Target: "/mnt/racksail/data", FSType: "btrfs", Options: []string{"noatime", "compress=zstd:3"}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(m.Options, ",") != "noatime,compress=zstd:3,nosuid,nodev" {
		t.Errorf("options = %v", m.Options)
	}
	for _, bad := range []Mount{
		{Source: "/srv/racksail/accounts/x/disk.img", Target: "/mnt/racksail/x", FSType: "ext4", Options: []string{"loop"}},
		{Source: "/dev/sdb", Target: "/mnt/racksail/x", FSType: "ext4", Options: []string{"suid"}},
		{Source: "/dev/sdb", Target: "/mnt/racksail/x", FSType: "ext4", Options: []string{"dev"}},
		{Source: "/dev/sdb", Target: "/mnt/racksail/x", FSType: "nfs"},
		{Source: "/etc", Target: "/mnt/racksail/x", FSType: "ext4", Options: []string{"bind"}},
		{Source: "/dev/../etc", Target: "/mnt/racksail/x", FSType: "ext4"},
	} {
		if _, err := CheckMount(bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("CheckMount(%+v) should be refused: %v", bad, err)
		}
	}
}

func TestUnitNameForPath(t *testing.T) {
	for p, want := range map[string]string{
		"/":                     "-",
		"/mnt/racksail/data":    "mnt-racksail-data",
		"/mnt/racksail/my-disk": `mnt-racksail-my\x2ddisk`,
		"/.snapshots":           `\x2esnapshots`,
		"/srv/racksail/a b/x.y": `srv-racksail-a\x20b-x.y`,
	} {
		if got := UnitNameForPath(p); got != want {
			t.Errorf("UnitNameForPath(%q) = %q, want %q", p, got, want)
		}
	}
}

func FuzzParseUnitFile(f *testing.F) {
	f.Add([]byte(goodService))
	f.Add([]byte("[Service]\nExecStart=/bin/a \\\n# c\n b\n"))
	f.Add([]byte("[Unit]\nA=\\\\\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		u, err := ParseUnitFile(data)
		if err != nil {
			return
		}
		// What parses renders, and the rendering reads back the same.
		out, err := u.Render()
		if err != nil {
			return
		}
		again, err := ParseUnitFile(out)
		if err != nil {
			t.Fatalf("rendering %q gave %q, which doesn't parse: %v", data, out, err)
		}
		if len(again.Sections) != len(u.Sections) {
			t.Fatalf("sections changed: %+v vs %+v", u.Sections, again.Sections)
		}
		_, _ = CheckUnitFile("racksail-fuzz.service", data, testUnitRules())
		_ = CheckDropIn("racksail-fuzz.service", data, testUnitRules())
	})
}
