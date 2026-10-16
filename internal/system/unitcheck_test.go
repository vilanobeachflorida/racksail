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

// accountSandboxLines are the least an account's service is confined by.
const accountSandboxLines = "ProtectSystem=strict\nProtectHome=tmpfs\nPrivateTmp=yes\nPrivateDevices=yes\nNoNewPrivileges=yes\n" +
	"ProtectKernelTunables=yes\nProtectKernelModules=yes\nProtectControlGroups=yes\nRestrictSUIDSGID=yes\n" +
	"RestrictNamespaces=yes\nLockPersonality=yes\nSystemCallFilter=@system-service\nInaccessiblePaths=-/run/racksail\nSocketBindDeny=any\n"

const goodService = "[Unit]\nDescription=Blog\nAfter=network-online.target mariadb.service\n\n[Service]\nUser=blog\nGroup=blog\n" +
	"ExecStart=/usr/bin/php -S 127.0.0.1:3000\nExecStartPre=-/usr/bin/true\n" + accountSandboxLines + "\n[Install]\nWantedBy=multi-user.target\n"

func TestCheckUnitFileAcceptsWhatRacksailRenders(t *testing.T) {
	unit, err := CheckUnitFile("racksail-site-blog.service", []byte(goodService), testUnitRules())
	if err != nil {
		t.Fatal(err)
	}
	if unit.UnitIdentity != (UnitIdentity{User: "blog", Group: "blog"}) || unit.Mount != nil {
		t.Errorf("unit = %+v", unit)
	}
	// Racksail's own service users run Racksail's code; the sandbox of
	// accounts isn't required of them.
	helper := "[Service]\nUser=racksail-sasl\nExecStart=/usr/bin/racksail sasl\n"
	if _, err := CheckUnitFile("racksail-sasl.service", []byte(helper), testUnitRules()); err != nil {
		t.Errorf("a service user's service: %v", err)
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
	unit, err = CheckUnitFile("mnt-racksail-data.mount", []byte(mount), testUnitRules())
	want := Mount{Source: "/dev/disk/by-uuid/0a1b2c3d-1111-2222-3333-444455556666", Target: "/mnt/racksail/data", FSType: "btrfs",
		Options: []string{"noatime", "compress=zstd:3", "nosuid", "nodev"}}
	if err != nil || unit.Mount == nil || unit.Mount.Source != want.Source || unit.Mount.Target != want.Target ||
		unit.Mount.FSType != want.FSType || strings.Join(unit.Mount.Options, ",") != strings.Join(want.Options, ",") {
		t.Errorf("mount = %+v, %v", unit.Mount, err)
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
		{"setuid mount", "mnt-racksail-data.mount", "[Mount]\nWhat=/dev/disk/by-id/ata-HDD_1\nWhere=/mnt/racksail/data\nType=ext4\nOptions=suid\n"},
		{"mount without nosuid", "mnt-racksail-data.mount", "[Mount]\nWhat=/dev/disk/by-id/ata-HDD_1\nWhere=/mnt/racksail/data\nType=ext4\nOptions=noatime\n"},
		{"mount elsewhere", "mnt-racksail-data.mount", "[Mount]\nWhat=/dev/disk/by-id/ata-HDD_1\nWhere=/etc\nType=ext4\nOptions=nosuid,nodev\n"},
		{"mount over /etc", "etc.mount", "[Mount]\nWhat=/dev/disk/by-id/ata-HDD_1\nWhere=/etc\nType=ext4\nOptions=nosuid,nodev\n"},
		{"mount by kernel name", "mnt-racksail-data.mount", "[Mount]\nWhat=/dev/sdb\nWhere=/mnt/racksail/data\nType=ext4\nOptions=nosuid,nodev\n"},
		{"mount by label", "mnt-racksail-data.mount", "[Mount]\nWhat=/dev/disk/by-label/data\nWhere=/mnt/racksail/data\nType=ext4\nOptions=nosuid,nodev\n"},
		{"mount through ..", "mnt-racksail-data.mount", "[Mount]\nWhat=/dev/disk/by-id/../../sda\nWhere=/mnt/racksail/data\nType=ext4\nOptions=nosuid,nodev\n"},
		{"mount without a type", "mnt-racksail-data.mount", "[Mount]\nWhat=/dev/disk/by-id/ata-HDD_1\nWhere=/mnt/racksail/data\nOptions=nosuid,nodev\n"},
		{"subvolume of ext4", "mnt-racksail-data.mount", "[Mount]\nWhat=/dev/disk/by-id/ata-HDD_1\nWhere=/mnt/racksail/data\nType=ext4\nOptions=subvolid=5,nosuid,nodev\n"},
		{"swap", "racksail-x.swap", "[Swap]\nWhat=/dev/sdb\n"},
	} {
		if _, err := CheckUnitFile(tc.unit, []byte(tc.content), testUnitRules()); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: should be refused: %v", tc.name, err)
		}
	}
	// An account's service keeps its sandbox, as systemd reads the last of
	// each setting.
	account := "[Service]\nUser=blog\nExecStart=/bin/true\n"
	for name, content := range map[string]string{
		"no sandbox":                      account,
		"one setting missing":             account + strings.Replace(accountSandboxLines, "LockPersonality=yes\n", "", 1),
		"a setting turned off again":      account + accountSandboxLines + "NoNewPrivileges=no\n",
		"a weaker setting":                account + accountSandboxLines + "ProtectSystem=full\n",
		"system calls only denied":        account + strings.Replace(accountSandboxLines, "SystemCallFilter=@system-service", "SystemCallFilter=~@mount @system-service", 1),
		"the system call filter reset":    account + accountSandboxLines + "SystemCallFilter=\n",
		"system service calls taken away": account + accountSandboxLines + "SystemCallFilter=~@system-service\n",
		"the agent's socket shown again":  account + accountSandboxLines + "InaccessiblePaths=\n",
		"any socket bound":                account + accountSandboxLines + "SocketBindDeny=\n",
	} {
		_, err := CheckUnitFile("racksail-x.service", []byte(content), testUnitRules())
		if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "runs as an account") {
			t.Errorf("%s: should be refused for its sandbox: %v", name, err)
		}
	}
	if _, err := CheckUnitFile("racksail-x.service", []byte(account+accountSandboxLines+"SystemCallFilter=~@privileged\n"), testUnitRules()); err != nil {
		t.Errorf("a filter that takes more away: %v", err)
	}
}

func TestCheckDropIn(t *testing.T) {
	for _, good := range []string{
		"[Service]\nMemoryMax=1G\nCPUQuota=50%\nLimitNOFILE=65536\nRestart=on-failure\n",
		"[Unit]\nRequiresMountsFor=/mnt/racksail/data\nAfter=mnt-racksail-data.mount\n",
	} {
		if err := CheckDropIn("mariadb.service", []byte(good), testUnitRules()); err != nil {
			t.Errorf("CheckDropIn(%q): %v", good, err)
		}
	}
	// Each type of unit takes the sections systemd reads for it.
	for unit, good := range map[string]string{
		"racksail-accounts-blog.slice": "[Slice]\nMemoryHigh=512M\n",
		"racksail-site-blog.socket":    "[Unit]\nAfter=mnt-racksail-data.mount\n[Socket]\nTasksMax=16\n",
		"racksail-backup.timer":        "[Unit]\nAfter=mnt-racksail-data.mount\n",
	} {
		if err := CheckDropIn(unit, []byte(good), testUnitRules()); err != nil {
			t.Errorf("CheckDropIn(%s, %q): %v", unit, good, err)
		}
	}
	for unit, bad := range map[string]string{
		"mariadb.service":              "[Slice]\nMemoryHigh=512M\n",
		"racksail-accounts-blog.slice": "[Service]\nMemoryHigh=512M\n",
		"racksail-site-blog.socket":    "[Service]\nRestart=always\n",
		"racksail-backup.timer":        "[Timer]\nOnCalendar=daily\n",
		"swap.swap":                    "[Unit]\nAfter=local-fs.target\n",
	} {
		if err := CheckDropIn(unit, []byte(bad), testUnitRules()); !errors.Is(err, ErrInvalid) {
			t.Errorf("CheckDropIn(%s, %q) should be refused: %v", unit, bad, err)
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
	for _, source := range []string{
		"/dev/disk/by-id/ata-SIM_HDD_4TB_H0001", "/dev/disk/by-id/ata-SIM_HDD_4TB_H0001-part1",
		"/dev/disk/by-uuid/0a1b2c3d-1111-2222-3333-444455556666", "/dev/disk/by-partuuid/7f3e9a10-01",
	} {
		m, err := CheckMount(Mount{Source: source, Target: "/mnt/racksail/data", FSType: "btrfs", Options: []string{"noatime", "compress=zstd:3", "subvol=/@data"}})
		if err != nil {
			t.Fatalf("CheckMount(%s): %v", source, err)
		}
		if strings.Join(m.Options, ",") != "noatime,compress=zstd:3,subvol=/@data,nosuid,nodev" {
			t.Errorf("options = %v", m.Options)
		}
	}
	for _, bad := range []Mount{
		{Source: "/srv/racksail/accounts/x/disk.img", Target: "/mnt/racksail/x", FSType: "ext4", Options: []string{"loop"}},
		{Source: "/dev/sdb", Target: "/mnt/racksail/x", FSType: "ext4"},
		{Source: "/dev/disk/by-id/ata-HDD_1", Target: "/mnt/racksail/x", FSType: "ext4", Options: []string{"suid"}},
		{Source: "/dev/disk/by-id/ata-HDD_1", Target: "/mnt/racksail/x", FSType: "ext4", Options: []string{"dev"}},
		{Source: "/dev/disk/by-id/ata-HDD_1", Target: "/mnt/racksail/x", FSType: "nfs"},
		{Source: "/dev/disk/by-id/ata-HDD_1", Target: "/mnt/racksail/x", FSType: "ext4", Options: []string{"subvol=/"}},
		{Source: "/dev/disk/by-id/ata-HDD_1", Target: "/", FSType: "btrfs"},
		{Source: "/dev/disk/by-label/root", Target: "/mnt/racksail/x", FSType: "btrfs"},
		{Source: "/etc", Target: "/mnt/racksail/x", FSType: "ext4", Options: []string{"bind"}},
		{Source: "/dev/disk/by-id/../../sda", Target: "/mnt/racksail/x", FSType: "ext4"},
	} {
		if _, err := CheckMount(bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("CheckMount(%+v) should be refused: %v", bad, err)
		}
	}
}

func TestCanonicalLimit(t *testing.T) {
	for _, tc := range [][3]string{
		{"MemoryMax", "1G", "1073741824"},
		{"MemoryMax", "1073741824", "1073741824"},
		{"MemoryHigh", "512M", "536870912"},
		{"MemoryMax", "infinity", "infinity"},
		{"TasksMax", "512", "512"},
		{"TasksMax", "infinity", "infinity"},
		{"CPUQuota", "200%", "2000000"},
		{"CPUQuota", "50%", "500000"},
		{"CPUQuota", "12.5%", "125000"},
		{"CPUWeight", "100", "100"},
		{"IOWeight", "500", "500"},
	} {
		if got, ok := CanonicalLimit(tc[0], tc[1]); !ok || got != tc[2] {
			t.Errorf("CanonicalLimit(%s, %s) = %q, %v; want %q", tc[0], tc[1], got, ok, tc[2])
		}
	}
	// Two spellings of the same limit reduce to the same canonical form.
	if a, _ := CanonicalLimit("MemoryMax", "2G"); a != mustCanon(t, "MemoryMax", "2048M") {
		t.Errorf("2G and 2048M differ: %q", a)
	}
	// Values that can't be reduced with certainty, so a caller never takes
	// two of them for equal.
	for _, tc := range [][2]string{
		{"AllowedCPUs", "0-3"}, {"CPUWeight", "idle"}, {"MemoryMax", "50%"}, {"MemoryMax", ""},
		{"CPUQuota", "200"}, {"TasksMax", "lots"}, {"CPUWeight", "0"}, {"CPUWeight", "20000"}, {"UMask", "0022"},
	} {
		if got, ok := CanonicalLimit(tc[0], tc[1]); ok {
			t.Errorf("CanonicalLimit(%s, %s) = %q, want it to be left uncertain", tc[0], tc[1], got)
		}
	}
}

func mustCanon(t *testing.T, name, value string) string {
	t.Helper()
	got, ok := CanonicalLimit(name, value)
	if !ok {
		t.Fatalf("CanonicalLimit(%s, %s) couldn't be reduced", name, value)
	}
	return got
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
