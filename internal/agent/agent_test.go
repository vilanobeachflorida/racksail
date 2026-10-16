package agent

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/store"
	"github.com/vilanobeachflorida/racksail/internal/system"
	"github.com/vilanobeachflorida/racksail/internal/system/sim"
)

func newLocal(t *testing.T) (*sim.Machine, *Local) {
	t.Helper()
	if err := Err(); err != nil {
		t.Fatal(err)
	}
	m := sim.New(sim.Options{Hostname: "agent-test"})
	return m, NewLocal(m.Backend(), nil)
}

func TestRegistry(t *testing.T) {
	if err := Err(); err != nil {
		t.Fatal(err)
	}
	ops := Operations()
	if len(ops) < 40 {
		t.Errorf("only %d operations are registered", len(ops))
	}
	for _, op := range ops {
		if op.Summary() == "" {
			t.Errorf("%s has no summary", op.Name())
		}
		if op.InputType().Kind().String() != "struct" {
			t.Errorf("%s input is a %s, not a struct", op.Name(), op.InputType().Kind())
		}
	}
	if _, ok := Lookup("WriteFile"); !ok {
		t.Error("WriteFile is not registered")
	}
}

func TestWriteFileIsIdempotent(t *testing.T) {
	ctx := context.Background()
	m, c := newLocal(t)
	in := WriteFileInput{Path: "/etc/racksail/generated/site.conf", Content: []byte("listen 80;\n"), Mode: 0o640, Owner: system.Owner{Group: "racksail"}}

	check, err := WriteFile.Call(ctx, c, in, CallOptions{Check: true})
	if err != nil {
		t.Fatal(err)
	}
	if !check.Changed || len(check.Effects) != 1 {
		t.Errorf("check mode = %+v", check)
	}
	if _, err := m.Backend().Files.Stat(ctx, in.Path); !errors.Is(err, system.ErrNotFound) {
		t.Fatal("check mode must not write")
	}
	first, err := WriteFile.Call(ctx, c, in, CallOptions{})
	if err != nil || !first.Changed {
		t.Fatalf("first write = %+v, %v", first, err)
	}
	again, err := WriteFile.Call(ctx, c, in, CallOptions{})
	if err != nil || again.Changed {
		t.Errorf("writing the same file again should change nothing: %+v, %v", again, err)
	}
	in.Mode = 0o600
	if res, _ := WriteFile.Call(ctx, c, in, CallOptions{}); !res.Changed {
		t.Error("a new mode is a change")
	}
}

func TestPathPolicy(t *testing.T) {
	ctx := context.Background()
	m, c := newLocal(t)
	for _, in := range []WriteFileInput{
		{Path: "/etc/shadow", Mode: 0o600},
		{Path: "/etc/racksail/../shadow", Mode: 0o600},
		{Path: "/srv/racksail/x", Mode: 0o4755},
		{Path: "relative", Mode: 0o644},
	} {
		_, err := WriteFile.Call(ctx, c, in, CallOptions{})
		if !errors.Is(err, ErrInvalidInput) {
			t.Errorf("WriteFile(%s, %o) = %v, want invalid input", in.Path, in.Mode, err)
		}
	}
	// A link planted inside an account's directory can't redirect writes.
	b := m.Backend()
	if err := b.Files.Mkdir(ctx, "/srv/racksail/accounts/blog", 0o755, system.Owner{}); err != nil {
		t.Fatal(err)
	}
	if err := b.Files.Symlink(ctx, "/etc", "/srv/racksail/accounts/blog/etc"); err != nil {
		t.Fatal(err)
	}
	_, err := WriteFile.Call(ctx, c, WriteFileInput{Path: "/srv/racksail/accounts/blog/etc/passwd", Content: []byte("x"), Mode: 0o644}, CallOptions{})
	if err == nil {
		t.Error("a write through a symbolic link must fail")
	}
	if _, err := RemovePath.Call(ctx, c, RemovePathInput{Path: "/srv/racksail", Recursive: true}, CallOptions{}); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("removing a root directory: %v", err)
	}
}

// The agent's own state and audit trail stay out of reach of every
// operation, even when a module lets file operations into the directories
// around them.
func TestAgentsOwnLocationsAreOutOfReach(t *testing.T) {
	ctx := context.Background()
	_, c := newLocal(t)
	policy.Lock()
	saved := slices.Clone(policy.writeRoots)
	policy.writeRoots = append(policy.writeRoots, "/var/log", "/var/lib")
	policy.Unlock()
	defer func() {
		policy.Lock()
		policy.writeRoots = saved
		policy.Unlock()
	}()
	for _, p := range []string{system.AgentAuditLog, system.AgentAuditLog + ".1", system.AgentLogDir, system.AgentClusterFile, system.AgentStateDir + "/ledger/x"} {
		if _, err := WriteFile.Call(ctx, c, WriteFileInput{Path: p, Content: []byte("{}"), Mode: 0o600}, CallOptions{}); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("writing %s: %v", p, err)
		}
		if _, err := RemovePath.Call(ctx, c, RemovePathInput{Path: p, Recursive: true}, CallOptions{}); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("removing %s: %v", p, err)
		}
		if _, err := EnsureSymlink.Call(ctx, c, EnsureSymlinkInput{Path: p, Target: "/srv/racksail/x"}, CallOptions{}); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("linking %s: %v", p, err)
		}
		if _, err := ReadFile.Call(ctx, c, ReadFileInput{Path: p}, CallOptions{}); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("reading %s: %v", p, err)
		}
	}
	// Nor through the directories that hold them.
	for _, p := range []string{"/var/log", "/var/lib"} {
		if _, err := RemovePath.Call(ctx, c, RemovePathInput{Path: p, Recursive: true}, CallOptions{}); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("removing %s: %v", p, err)
		}
	}
	if _, err := WriteFile.Call(ctx, c, WriteFileInput{Path: "/var/log/racksail-other.log", Content: []byte("x"), Mode: 0o644}, CallOptions{}); err != nil {
		t.Errorf("a neighbour of the trail stays writable: %v", err)
	}
}

func TestUsersAndGroups(t *testing.T) {
	ctx := context.Background()
	m, c := newLocal(t)
	acct := EnsureUserInput{Name: "blog", UID: 20001, Home: "/srv/racksail/accounts/blog", SubIDs: &system.IDRange{Start: 1065536, Count: 65536}}
	if res, err := EnsureUser.Call(ctx, c, acct, CallOptions{}); err != nil || !res.Changed {
		t.Fatalf("create = %+v, %v", res, err)
	}
	if res, err := EnsureUser.Call(ctx, c, acct, CallOptions{}); err != nil || res.Changed {
		t.Errorf("second ensure = %+v, %v", res, err)
	}
	acct.Locked = true
	if res, _ := EnsureUser.Call(ctx, c, acct, CallOptions{}); !res.Changed {
		t.Error("locking is a change")
	}
	if u, _ := m.Backend().Users.LookupUser(ctx, "blog"); !u.Locked {
		t.Error("the user should be locked")
	}
	for _, bad := range []EnsureUserInput{
		{Name: "blog2", UID: 5000, Home: "/srv/racksail/accounts/blog2"},
		{Name: "blog3", UID: 20003, Home: "/srv/x", Groups: []string{"sudo"}},
		{Name: "blog4", UID: 20004, Home: "/srv/x", Shell: "/usr/bin/python3"},
	} {
		if _, err := EnsureUser.Call(ctx, c, bad, CallOptions{}); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("EnsureUser(%+v) = %v", bad, err)
		}
	}
	members := []string{"blog"}
	if res, err := EnsureGroup.Call(ctx, c, EnsureGroupInput{Name: "webusers", Members: &members}, CallOptions{}); err != nil || !res.Changed {
		t.Errorf("group = %+v, %v", res, err)
	}
	for _, bad := range []EnsureUserInput{
		{Name: "diskuser", UID: 20077, GID: 6, Home: "/srv/racksail/accounts/diskuser"},
		{Name: "paneluser", System: true, GID: 990, Home: "/nonexistent"},
	} {
		if _, err := EnsureUser.Call(ctx, c, bad, CallOptions{}); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("a privileged primary group %d: %v", bad.GID, err)
		}
	}
	if _, err := DeleteUser.Call(ctx, c, DeleteUserInput{Name: "root"}, CallOptions{}); !errors.Is(err, ErrNotAllowed) {
		t.Errorf("deleting root: %v", err)
	}
	if res, err := DeleteUser.Call(ctx, c, DeleteUserInput{Name: "blog"}, CallOptions{}); err != nil || !res.Changed {
		t.Errorf("delete = %+v, %v", res, err)
	}
	if res, _ := DeleteUser.Call(ctx, c, DeleteUserInput{Name: "blog"}, CallOptions{}); res.Changed {
		t.Error("deleting a missing user changes nothing")
	}
}

// newAccount creates an account user and group with the given ID.
func newAccount(t *testing.T, c Client, name string, id int) {
	t.Helper()
	ctx := context.Background()
	if _, err := EnsureGroup.Call(ctx, c, EnsureGroupInput{Name: name, GID: id}, CallOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureUser.Call(ctx, c, EnsureUserInput{Name: name, UID: id, GID: id, Home: system.AccountDir(name)}, CallOptions{}); err != nil {
		t.Fatal(err)
	}
}

// sandboxLines are the least confinement an account's service gets.
var sandboxLines = func() string {
	entries, err := system.Sandbox{User: "x"}.Entries()
	if err != nil {
		panic(err)
	}
	var b strings.Builder
	for _, e := range entries[1:] {
		b.WriteString(e.Key + "=" + e.Value + "\n")
	}
	return b.String()
}()

// sandboxed adds the sandbox an account's service needs to a unit's
// [Service] section, so a test's refusal is about what it means to test.
func sandboxed(unit string) []byte {
	return []byte(strings.Replace(unit, "[Service]\n", "[Service]\n"+sandboxLines, 1))
}

func TestUnitsAndPolicy(t *testing.T) {
	ctx := context.Background()
	m, c := newLocal(t)
	root := EnsureUnitInput{Name: "racksail-site-blog.service", Content: sandboxed("[Service]\nExecStart=/bin/true\n")}
	if _, err := EnsureUnit.Call(ctx, c, root, CallOptions{}); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("a service without a user: %v", err)
	}
	other := EnsureUnitInput{Name: "evil.service", Content: sandboxed("[Service]\nUser=nobody\n")}
	if _, err := EnsureUnit.Call(ctx, c, other, CallOptions{}); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("a unit not named racksail-*: %v", err)
	}
	bare := EnsureUnitInput{Name: "racksail-site-blog.service", Content: []byte("[Service]\nUser=blog\nExecStart=/usr/bin/sleep infinity\n")}
	if _, err := EnsureUnit.Call(ctx, c, bare, CallOptions{Check: true}); !errors.Is(err, ErrInvalidInput) || !strings.Contains(err.Error(), "runs as an account") {
		t.Errorf("an account's service without its sandbox: %v", err)
	}
	unit := EnsureUnitInput{Name: "racksail-site-blog.service", Content: sandboxed("[Service]\nUser=blog\nExecStart=/usr/bin/sleep infinity\n")}
	// A preview can't see the user an earlier step creates, so check mode
	// lets a missing user through; running it can't.
	if res, err := EnsureUnit.Call(ctx, c, unit, CallOptions{Check: true}); err != nil || !res.Changed {
		t.Fatalf("check = %+v, %v", res, err)
	}
	if _, err := EnsureUnit.Call(ctx, c, unit, CallOptions{}); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("a service for a user that doesn't exist: %v", err)
	}
	newAccount(t, c, "blog", 20001)
	if res, err := EnsureUnit.Call(ctx, c, unit, CallOptions{}); err != nil || !res.Changed {
		t.Fatalf("ensure unit = %+v, %v", res, err)
	}
	if res, _ := EnsureUnit.Call(ctx, c, unit, CallOptions{}); res.Changed {
		t.Error("the same unit file is no change")
	}
	if res, err := StartUnit.Call(ctx, c, UnitInput{Name: unit.Name}, CallOptions{}); err != nil || !res.Changed {
		t.Fatalf("start = %+v, %v", res, err)
	}
	if res, _ := StartUnit.Call(ctx, c, UnitInput{Name: unit.Name}, CallOptions{}); res.Changed {
		t.Error("starting a running unit changes nothing")
	}
	if _, err := StopUnit.Call(ctx, c, UnitInput{Name: "ssh.service"}, CallOptions{}); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("stopping a unit Racksail doesn't manage: %v", err)
	}

	// Users that reach the agent's socket or other privileges never run a
	// unit written for the panel.
	b := m.Backend()
	if err := b.Users.CreateUser(ctx, system.User{Name: "shady", UID: 20050, GID: 990, Home: "/srv/racksail/accounts/shady"}); err != nil {
		t.Fatal(err)
	}
	for _, content := range []string{
		"[Service]\nUser=racksail\nExecStart=/bin/true\n",
		"[Service]\nUser=shady\nExecStart=/bin/true\n",
		"[Service]\nUser=blog\nGroup=racksail\nExecStart=/bin/true\n",
		"[Service]\nUser=nobody\nExecStart=/bin/true\n",
		"[Service]\nUser=blog\nDynamicUser=yes\nExecStart=/bin/true\n",
		"[Service]\nUser=blog\nExecStart=+/bin/sh -c id\n",
		"[Service]\nUser=blog\nExecStart=/bin/true\nUser = root\n",
	} {
		in := EnsureUnitInput{Name: "racksail-site-x.service", Content: sandboxed(content)}
		if _, err := EnsureUnit.Call(ctx, c, in, CallOptions{}); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("EnsureUnit(%q) should be refused: %v", content, err)
		}
	}
	dynamic := EnsureUnitInput{Name: "racksail-addon-x.service", Content: []byte("[Service]\nUser=racksail-addon-x\nDynamicUser=yes\nExecStart=/bin/true\n")}
	if _, err := EnsureUnit.Call(ctx, c, dynamic, CallOptions{}); err != nil {
		t.Errorf("a dynamic user: %v", err)
	}

	// Racksail's own units are rendered by the agent, never written.
	for _, name := range system.PlatformUnitNames() {
		in := EnsureUnitInput{Name: name, Content: sandboxed("[Service]\nUser=blog\nExecStart=/bin/true\n")}
		if _, err := EnsureUnit.Call(ctx, c, in, CallOptions{}); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("writing %s: %v", name, err)
		}
	}
	if _, err := RemoveUnit.Call(ctx, c, UnitInput{Name: system.AgentUnit}, CallOptions{}); !errors.Is(err, ErrNotAllowed) {
		t.Errorf("removing the agent's unit: %v", err)
	}

	for _, content := range []string{
		"[Service]\nUser=root\n",
		"[Service]\nExecStart =/bin/sh\n",
		"[Service]\nEnvironment=LD_PRELOAD=/srv/racksail/accounts/blog/x.so\n",
	} {
		in := DropInInput{Unit: unit.Name, Name: "racksail-limits", Content: []byte(content)}
		if _, err := EnsureDropIn.Call(ctx, c, in, CallOptions{}); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("drop-in %q should be refused: %v", content, err)
		}
	}
	if _, err := EnsureDropIn.Call(ctx, c, DropInInput{Unit: system.AgentUnit, Name: "racksail-limits", Content: []byte("[Service]\nMemoryMax=1G\n")}, CallOptions{}); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("a drop-in for the agent: %v", err)
	}
	limits := DropInInput{Unit: unit.Name, Name: "racksail-limits", Content: []byte("[Service]\nMemoryMax=1G\nTasksMax=64\n")}
	if res, err := EnsureDropIn.Call(ctx, c, limits, CallOptions{}); err != nil || !res.Changed {
		t.Errorf("drop-in = %+v, %v", res, err)
	}
	if res, err := SetUnitProperties.Call(ctx, c, SetUnitPropertiesInput{Name: unit.Name, Properties: []system.UnitProperty{{Name: "MemoryMax", Value: "1G"}}}, CallOptions{}); err != nil || !res.Changed {
		t.Errorf("properties = %+v, %v", res, err)
	}
	if res, err := RemoveUnit.Call(ctx, c, UnitInput{Name: unit.Name}, CallOptions{}); err != nil || !res.Changed {
		t.Errorf("remove = %+v, %v", res, err)
	}
	st, _ := UnitStatus.Call(ctx, c, UnitStatusInput{Name: unit.Name}, CallOptions{})
	if st.LoadState != "not-found" {
		t.Errorf("after removal = %+v", st)
	}
}

func TestPlatformUnits(t *testing.T) {
	ctx := context.Background()
	m, c := newLocal(t)
	in := PlatformUnitsInput{Binary: "/usr/bin/racksail"}
	res, err := InstallPlatformUnits.Call(ctx, c, in, CallOptions{})
	if err != nil || !res.Changed || len(res.Effects) != len(system.PlatformUnitNames()) {
		t.Fatalf("install = %+v, %v", res, err)
	}
	want, _ := system.PlatformUnits(in.Binary)
	b := m.Backend()
	for _, name := range system.PlatformUnitNames() {
		got, err := b.Units.ReadUnit(ctx, name)
		if err != nil || string(got) != string(want[name]) {
			t.Errorf("%s = %q, %v", name, got, err)
		}
	}
	if res, _ := InstallPlatformUnits.Call(ctx, c, in, CallOptions{}); res.Changed {
		t.Error("installing the same units again changes nothing")
	}

	// The executable must be one only root can change.
	for _, bad := range []string{"/opt/racksail/runtimes/x/racksail", "/srv/racksail/accounts/blog/racksail", "/usr/bin/bash", "racksail", "/usr/bin/racksail old"} {
		if _, err := InstallPlatformUnits.Call(ctx, c, PlatformUnitsInput{Binary: bad}, CallOptions{}); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("binary %q: %v", bad, err)
		}
	}
	if err := b.Files.Mkdir(ctx, "/home/blog", 0o755, system.Owner{User: "nobody"}); err != nil {
		t.Fatal(err)
	}
	if err := b.Files.WriteFile(ctx, "/home/blog/racksail", []byte("x"), 0o755, system.Owner{User: "nobody"}); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallPlatformUnits.Call(ctx, c, PlatformUnitsInput{Binary: "/home/blog/racksail"}, CallOptions{}); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("a binary another user owns: %v", err)
	}
	if _, err := InstallPlatformUnits.Call(ctx, c, PlatformUnitsInput{Binary: "/usr/local/bin/racksail"}, CallOptions{}); !errors.Is(err, ErrNotFound) {
		t.Errorf("a binary that isn't there: %v", err)
	}
}

func TestPackagesAndSources(t *testing.T) {
	ctx := context.Background()
	m, c := newLocal(t)
	check, err := InstallPackages.Call(ctx, c, InstallPackagesInput{Packages: []system.PackageRequest{{Name: "mariadb-server"}}}, CallOptions{Check: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(check.Effects) != 2 || check.Effects[0].DownloadBytes == 0 {
		t.Errorf("preview should list the server and its client with sizes: %+v", check.Effects)
	}
	if res, err := InstallPackages.Call(ctx, c, InstallPackagesInput{Packages: []system.PackageRequest{{Name: "mariadb-server"}}}, CallOptions{}); err != nil || !res.Changed {
		t.Fatalf("install = %+v, %v", res, err)
	}
	if res, _ := InstallPackages.Call(ctx, c, InstallPackagesInput{Packages: []system.PackageRequest{{Name: "mariadb-server"}}}, CallOptions{}); res.Changed {
		t.Error("installing an installed package changes nothing")
	}
	if _, err := EnsureAptSource.Call(ctx, c, SourceInput{Name: "unknown-vendor"}, CallOptions{}); !errors.Is(err, ErrNotAllowed) {
		t.Errorf("unregistered source: %v", err)
	}
	RegisterAptSource(system.AptSource{
		Name: "agent-test-php", URIs: []string{"https://packages.sury.org/php/"}, Suites: []string{"trixie"}, Components: []string{"main"},
		Key: []byte("-----BEGIN PGP PUBLIC KEY BLOCK-----\nk\n-----END PGP PUBLIC KEY BLOCK-----\n"), Packages: []string{"php*"},
	})
	if res, err := EnsureAptSource.Call(ctx, c, SourceInput{Name: "agent-test-php"}, CallOptions{}); err != nil || !res.Changed {
		t.Fatalf("source = %+v, %v", res, err)
	}
	if res, _ := EnsureAptSource.Call(ctx, c, SourceInput{Name: "agent-test-php"}, CallOptions{}); res.Changed {
		t.Error("an enabled source is no change")
	}
	if _, err := InstallPackages.Call(ctx, c, InstallPackagesInput{Packages: []system.PackageRequest{{Name: "php8.4-fpm"}}}, CallOptions{}); err != nil {
		t.Errorf("installing from the source: %v", err)
	}
	if st, _ := m.Backend().Units.Status(ctx, "php8.4-fpm.service"); !st.Active() {
		t.Error("the package's service should be running")
	}
	archive := InstallArchiveInput{Archive: system.Archive{URL: "https://nodejs.org/dist/v22.11.0/node-v22.11.0-linux-x64.tar.xz", SHA256: strings.Repeat("ab", 32), Size: 30 << 20, Format: "tar.xz", Dest: "/opt/racksail/runtimes/node/22.11.0", StripComponents: 1}}
	if res, err := InstallArchive.Call(ctx, c, archive, CallOptions{}); err != nil || !res.Changed || res.Effects[0].Source == "" {
		t.Errorf("archive = %+v, %v", res, err)
	}
	archive.Archive.Dest = "/etc/racksail/evil"
	if _, err := InstallArchive.Call(ctx, c, archive, CallOptions{}); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("archive outside the runtime directories: %v", err)
	}
}

func TestStorageOps(t *testing.T) {
	ctx := context.Background()
	_, c := newLocal(t)
	sv := SubvolumeInput{Path: "/srv/racksail/accounts/blog"}
	if res, err := EnsureSubvolume.Call(ctx, c, sv, CallOptions{}); err != nil || !res.Changed {
		t.Fatalf("subvolume = %+v, %v", res, err)
	}
	if res, _ := EnsureSubvolume.Call(ctx, c, sv, CallOptions{}); res.Changed {
		t.Error("an existing subvolume is no change")
	}
	if res, err := SetSubvolumeQuota.Call(ctx, c, QuotaInput{Path: sv.Path, LimitBytes: 5 << 30}, CallOptions{}); err != nil || !res.Changed {
		t.Errorf("quota = %+v, %v", res, err)
	}
	if res, _ := SetSubvolumeQuota.Call(ctx, c, QuotaInput{Path: sv.Path, LimitBytes: 5 << 30}, CallOptions{}); res.Changed {
		t.Error("the same quota is no change")
	}
	if _, err := EnsureDir.Call(ctx, c, EnsureDirInput{Path: SnapshotDir, Mode: 0o700}, CallOptions{}); err != nil {
		t.Fatal(err)
	}
	snap := SnapshotInput{Source: sv.Path, Dest: SnapshotDir + "/blog-1", ReadOnly: true}
	if res, err := CreateSnapshot.Call(ctx, c, snap, CallOptions{}); err != nil || !res.Changed {
		t.Errorf("snapshot = %+v, %v", res, err)
	}
	if _, err := WipeDevice.Call(ctx, c, DeviceInput{Device: "/dev/nvme0n1"}, CallOptions{}); err == nil {
		t.Error("wiping the system disk must fail")
	}
	if _, err := FormatDevice.Call(ctx, c, FormatInput{Spec: system.FormatSpec{Type: "btrfs", Devices: []string{"/dev/sda", "/dev/sdb"}, Profiles: system.BtrfsProfiles{Data: "raid1", Metadata: "raid1"}}}, CallOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureDir.Call(ctx, c, EnsureDirInput{Path: "/mnt/racksail/pool", Mode: 0o755}, CallOptions{}); err != nil {
		t.Fatal(err)
	}
	const sda, sdb = "/dev/disk/by-id/ata-SIM_HDD_4TB_H0001", "/dev/disk/by-id/ata-SIM_HDD_4TB_H0002"
	for _, bad := range []system.Mount{
		{Source: "/srv/racksail/accounts/blog/disk.img", Target: "/mnt/racksail/img", FSType: "ext4", Options: []string{"loop"}},
		{Source: sdb, Target: "/mnt/racksail/x", FSType: "btrfs", Options: []string{"suid"}},
		{Source: sdb, Target: "/var/lib/racksail", FSType: "btrfs"},
		{Source: "/dev/sda", Target: "/mnt/racksail/pool", FSType: "btrfs"},
		// Mount points sit right below the data disks' location.
		{Source: sda, Target: "/mnt/racksail/pool/inner", FSType: "btrfs"},
		{Source: sda, Target: "/srv/racksail/accounts/blog", FSType: "btrfs"},
		{Source: sda, Target: "/mnt/racksail", FSType: "btrfs"},
	} {
		if _, err := EnsureMounted.Call(ctx, c, MountInput{Mount: bad}, CallOptions{}); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("mount %+v should be refused: %v", bad, err)
		}
	}
	mount := MountInput{Mount: system.Mount{Source: sda, Target: "/mnt/racksail/pool", FSType: "btrfs"}}
	if res, err := EnsureMounted.Call(ctx, c, mount, CallOptions{Check: true}); err != nil || !res.Changed {
		t.Fatalf("mount in check mode = %+v, %v", res, err)
	}
	if res, err := EnsureMounted.Call(ctx, c, mount, CallOptions{}); err != nil || !res.Changed {
		t.Fatalf("mount = %+v, %v", res, err)
	}
	mounts, _ := ListMounts.Call(ctx, c, Empty{}, CallOptions{})
	for _, mt := range mounts.Mounts {
		if mt.Target == mount.Mount.Target && !(slices.Contains(mt.Options, "nosuid") && slices.Contains(mt.Options, "nodev")) {
			t.Errorf("data disks are mounted with nosuid and nodev: %+v", mt)
		}
	}
	// The pool is the same mount whichever of its disks names it.
	disks, _ := ListDisks.Call(ctx, c, Empty{}, CallOptions{})
	pool, _ := system.FindDevice(disks.Disks, sdb)
	for _, again := range []string{sda, sdb, "/dev/disk/by-uuid/" + pool.UUID()} {
		in := MountInput{Mount: system.Mount{Source: again, Target: "/mnt/racksail/pool", FSType: "btrfs"}}
		if res, err := EnsureMounted.Call(ctx, c, in, CallOptions{}); err != nil || res.Changed {
			t.Errorf("mounting %s again = %+v, %v", again, res, err)
		}
	}
	if _, err := WipeDevice.Call(ctx, c, DeviceInput{Device: "/dev/sdb"}, CallOptions{}); err == nil {
		t.Error("wiping a member of a mounted pool must fail")
	}
	if _, err := WipeDevice.Call(ctx, c, DeviceInput{Device: sdb}, CallOptions{}); err == nil {
		t.Error("wiping a member of a mounted pool by its link must fail")
	}
	if res, err := StartScrub.Call(ctx, c, MountPointInput{Mount: "/mnt/racksail/pool"}, CallOptions{}); err != nil || !res.Changed {
		t.Errorf("scrub = %+v, %v", res, err)
	}
	if res, err := AddBtrfsDevice.Call(ctx, c, BtrfsDeviceInput{Mount: "/mnt/racksail/pool", Device: sdb}, CallOptions{}); err != nil || res.Changed {
		t.Errorf("adding a member by its link = %+v, %v", res, err)
	}
}

// A compromised panel could otherwise bring the system disk, or a
// filesystem mounted elsewhere, into the locations its file operations
// write, and change the system through them.
func TestMountsTakeOnlyDataDisks(t *testing.T) {
	ctx := context.Background()
	m, c := newLocal(t)
	b := m.Backend()
	disks, err := ListDisks.Call(ctx, c, Empty{}, CallOptions{})
	if err != nil {
		t.Fatal(err)
	}
	root, _ := system.FindDevice(disks.Disks, "/dev/nvme0n1p2")
	efi, _ := system.FindDevice(disks.Disks, "/dev/nvme0n1p1")
	for _, target := range []string{"/mnt/racksail/a", "/mnt/racksail/b"} {
		if _, err := EnsureDir.Call(ctx, c, EnsureDirInput{Path: target, Mode: 0o755}, CallOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := FormatDevice.Call(ctx, c, FormatInput{Spec: system.FormatSpec{Type: "ext4", Devices: []string{"/dev/sda"}}}, CallOptions{}); err != nil {
		t.Fatal(err)
	}
	const data = "/dev/disk/by-id/ata-SIM_HDD_4TB_H0001"
	if _, err := EnsureMounted.Call(ctx, c, MountInput{Mount: system.Mount{Source: data, Target: "/mnt/racksail/a", FSType: "ext4"}}, CallOptions{}); err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		mount system.Mount
		code  error
		// onSystem says the refusal is for the system disk.
		onSystem bool
	}{
		"the system disk":              {system.Mount{Source: "/dev/disk/by-id/nvme-SIM_SSD_512GB_S0001-part2", Target: "/mnt/racksail/b", FSType: "btrfs"}, ErrInvalidInput, true},
		"the root filesystem":          {system.Mount{Source: "/dev/disk/by-uuid/" + root.UUID(), Target: "/mnt/racksail/b", FSType: "btrfs"}, ErrInvalidInput, true},
		"its top level":                {system.Mount{Source: "/dev/disk/by-uuid/" + root.UUID(), Target: "/mnt/racksail/b", FSType: "btrfs", Options: []string{"subvolid=5"}}, ErrInvalidInput, true},
		"its snapshots":                {system.Mount{Source: "/dev/disk/by-partuuid/" + root.Partition.PartUUID, Target: "/mnt/racksail/b", FSType: "btrfs", Options: []string{"subvol=/@snapshots"}}, ErrInvalidInput, true},
		"the boot partition":           {system.Mount{Source: "/dev/disk/by-uuid/" + efi.UUID(), Target: "/mnt/racksail/b", FSType: "vfat"}, ErrInvalidInput, true},
		"the whole system disk":        {system.Mount{Source: "/dev/disk/by-id/nvme-SIM_SSD_512GB_S0001", Target: "/mnt/racksail/b", FSType: "btrfs"}, ErrInvalidInput, true},
		"a disk mounted elsewhere":     {system.Mount{Source: data, Target: "/mnt/racksail/b", FSType: "ext4"}, ErrConflict, false},
		"a disk that isn't there":      {system.Mount{Source: "/dev/disk/by-id/ata-NOTHING", Target: "/mnt/racksail/b", FSType: "ext4"}, ErrNotFound, false},
		"a filesystem it doesn't hold": {system.Mount{Source: "/dev/disk/by-id/ata-SIM_HDD_4TB_H0002", Target: "/mnt/racksail/b", FSType: "ext4"}, ErrInvalidInput, false},
	} {
		_, err := EnsureMounted.Call(ctx, c, MountInput{Mount: tc.mount}, CallOptions{})
		if !errors.Is(err, tc.code) {
			t.Errorf("%s: %v, want %v", name, err, tc.code)
		}
		if tc.onSystem && (err == nil || !strings.Contains(err.Error(), "the disk the system runs from")) {
			t.Errorf("%s isn't refused for being on the system disk: %v", name, err)
		}
		// A unit mounting the same at boot is refused alike.
		unit := "[Mount]\nWhat=" + tc.mount.Source + "\nWhere=/mnt/racksail/b\nType=" + tc.mount.FSType + "\nOptions=" +
			strings.Join(append(tc.mount.Options, "nosuid", "nodev"), ",") + "\n"
		if _, err := EnsureUnit.Call(ctx, c, EnsureUnitInput{Name: "mnt-racksail-b.mount", Content: []byte(unit)}, CallOptions{}); !errors.Is(err, tc.code) {
			t.Errorf("%s as a unit: %v, want %v", name, err, tc.code)
		}
	}
	// A mount point a link was planted at is refused, and no operation
	// plants one where data disks are mounted.
	if err := b.Files.Symlink(ctx, "/etc", "/mnt/racksail/link"); err != nil {
		t.Fatal(err)
	}
	if _, err := FormatDevice.Call(ctx, c, FormatInput{Spec: system.FormatSpec{Type: "ext4", Devices: []string{"/dev/sdb"}}}, CallOptions{}); err != nil {
		t.Fatal(err)
	}
	spare := system.Mount{Source: "/dev/disk/by-id/ata-SIM_HDD_4TB_H0002", Target: "/mnt/racksail/link", FSType: "ext4"}
	if _, err := EnsureMounted.Call(ctx, c, MountInput{Mount: spare}, CallOptions{}); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("a mount point that is a link: %v", err)
	}
	if _, err := EnsureSymlink.Call(ctx, c, EnsureSymlinkInput{Path: "/mnt/racksail/c", Target: "/srv/racksail/accounts"}, CallOptions{}); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("a link where data disks are mounted: %v", err)
	}
	extract := ExtractArchiveInput{Root: system.DataDisksDir, Archive: "a/x.tar", Dest: ".", Format: "tar"}
	if _, err := ExtractArchive.Call(ctx, c, extract, CallOptions{}); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("unpacking into where data disks are mounted: %v", err)
	}
	// The system disk's filesystems stay mounted.
	if _, err := Unmount.Call(ctx, c, UnmountInput{Target: "/srv/racksail"}, CallOptions{}); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("unmounting the accounts' area of the system disk: %v", err)
	}
	if _, err := Unmount.Call(ctx, c, UnmountInput{Target: "/var/lib/racksail"}, CallOptions{}); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("unmounting the panel's state: %v", err)
	}
	// A spare data disk mounts, and a unit mounts it at boot.
	spare.Target = "/mnt/racksail/b"
	if _, err := EnsureMounted.Call(ctx, c, MountInput{Mount: spare}, CallOptions{}); err != nil {
		t.Errorf("a spare data disk: %v", err)
	}
	if res, err := Unmount.Call(ctx, c, UnmountInput{Target: "/mnt/racksail/a"}, CallOptions{}); err != nil || !res.Changed {
		t.Errorf("unmounting a data disk = %+v, %v", res, err)
	}
	unit := "[Mount]\nWhat=" + spare.Source + "\nWhere=/mnt/racksail/b\nType=ext4\nOptions=noatime,nosuid,nodev\n[Install]\nWantedBy=multi-user.target\n"
	if res, err := EnsureUnit.Call(ctx, c, EnsureUnitInput{Name: "mnt-racksail-b.mount", Content: []byte(unit)}, CallOptions{}); err != nil || !res.Changed {
		t.Errorf("a unit for a mounted data disk = %+v, %v", res, err)
	}
	// In a preview, the partition an earlier step makes isn't there yet.
	planned := "[Mount]\nWhat=/dev/disk/by-id/nvme-SIM_SSD_1TB_S0002-part1\nWhere=/mnt/racksail/fast\nType=xfs\nOptions=nosuid,nodev\n"
	if _, err := EnsureUnit.Call(ctx, c, EnsureUnitInput{Name: "mnt-racksail-fast.mount", Content: []byte(planned)}, CallOptions{Check: true}); err != nil {
		t.Errorf("a planned partition in check mode: %v", err)
	}
	onSystem := "[Mount]\nWhat=/dev/disk/by-id/nvme-SIM_SSD_512GB_S0001-part3\nWhere=/mnt/racksail/fast\nType=xfs\nOptions=nosuid,nodev\n"
	if _, err := EnsureUnit.Call(ctx, c, EnsureUnitInput{Name: "mnt-racksail-fast.mount", Content: []byte(onSystem)}, CallOptions{Check: true}); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("a planned partition of the system disk: %v", err)
	}
}

func TestSnapshotPaths(t *testing.T) {
	ctx := context.Background()
	m, c := newLocal(t)
	b := m.Backend()
	blog := "/srv/racksail/accounts/blog"
	if _, err := EnsureSubvolume.Call(ctx, c, SubvolumeInput{Path: blog}, CallOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteFile.Call(ctx, c, WriteFileInput{Path: blog + "/index.html", Content: []byte("home"), Mode: 0o644}, CallOptions{}); err != nil {
		t.Fatal(err)
	}
	in := SnapshotPathsInput{Label: "chg_01test", Paths: []string{
		blog + "/index.html",
		blog,
		"/etc/racksail/generated/blog.conf",
		"/boot/efi/EFI/debian/grub.cfg",
		"/.snapshots/racksail/chg_01old/root",
	}}
	dir := SnapshotDir + "/chg_01test"
	// The root filesystem holds what Racksail mustn't read, so its
	// snapshot goes where only the agent reaches.
	sealed := SystemSnapshotDir + "/chg_01test"
	want := []Snapshot{{Source: blog, Path: dir + "/" + snapshotName(blog)}, {Source: "/", Path: sealed + "/" + snapshotName("/")}}
	if !strings.HasPrefix(path.Base(want[0].Path), "srv-racksail-accounts-blog-") || !strings.HasPrefix(path.Base(want[1].Path), "root-") {
		t.Errorf("snapshot names = %v", want)
	}
	if snapshotName("/srv/racksail") != "srv-racksail-abdff825" || snapshotName("/") != "root-8a5edab2" {
		t.Errorf("snapshot names = %s, %s", snapshotName("/srv/racksail"), snapshotName("/"))
	}
	if snapshotName("/srv/a-b") == snapshotName("/srv/a/b") {
		t.Error("subvolumes whose names read the same get different snapshots")
	}

	check, err := SnapshotPaths.Call(ctx, c, in, CallOptions{Check: true})
	if err != nil || !check.Changed || len(check.Effects) != 2 || !slices.Equal(check.Snapshots, want) {
		t.Fatalf("check = %+v, %v", check, err)
	}
	if !slices.Equal(check.Unprotected, []string{"/boot/efi/EFI/debian/grub.cfg", "/.snapshots/racksail/chg_01old/root"}) {
		t.Errorf("paths no snapshot covers = %v", check.Unprotected)
	}
	if _, err := b.Files.Stat(ctx, dir); !errors.Is(err, system.ErrNotFound) {
		t.Fatal("check mode must not snapshot anything")
	}

	res, err := SnapshotPaths.Call(ctx, c, in, CallOptions{})
	if err != nil || !res.Changed || !slices.Equal(res.Snapshots, want) {
		t.Fatalf("snapshots = %+v, %v", res, err)
	}
	if data, err := b.Files.ReadFile(ctx, want[0].Path+"/index.html", 0); err != nil || string(data) != "home" {
		t.Errorf("the snapshot holds the site's files: %q, %v", data, err)
	}
	for _, d := range []string{dir, sealed} {
		if info, err := b.Files.Stat(ctx, d); err != nil || info.Perm() != 0o700 {
			t.Errorf("snapshot directory %s = %v, %v", d, info, err)
		}
	}
	svs, _ := b.Btrfs.Subvolumes(ctx, "/.snapshots")
	readOnly := 0
	for _, sv := range svs {
		if (strings.HasPrefix(sv.Path, dir+"/") || strings.HasPrefix(sv.Path, sealed+"/")) && sv.ReadOnly {
			readOnly++
		}
	}
	if readOnly != 2 {
		t.Errorf("read-only snapshots = %d in %+v", readOnly, svs)
	}

	// Nothing reads the system's snapshot, sends it, copies it or writes
	// near it: it holds root's secrets.
	shadow := want[1].Path + "/etc/shadow"
	if _, err := ReadFile.Call(ctx, c, ReadFileInput{Path: shadow}, CallOptions{}); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("reading a system snapshot: %v", err)
	}
	if _, err := ListDir.Call(ctx, c, PathInput{Path: want[1].Path}, CallOptions{}); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("listing a system snapshot: %v", err)
	}
	if err := c.Download(ctx, shadow, io.Discard); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("downloading from a system snapshot: %v", err)
	}
	if err := c.SendSnapshot(ctx, SnapshotStreamInput{Snapshot: want[1].Path}, io.Discard); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("sending a system snapshot: %v", err)
	}
	if _, err := SaveSnapshot.Call(ctx, c, SaveSnapshotInput{Snapshot: want[1].Path, Dest: "/srv/racksail/root.btrfs"}, CallOptions{}); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("saving a system snapshot: %v", err)
	}
	if _, err := CreateSnapshot.Call(ctx, c, SnapshotInput{Source: want[1].Path, Dest: blog + "/root"}, CallOptions{}); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("copying a system snapshot: %v", err)
	}
	if _, err := EnsureSubvolume.Call(ctx, c, SubvolumeInput{Path: sealed + "/root-00000000"}, CallOptions{}); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("making a subvolume among the system's snapshots: %v", err)
	}
	for _, p := range []string{SystemSnapshotDir, sealed, "/.snapshots/other/etc/shadow", "/.snapshots"} {
		if _, err := StatPath.Call(ctx, c, PathInput{Path: p}, CallOptions{}); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("stat %s: %v", p, err)
		}
		if _, err := EnsureSymlink.Call(ctx, c, EnsureSymlinkInput{Path: p, Target: blog}, CallOptions{}); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("a link at %s: %v", p, err)
		}
	}
	again, err := SnapshotPaths.Call(ctx, c, in, CallOptions{})
	if err != nil || again.Changed || !slices.Equal(again.Snapshots, want) {
		t.Errorf("taking them again = %+v, %v", again, err)
	}
	if _, err := SnapshotPaths.Call(ctx, c, SnapshotPathsInput{Label: "../etc", Paths: []string{blog}}, CallOptions{}); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("a label with a path in it: %v", err)
	}
	for _, s := range want {
		if _, err := DeleteSubvolume.Call(ctx, c, SubvolumeInput{Path: s.Path}, CallOptions{}); err != nil {
			t.Errorf("deleting %s: %v", s.Path, err)
		}
	}
	if _, err := b.Files.Stat(ctx, want[1].Path); !errors.Is(err, system.ErrNotFound) {
		t.Errorf("the system's snapshot is still there: %v", err)
	}
	if _, err := DeleteSubvolume.Call(ctx, c, SubvolumeInput{Path: sealed + "/../../etc"}, CallOptions{}); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("deleting outside the system's snapshots: %v", err)
	}
	if _, err := RemovePath.Call(ctx, c, RemovePathInput{Path: dir}, CallOptions{}); err != nil {
		t.Errorf("removing the emptied directory: %v", err)
	}
}

func TestNetworkAndSystemOps(t *testing.T) {
	ctx := context.Background()
	m, c := newLocal(t)
	ruleset := "table inet racksail {\n  set bans { type ipv4_addr; flags timeout; }\n  chain input { type filter hook input priority 0; policy drop; }\n}\n"
	if res, err := ApplyFirewall.Call(ctx, c, ApplyFirewallInput{Ruleset: ruleset}, CallOptions{}); err != nil || !res.Changed {
		t.Fatalf("firewall = %+v, %v", res, err)
	}
	if res, err := ApplyFirewall.Call(ctx, c, ApplyFirewallInput{Ruleset: ruleset}, CallOptions{}); err != nil || res.Changed {
		t.Errorf("the same ruleset is no change: %+v, %v", res, err)
	}
	for _, bad := range []string{
		"flush ruleset\ntable inet filter {}\n",
		"flush ruleset\n" + ruleset,
		"include \"/etc/shadow\"\n" + ruleset,
		"table inet racksail {\n include \"/root/.ssh/id_ed25519\"\n}\n",
		ruleset + "add rule inet filter input accept\n",
		ruleset + "delete table inet filter\n",
	} {
		if _, err := ApplyFirewall.Call(ctx, c, ApplyFirewallInput{Ruleset: bad}, CallOptions{}); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("ruleset %q: %v", bad, err)
		}
	}
	bans := SetElementsInput{Set: system.SetRef{Family: "inet", Table: "racksail", Set: "bans"}, Elements: []system.SetElement{{Value: "198.51.100.4"}}}
	if _, err := AddFirewallElements.Call(ctx, c, bans, CallOptions{}); err != nil {
		t.Fatal(err)
	}
	list, _ := ListFirewallElements.Call(ctx, c, SetRefInput{Set: bans.Set}, CallOptions{})
	if len(list.Elements) != 1 {
		t.Errorf("bans = %+v", list)
	}
	files := NetworkConfigInput{Files: map[string]string{"50-racksail-enp1s0.network": "[Match]\nName=enp1s0\n"}}
	if res, err := ApplyNetworkConfig.Call(ctx, c, files, CallOptions{}); err != nil || !res.Changed {
		t.Errorf("network = %+v, %v", res, err)
	}
	if res, _ := ApplyNetworkConfig.Call(ctx, c, files, CallOptions{}); res.Changed {
		t.Error("the same network files are no change")
	}
	rendered, err := system.NetworkConfig{
		NetDevs:  []system.NetDev{{Name: "br0", Kind: "bridge"}},
		Networks: []system.NetworkFile{{Name: "enp1s0", MatchName: "enp1s0", Bridge: "br0"}, {Name: "br0", MatchName: "br0", DHCP: "yes"}},
	}.Render()
	if err != nil {
		t.Fatal(err)
	}
	renderedFiles := map[string]string{}
	for name, data := range rendered {
		renderedFiles[name] = string(data)
	}
	if _, err := ApplyNetworkConfig.Call(ctx, c, NetworkConfigInput{Files: renderedFiles}, CallOptions{Check: true}); err != nil {
		t.Errorf("rendered network files: %v", err)
	}
	for _, bad := range []map[string]string{
		{"50-racksail-lan.link": "[Match]\nOriginalName=enp1s0\n[Link]\nName=lan0\n"},
		{"50-racksail-enp1s0.network": "[Match]\nName=enp1s0\n[Network]\nIPForward=yes\n"},
		{"50-racksail-all.network": "[Network]\nDHCP=yes\n"},
		{"50-racksail-wg0.netdev": "[NetDev]\nName=wg0\nKind=wireguard\n[WireGuard]\nPrivateKeyFile=/etc/shadow\n"},
		{"10-other.network": "[Match]\nName=enp1s0\n"},
	} {
		if _, err := ApplyNetworkConfig.Call(ctx, c, NetworkConfigInput{Files: bad}, CallOptions{Check: true}); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("network files %v: %v", bad, err)
		}
	}
	sysctl := SysctlInput{Name: "network", Values: map[string]string{"net.core.default_qdisc": "fq", "net.ipv4.tcp_congestion_control": "bbr"}}
	if res, err := SetSysctl.Call(ctx, c, sysctl, CallOptions{}); err != nil || !res.Changed {
		t.Errorf("sysctl = %+v, %v", res, err)
	}
	if res, _ := SetSysctl.Call(ctx, c, sysctl, CallOptions{}); res.Changed {
		t.Error("the same parameters are no change")
	}
	for k, v := range map[string]string{
		"vm.mmap_min_addr":              "0",
		"vm.unprivileged_userfaultfd":   "1",
		"net.core.bpf_jit_harden":       "0",
		"kernel.core_pattern":           "|/srv/racksail/accounts/x/evil",
		"net.ipv4.conf.eth0.forwarding": "1\rkernel.core_pattern = |/tmp/x",
	} {
		if _, err := SetSysctl.Call(ctx, c, SysctlInput{Name: "x", Values: map[string]string{k: v}}, CallOptions{}); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("%s=%q should be refused: %v", k, v, err)
		}
	}
	if _, err := SetSysctl.Call(ctx, c, SysctlInput{Name: "routing", Values: map[string]string{"net.ipv4.conf.eth0.forwarding": "1"}}, CallOptions{}); err != nil {
		t.Errorf("a per-interface parameter: %v", err)
	}
	if _, err := SetSysctl.Call(ctx, c, SysctlInput{Name: "x", Values: map[string]string{"kernel.modules_disabled": "1"}}, CallOptions{}); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("a parameter outside the allowlist: %v", err)
	}
	procs, _ := ListProcesses.Call(ctx, c, ProcessFilterInput{}, CallOptions{})
	var sshPID int
	for _, p := range procs.Processes {
		if p.Unit == "ssh.service" {
			sshPID = p.PID
		}
	}
	if _, err := SignalProcess.Call(ctx, c, SignalInput{PID: sshPID, Signal: system.SIGKILL}, CallOptions{}); !errors.Is(err, ErrNotAllowed) {
		t.Errorf("killing sshd: %v", err)
	}
	facts, err := GetFacts.Call(ctx, c, Empty{}, CallOptions{})
	if err != nil || facts.Hostname != "agent-test" {
		t.Errorf("facts = %+v, %v", facts, err)
	}
	_ = m
}

func TestExecuteAndPlannedEffects(t *testing.T) {
	ctx := context.Background()
	m, c := newLocal(t)
	steps := []Step{
		EnsureDir.Step(EnsureDirInput{Path: "/srv/racksail/accounts/shop", Mode: 0o750, Parents: true}),
		WriteFile.Titled("Write the index page", WriteFileInput{Path: "/srv/racksail/accounts/shop/index.html", Content: []byte("hi"), Mode: 0o644}),
	}
	effects, err := PlannedEffects(steps)
	if err != nil || len(effects) != 2 {
		t.Fatalf("planned effects = %+v, %v", effects, err)
	}
	var seen []string
	results, err := Execute(ctx, c, steps, CallOptions{Owner: store.NewID("site")}, func(i int, s Step) { seen = append(seen, s.Title) })
	if err != nil || len(results) != 2 || !results[1].Result.Changed || seen[1] != "Write the index page" {
		t.Fatalf("execute = %+v, %v (%v)", results, err, seen)
	}
	m.FailNext("files.write", "/srv/racksail/accounts/shop/b.html", errors.New("disk full"))
	failing := append(steps, WriteFile.Step(WriteFileInput{Path: "/srv/racksail/accounts/shop/b.html", Mode: 0o644}))
	if _, err := Execute(ctx, c, failing, CallOptions{}, nil); !errors.Is(err, ErrFailed) || !strings.Contains(err.Error(), "step 3") {
		t.Errorf("a failing step: %v", err)
	}
	if _, err := c.Call(ctx, "NoSuchOperation", json.RawMessage(`{}`), CallOptions{}); !errors.Is(err, ErrUnknownOperation) {
		t.Errorf("unknown operation: %v", err)
	}
	if _, err := c.Call(ctx, "WriteFile", json.RawMessage(`{"path":"/srv/racksail/x","mode":"0644","content":"","extra":1}`), CallOptions{}); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("unknown fields must be refused: %v", err)
	}
}

func TestHTTPServerRoundTrip(t *testing.T) {
	ctx := context.Background()
	m := sim.New(sim.Options{Hostname: "remote"})
	srv := NewServer(m.Backend(), "test", nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	client := NewHTTPClient(ts.URL, ts.Client())

	h, err := client.Health(ctx)
	if err != nil || h.Status != "ok" || h.Backend != "sim" {
		t.Fatalf("health = %+v, %v", h, err)
	}
	res, err := WriteFile.Call(ctx, client, WriteFileInput{Path: "/etc/racksail/generated/a.conf", Content: []byte("a"), Mode: 0o644}, CallOptions{Check: true})
	if err != nil || !res.Changed {
		t.Fatalf("check over HTTP = %+v, %v", res, err)
	}
	facts, err := GetFacts.Call(ctx, client, Empty{}, CallOptions{})
	if err != nil || facts.Hostname != "remote" {
		t.Errorf("facts over HTTP = %+v, %v", facts, err)
	}
	_, err = WriteFile.Call(ctx, client, WriteFileInput{Path: "/etc/shadow", Mode: 0o644}, CallOptions{})
	var coded *i18n.Error
	if !errors.As(err, &coded) || coded.Code != ErrInvalidInput.Code || coded.Status != 422 {
		t.Errorf("errors keep their code over HTTP: %v", err)
	}
	if _, err := client.Call(ctx, "NoSuchOperation", json.RawMessage(`{}`), CallOptions{}); err == nil {
		t.Error("unknown operations must fail over HTTP")
	}
	unreachable := NewHTTPClient("http://127.0.0.1:1", nil)
	if _, err := unreachable.Health(ctx); !errors.Is(err, ErrUnreachable) {
		t.Errorf("unreachable agent: %v", err)
	}
}

// Files are written and read whole, so the agent's socket must carry the
// largest file WriteFile accepts, base64-encoded in JSON.
func TestBodyLimitFitsTheLargestFile(t *testing.T) {
	if need := base64.StdEncoding.EncodedLen(maxFileContent) + 64<<10; maxBody < need {
		t.Errorf("maxBody is %d bytes, a full-size WriteFile needs %d", maxBody, need)
	}
}

// Calls through the socket go through Huma's request handling, which the
// in-process client skips, so large requests are tried over a real socket.
func TestLargeFilesOverTheSocket(t *testing.T) {
	// Unix socket paths are limited to about 100 bytes, which the test's own
	// temporary directory can exceed.
	dir, err := os.MkdirTemp("", "ag")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "agent.sock")
	ln, err := ListenUnix(sock, 0o600, -1)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- NewServer(sim.New(sim.Options{}).Backend(), "test", nil).Serve(ctx, ln) }()
	defer func() {
		cancel()
		if err := <-served; err != nil {
			t.Errorf("serve: %v", err)
		}
	}()
	client := DialUnix(sock)

	content := bytes.Repeat([]byte("server {\n  listen 443;\n}\n"), (3<<20)/27)
	in := WriteFileInput{Path: "/etc/racksail/generated/firewall.nft", Content: content, Mode: 0o640}
	res, err := WriteFile.Call(ctx, client, in, CallOptions{})
	if err != nil || !res.Changed {
		t.Fatalf("writing %d bytes over the socket = %+v, %v", len(content), res, err)
	}
	got, err := ReadFile.Call(ctx, client, ReadFileInput{Path: in.Path, MaxBytes: 4 << 20}, CallOptions{})
	if err != nil || !bytes.Equal(got.Content, content) {
		t.Errorf("reading it back = %d bytes, %v", len(got.Content), err)
	}
	_, err = client.Call(ctx, "WriteFile", bytes.Repeat([]byte(" "), maxBody+1), CallOptions{})
	var coded *i18n.Error
	if !errors.As(err, &coded) || coded.Code != i18n.ErrTooLarge.Code {
		t.Errorf("a request over the limit: %v", err)
	}
}

func TestProtocolDocument(t *testing.T) {
	doc, err := NewServer(nil, "test", nil).OpenAPI()
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		OpenAPI string         `json:"openapi"`
		Paths   map[string]any `json:"paths"`
	}
	if err := json.Unmarshal(doc, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.OpenAPI != "3.1.0" || parsed.Paths["/v1/ops/WriteFile"] == nil || parsed.Paths["/v1/health"] == nil {
		t.Errorf("unexpected document: openapi %s, %d paths", parsed.OpenAPI, len(parsed.Paths))
	}
}

func TestMode(t *testing.T) {
	var m Mode
	if err := m.UnmarshalText([]byte("0755")); err != nil || m != 0o755 {
		t.Errorf("mode = %o, %v", m, err)
	}
	if b, _ := Mode(0o1777).MarshalText(); string(b) != "1777" {
		t.Errorf("text = %s", b)
	}
	if err := m.UnmarshalText([]byte("rwx")); err == nil {
		t.Error("non-octal modes must fail")
	}
}
