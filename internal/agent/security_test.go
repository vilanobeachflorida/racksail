package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vilanobeachflorida/racksail/internal/system"
	"github.com/vilanobeachflorida/racksail/internal/system/sim"
)

// EnsureUser must never change a user Racksail doesn't manage: not root, not
// an administrator's own login user. A matching no-op is still fine, since it
// changes nothing.
func TestEnsureUserOnlyChangesManagedUsers(t *testing.T) {
	ctx := context.Background()
	m, c := newLocal(t)
	b := m.Backend()
	// An administrator's login user, in the regular UID range.
	if err := b.Users.CreateUser(ctx, system.User{Name: "admin", UID: 1000, Home: "/home/admin", Shell: "/bin/bash", Groups: []string{"sudo"}}); err != nil {
		t.Fatal(err)
	}

	// Locking root, or moving its home, is refused.
	if _, err := EnsureUser.Call(ctx, c, EnsureUserInput{Name: "root", System: true, Home: "/root", Shell: "/bin/bash", Locked: true}, CallOptions{}); !errors.Is(err, ErrNotAllowed) {
		t.Errorf("locking root: %v", err)
	}
	if _, err := EnsureUser.Call(ctx, c, EnsureUserInput{Name: "root", System: true, Home: "/root/moved", Shell: "/bin/bash"}, CallOptions{Check: true}); !errors.Is(err, ErrNotAllowed) {
		t.Errorf("moving root's home in check mode: %v", err)
	}
	// Taking the administrator out of a group, or changing their shell, is
	// refused for the same reason.
	if _, err := EnsureUser.Call(ctx, c, EnsureUserInput{Name: "admin", System: true, Home: "/home/admin", Shell: "/usr/sbin/nologin"}, CallOptions{}); !errors.Is(err, ErrNotAllowed) {
		t.Errorf("changing an administrator's login: %v", err)
	}
	if u, _ := b.Users.LookupUser(ctx, "admin"); u.Shell != "/bin/bash" || !hasGroup(u, "sudo") {
		t.Errorf("the administrator was changed: %+v", u)
	}

	// A request that matches root exactly changes nothing and is allowed.
	if res, err := EnsureUser.Call(ctx, c, EnsureUserInput{Name: "root", System: true, Home: "/root", Shell: "/bin/bash"}, CallOptions{}); err != nil || res.Changed {
		t.Errorf("a no-op on root = %+v, %v", res, err)
	}

	// An account can be created and then changed.
	acct := EnsureUserInput{Name: "blog", UID: 20001, GID: 20001, Home: system.AccountDir("blog")}
	newAccount(t, c, "blog", 20001)
	acct.Locked = true
	if res, err := EnsureUser.Call(ctx, c, acct, CallOptions{}); err != nil || !res.Changed {
		t.Errorf("locking an account = %+v, %v", res, err)
	}
}

func hasGroup(u system.User, name string) bool {
	for _, g := range u.Groups {
		if g == name {
			return true
		}
	}
	return false
}

// An account's home has to be the account's own directory under the accounts
// area, so it can't point at a location the panel itself writes, nor at the
// accounts' parent or a sibling such as the replica or backups staging.
func TestEnsureUserKeepsAccountHomesInTheAccountsArea(t *testing.T) {
	ctx := context.Background()
	_, c := newLocal(t)
	for _, home := range []string{
		"/etc/racksail/generated/blog", "/opt/racksail/runtimes/blog", "/var/lib/racksail/blog",
		system.AccountsDir, "/srv/racksail", "/srv/racksail/replica", "/srv/racksail/backups",
	} {
		in := EnsureUserInput{Name: "blog", UID: 20002, GID: 20002, Home: home}
		if _, err := EnsureUser.Call(ctx, c, in, CallOptions{}); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("home %s should be refused: %v", home, err)
		}
	}
	if _, err := EnsureUser.Call(ctx, c, EnsureUserInput{Name: "blog", UID: 20002, GID: 20002, Home: system.AccountDir("blog")}, CallOptions{Check: true}); err != nil {
		t.Errorf("a home in the accounts area: %v", err)
	}
}

// Dash-prefix drop-in targets reach every unit under the prefix, so a
// racksail-.service drop-in would adjust the agent, the panel, the edge and
// the add-on instances at once. They are refused, as are the type-wide forms.
func TestDropInsRefuseDashPrefixTargets(t *testing.T) {
	ctx := context.Background()
	_, c := newLocal(t)
	limit := []byte("[Service]\nMemoryMax=1G\n")
	for _, unit := range []string{"racksail-.service", "racksail-.slice", "racksail-.socket", "racksail-accounts-.slice", "-.service", "-.slice"} {
		if _, err := EnsureDropIn.Call(ctx, c, DropInInput{Unit: unit, Name: "racksail-extra", Content: limit}, CallOptions{}); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("a drop-in for the prefix %s should be refused: %v", unit, err)
		}
		if _, err := RemoveDropIn.Call(ctx, c, DropInInput{Unit: unit, Name: "racksail-extra"}, CallOptions{}); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("removing a drop-in for the prefix %s should be refused: %v", unit, err)
		}
	}
}

// EnsureUser and DeleteUser leave the platform's own identities alone, since
// only the installer and the self-update paths make them. EnsureUser can
// still create them when they're absent, as the installer does.
func TestPlatformUsersAreNotPanelManageable(t *testing.T) {
	ctx := context.Background()
	m, c := newLocal(t)
	b := m.Backend()
	// racksail-edge is one of the platform users the installed server already
	// has. Changing its home, shell or lock state is refused.
	change := EnsureUserInput{Name: "racksail-edge", System: true, Home: system.StateDir, Shell: "/bin/bash"}
	if _, err := EnsureUser.Call(ctx, c, change, CallOptions{}); !errors.Is(err, ErrNotAllowed) {
		t.Errorf("changing racksail-edge: %v", err)
	}
	if _, err := DeleteUser.Call(ctx, c, DeleteUserInput{Name: "racksail-edge"}, CallOptions{}); !errors.Is(err, ErrNotAllowed) {
		t.Errorf("deleting racksail-edge: %v", err)
	}
	if u, _ := b.Users.LookupUser(ctx, "racksail-edge"); u.Shell != "/usr/sbin/nologin" || !u.Locked {
		t.Errorf("racksail-edge was changed: %+v", u)
	}
}

// A new system user or group must take a racksail-* name, and a new account
// an account-range ID, so a name a distribution package might take later
// can't be claimed in advance with panel-chosen settings.
func TestCreationHeldToOwnershipAllowlist(t *testing.T) {
	ctx := context.Background()
	_, c := newLocal(t)
	// A system user or group with a free name is refused.
	if _, err := EnsureUser.Call(ctx, c, EnsureUserInput{Name: "ssl-mgr", System: true, Home: "/nonexistent"}, CallOptions{}); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("a free-named system user: %v", err)
	}
	if _, err := EnsureGroup.Call(ctx, c, EnsureGroupInput{Name: "ssl-mgr", System: true}, CallOptions{}); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("a free-named system group: %v", err)
	}
	// An account created with a UID the system would choose, outside the
	// account range, is refused once it is really applied.
	if _, err := EnsureUser.Call(ctx, c, EnsureUserInput{Name: "blog", Home: system.AccountDir("blog")}, CallOptions{}); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("an account with a system-chosen uid: %v", err)
	}
	// racksail-* names and account-range IDs are accepted.
	if _, err := EnsureUser.Call(ctx, c, EnsureUserInput{Name: "racksail-mail", System: true, Home: "/nonexistent"}, CallOptions{Check: true}); err != nil {
		t.Errorf("a racksail-* system user: %v", err)
	}
	if _, err := EnsureGroup.Call(ctx, c, EnsureGroupInput{Name: "blog", GID: 20001}, CallOptions{Check: true}); err != nil {
		t.Errorf("an account-range group: %v", err)
	}
}

// A racksail-* system user's primary group is held to the ownership
// allowlist, so a fixed GID can't make it a member of a key-reading group
// such as ssl-cert, and a panel-written unit can't then run as it.
func TestPrimaryGroupHeldToTheAllowlist(t *testing.T) {
	ctx := context.Background()
	m, c := newLocal(t)
	b := m.Backend()
	if err := b.Users.CreateGroup(ctx, system.Group{Name: "ssl-cert", GID: 113, System: true}); err != nil {
		t.Fatal(err)
	}
	// Creating a racksail-* user whose primary group is ssl-cert is refused.
	in := EnsureUserInput{Name: "racksail-web", System: true, GID: 113, Home: "/nonexistent"}
	if _, err := EnsureUser.Call(ctx, c, in, CallOptions{}); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("a racksail-* user homed into ssl-cert's group: %v", err)
	}
	// Even if such a user exists, a unit written for the panel won't run as
	// it, because its primary group isn't one Racksail owns.
	if err := b.Users.CreateUser(ctx, system.User{Name: "racksail-web", UID: 981, GID: 113, System: true, Home: "/nonexistent", Shell: "/usr/sbin/nologin"}); err != nil {
		t.Fatal(err)
	}
	unit := EnsureUnitInput{Name: "racksail-web.service", Content: []byte("[Service]\nUser=racksail-web\nExecStart=/usr/bin/true\n")}
	if _, err := EnsureUnit.Call(ctx, c, unit, CallOptions{}); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("a service running as a user in a key-reading group: %v", err)
	}
}

// SetUnitProperties with runtime false writes its persistent drop-in even
// when the effective value matches only because of an earlier runtime
// override, so the limit survives a reboot.
func TestSetUnitPropertiesPersistsOverRuntimeOverride(t *testing.T) {
	ctx := context.Background()
	m, c := newLocal(t)
	newAccount(t, c, "blog", 20001)
	slice, err := system.AccountSliceSpec("blog", system.ResourceLimits{MemoryMaxBytes: 1 << 30}).Render()
	if err != nil {
		t.Fatal(err)
	}
	name := system.AccountSlice("blog")
	if _, err := EnsureUnit.Call(ctx, c, EnsureUnitInput{Name: name, Content: slice}, CallOptions{}); err != nil {
		t.Fatal(err)
	}
	// A runtime change raises the live limit to 2G without persisting it.
	if res, err := SetUnitProperties.Call(ctx, c, SetUnitPropertiesInput{Name: name, Runtime: true, Properties: []system.UnitProperty{{Name: "MemoryMax", Value: "2G"}}}, CallOptions{}); err != nil || !res.Changed {
		t.Fatalf("runtime limit = %+v, %v", res, err)
	}
	// Asking for the same value persistently still counts as a change, since
	// the persistent layer is still 1G, and it must write the drop-in.
	if res, err := SetUnitProperties.Call(ctx, c, SetUnitPropertiesInput{Name: name, Properties: []system.UnitProperty{{Name: "MemoryMax", Value: "2G"}}}, CallOptions{}); err != nil || !res.Changed {
		t.Fatalf("persistent limit after a runtime override = %+v, %v", res, err)
	}
	// After a reboot the runtime override is gone, but the persistent drop-in
	// keeps the limit at 2G.
	m.Reboot()
	if v, _ := m.Property(name, "MemoryMax"); v != "2G" {
		t.Errorf("persistent MemoryMax after reboot = %q", v)
	}
	if res, _ := SetUnitProperties.Call(ctx, c, SetUnitPropertiesInput{Name: name, Properties: []system.UnitProperty{{Name: "MemoryMax", Value: "2G"}}}, CallOptions{}); res.Changed {
		t.Error("the persistent limit is already in place, so setting it again should change nothing")
	}
}

// Racksail only changes the membership of groups it owns, and only adds
// accounts to its own groups, so an account can't be slipped into a group
// such as ssl-cert that reads private keys.
func TestGroupMembershipStaysWithinRacksailsGroups(t *testing.T) {
	ctx := context.Background()
	m, c := newLocal(t)
	b := m.Backend()
	if err := b.Users.CreateGroup(ctx, system.Group{Name: "ssl-cert", GID: 113, System: true}); err != nil {
		t.Fatal(err)
	}
	newAccount(t, c, "blog", 20001)

	// Rewriting a group Racksail doesn't own is refused.
	members := []string{"blog"}
	if _, err := EnsureGroup.Call(ctx, c, EnsureGroupInput{Name: "ssl-cert", Members: &members}, CallOptions{}); !errors.Is(err, ErrNotAllowed) {
		t.Errorf("adding an account to ssl-cert: %v", err)
	}
	if g, _ := b.Users.LookupGroup(ctx, "ssl-cert"); len(g.Members) != 0 {
		t.Errorf("ssl-cert gained members: %+v", g)
	}
	// So is adding an account to it as a supplementary group.
	if _, err := EnsureUser.Call(ctx, c, EnsureUserInput{Name: "blog2", UID: 20002, GID: 20002, Home: system.AccountDir("blog2"), Groups: []string{"ssl-cert"}}, CallOptions{}); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("an account homed into ssl-cert: %v", err)
	}
	// Claiming a specific system GID for a new group is refused.
	if _, err := EnsureGroup.Call(ctx, c, EnsureGroupInput{Name: "racksail-shared", GID: 500}, CallOptions{}); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("a group with a system GID: %v", err)
	}
	// An account's own group can be changed.
	accountMembers := []string{"blog"}
	if res, err := EnsureGroup.Call(ctx, c, EnsureGroupInput{Name: "blog", GID: 20001, Members: &accountMembers}, CallOptions{}); err != nil || !res.Changed {
		t.Errorf("setting an account group's members = %+v, %v", res, err)
	}
}

// Drop-ins never reach Racksail's own units or the root system services whose
// files the agent renders itself, so a compromised panel can't adjust them.
func TestDropInsRefusePrivilegedUnits(t *testing.T) {
	ctx := context.Background()
	_, c := newLocal(t)
	limit := []byte("[Service]\nMemoryMax=1G\n")
	for _, unit := range []string{
		"nftables.service", "systemd-networkd.service", "systemd-timesyncd.service",
		system.AgentUnit, system.PanelUnit, system.EdgeUnit, system.PlatformSlice, system.AccountsSlice,
		"racksail-addon@site.service",
	} {
		in := DropInInput{Unit: unit, Name: "racksail-extra", Content: limit}
		if _, err := EnsureDropIn.Call(ctx, c, in, CallOptions{}); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("a drop-in for %s should be refused: %v", unit, err)
		}
		if _, err := RemoveDropIn.Call(ctx, c, DropInInput{Unit: unit, Name: "racksail-extra"}, CallOptions{}); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("removing a drop-in for %s should be refused: %v", unit, err)
		}
	}
	// The panel can't write the add-on service family either; the agent
	// renders it from typed input through EnsureAddonService.
	for _, name := range []string{"racksail-addon@.service", "racksail-addon@site.service"} {
		body := sandboxed("[Service]\nUser=blog\nExecStart=/bin/true\n")
		if _, err := EnsureUnit.Call(ctx, c, EnsureUnitInput{Name: name, Content: body}, CallOptions{}); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("writing %s should be refused: %v", name, err)
		}
	}
	// A distribution service a module manages still takes drop-ins.
	AllowUnit("mariadb.service")
	newAccount(t, c, "blog", 20001)
	if _, err := EnsureDropIn.Call(ctx, c, DropInInput{Unit: "mariadb.service", Name: "racksail-limits", Content: limit}, CallOptions{Check: true}); err != nil {
		t.Errorf("a drop-in for a managed service: %v", err)
	}
}

// SetUnitProperties reports no change when the unit's limits already have the
// asked values, so a plan that runs twice does nothing the second time.
func TestSetUnitPropertiesIsIdempotent(t *testing.T) {
	ctx := context.Background()
	m, c := newLocal(t)
	newAccount(t, c, "blog", 20001)
	slice, err := system.AccountSliceSpec("blog", system.ResourceLimits{MemoryMaxBytes: 1 << 30, CPUQuotaPercent: 200, TasksMax: 512}).Render()
	if err != nil {
		t.Fatal(err)
	}
	name := system.AccountSlice("blog")
	if _, err := EnsureUnit.Call(ctx, c, EnsureUnitInput{Name: name, Content: slice}, CallOptions{}); err != nil {
		t.Fatal(err)
	}

	// The slice already holds these limits, written in another spelling, so
	// setting them again changes nothing.
	for _, p := range []system.UnitProperty{{Name: "MemoryMax", Value: "1G"}, {Name: "CPUQuota", Value: "200%"}, {Name: "TasksMax", Value: "512"}} {
		res, err := SetUnitProperties.Call(ctx, c, SetUnitPropertiesInput{Name: name, Properties: []system.UnitProperty{p}}, CallOptions{})
		if err != nil || res.Changed {
			t.Errorf("setting %s=%s again = %+v, %v", p.Name, p.Value, res, err)
		}
	}

	// A different value is a change, and then setting it again is not.
	if res, err := SetUnitProperties.Call(ctx, c, SetUnitPropertiesInput{Name: name, Properties: []system.UnitProperty{{Name: "MemoryMax", Value: "2G"}}}, CallOptions{}); err != nil || !res.Changed {
		t.Errorf("a new memory limit = %+v, %v", res, err)
	}
	if res, _ := SetUnitProperties.Call(ctx, c, SetUnitPropertiesInput{Name: name, Properties: []system.UnitProperty{{Name: "MemoryMax", Value: "2G"}}}, CallOptions{}); res.Changed {
		t.Error("the same memory limit again should change nothing")
	}
	if v, _ := m.Property(name, "MemoryMax"); v != "2G" {
		t.Errorf("MemoryMax override = %q", v)
	}

	// A CPU set can't be compared with certainty, so it is always applied.
	for range 2 {
		if res, err := SetUnitProperties.Call(ctx, c, SetUnitPropertiesInput{Name: name, Properties: []system.UnitProperty{{Name: "AllowedCPUs", Value: "0-1"}}}, CallOptions{}); err != nil || !res.Changed {
			t.Errorf("setting a CPU set = %+v, %v", res, err)
		}
	}
}

// PartitionDisk and FormatDevice install the tools Debian 13 doesn't ship by
// default before they run, so the operations work on a fresh server. The
// install shows up in the preview.
func TestDiskOperationsInstallTheirTools(t *testing.T) {
	ctx := context.Background()
	m, c := newLocal(t)
	part := PartitionInput{Disk: "sda", Partitions: []system.PartitionSpec{{Number: 1, Type: "linux"}}}
	res, err := PartitionDisk.Call(ctx, c, part, CallOptions{Check: true})
	if err != nil || !installs(res.Effects, "gdisk") {
		t.Errorf("partition preview = %+v, %v", res, err)
	}
	if res, err := PartitionDisk.Call(ctx, c, part, CallOptions{}); err != nil || !res.Changed {
		t.Fatalf("partition = %+v, %v", res, err)
	}
	if ins, _ := m.Backend().Packages.Installed(ctx, "gdisk"); len(ins) == 0 {
		t.Error("gdisk wasn't installed")
	}

	for _, tc := range []struct{ fs, pkg string }{{"xfs", "xfsprogs"}, {"vfat", "dosfstools"}} {
		res, err := FormatDevice.Call(ctx, c, FormatInput{Spec: system.FormatSpec{Type: tc.fs, Devices: []string{"/dev/sdb"}}}, CallOptions{Check: true})
		if err != nil || !installs(res.Effects, tc.pkg) {
			t.Errorf("%s preview = %+v, %v", tc.fs, res, err)
		}
	}
	// A filesystem whose tools are already present installs nothing more.
	res, err = FormatDevice.Call(ctx, c, FormatInput{Spec: system.FormatSpec{Type: "btrfs", Devices: []string{"/dev/sdb"}}}, CallOptions{Check: true})
	if err != nil || len(res.Effects) != 1 || res.Effects[0].Type != system.EffectStorage {
		t.Errorf("btrfs preview = %+v, %v", res, err)
	}
}

func installs(effects []system.Effect, pkg string) bool {
	for _, e := range effects {
		if e.Type == system.EffectPackage && e.Action == "install" && e.Target == pkg {
			return true
		}
	}
	return false
}

// Every call the agent refuses before it reaches an operation is recorded,
// stream calls and requests to unknown paths included, not only operations.
func TestRefusedStreamsAndUnknownPathsAreAudited(t *testing.T) {
	m := sim.New(sim.Options{Hostname: "stream-audit"})
	srv := NewServer(m.Backend(), "test", nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// A malformed stream request body.
	resp, err := http.Post(ts.URL+"/v1/streams/Download", "application/json", strings.NewReader("not json"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	// An upload with an unparseable mode.
	resp, err = http.Post(ts.URL+"/v1/streams/Upload?path=/srv/racksail/x&mode=notamode", "application/octet-stream", strings.NewReader("data"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	// Paths no handler serves, under /v1 and outside it.
	for _, p := range []string{"/v1/nonsense", "/", "/x"} {
		resp, err = http.Post(ts.URL+p, "application/json", strings.NewReader("{}"))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}

	records, _ := srv.Runtime().Audit().Recent(AuditFilter{})
	want := map[string]bool{"streams/Download": false, "streams/Upload": false, "/v1/nonsense": false, "/": false, "/x": false}
	for _, r := range records {
		if _, ok := want[r.Op]; ok {
			if !r.Failed() {
				t.Errorf("record for %s isn't a refusal: %+v", r.Op, r)
			}
			want[r.Op] = true
		}
	}
	for op, seen := range want {
		if !seen {
			t.Errorf("no audit record for the refused call %q", op)
		}
	}

	// The health check answers normally and isn't recorded as a refused call.
	resp, err = http.Get(ts.URL + "/v1/health")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	health, _ := srv.Runtime().Audit().Recent(AuditFilter{Op: "health"})
	if len(health) != 0 {
		t.Errorf("the health check was recorded: %v", health)
	}
}

// ApplyNetworkConfig for good refuses while an earlier change still waits for
// confirmation, since that change's rollback would otherwise undo it.
func TestApplyingNetworkForGoodRefusesAPendingChange(t *testing.T) {
	ctx := context.Background()
	_, c := newLocal(t)
	pending := NetworkConfigInput{Files: map[string]string{"50-racksail-enp1s0.network": "[Match]\nName=enp1s0\n[Network]\nDHCP=yes\n"}, ConfirmWithin: 600}
	if _, err := ApplyNetworkConfig.Call(ctx, c, pending, CallOptions{}); err != nil {
		t.Fatal(err)
	}
	forGood := NetworkConfigInput{Files: map[string]string{"50-racksail-enp1s0.network": "[Match]\nName=enp1s0\n[Network]\nAddress=203.0.113.9/24\n"}}
	if _, err := ApplyNetworkConfig.Call(ctx, c, forGood, CallOptions{}); !errors.Is(err, ErrConflict) {
		t.Errorf("applying for good while a change is pending: %v", err)
	}
}

// A persistent request for a limit the unit's configuration already keeps
// still changes the live value when a runtime override holds another one,
// as when a plan restores a limit it throttled at runtime: it reports a
// change only when the asked limit isn't in effect or isn't kept.
func TestSetUnitPropertiesRestoresTheLiveLimit(t *testing.T) {
	ctx := context.Background()
	m, c := newLocal(t)
	newAccount(t, c, "blog", 20001)
	slice, err := system.AccountSliceSpec("blog", system.ResourceLimits{MemoryMaxBytes: 1 << 30}).Render()
	if err != nil {
		t.Fatal(err)
	}
	name := system.AccountSlice("blog")
	if _, err := EnsureUnit.Call(ctx, c, EnsureUnitInput{Name: name, Content: slice}, CallOptions{}); err != nil {
		t.Fatal(err)
	}
	limit := func(v string) SetUnitPropertiesInput {
		return SetUnitPropertiesInput{Name: name, Properties: []system.UnitProperty{{Name: "MemoryMax", Value: v}}}
	}
	throttle := limit("512M")
	throttle.Runtime = true
	if res, err := SetUnitProperties.Call(ctx, c, throttle, CallOptions{}); err != nil || !res.Changed {
		t.Fatalf("throttling at runtime = %+v, %v", res, err)
	}
	// The persistent configuration still says 1G, but the live limit is
	// 512M, so restoring 1G is a change, in check mode as when applied.
	if res, err := SetUnitProperties.Call(ctx, c, limit("1G"), CallOptions{Check: true}); err != nil || !res.Changed {
		t.Errorf("restoring in check mode = %+v, %v", res, err)
	}
	if res, err := SetUnitProperties.Call(ctx, c, limit("1G"), CallOptions{}); err != nil || !res.Changed {
		t.Fatalf("restoring = %+v, %v", res, err)
	}
	if live, _ := m.Backend().Units.ResourceControl(ctx, name, true); live["MemoryMax"] != "1073741824" {
		t.Errorf("live limits after restoring = %v", live)
	}
	if res, _ := SetUnitProperties.Call(ctx, c, limit("1G"), CallOptions{}); res.Changed {
		t.Error("the restored limit is in effect and kept, so setting it again changes nothing")
	}
	// A runtime request only cares about the live value.
	again := limit("1G")
	again.Runtime = true
	if res, _ := SetUnitProperties.Call(ctx, c, again, CallOptions{}); res.Changed {
		t.Error("a runtime request for the live value changes nothing")
	}
}

// Resource limits never reach Racksail's own units or the root system
// services: a persistent set-property is kept as a drop-in, so it is held to
// what drop-ins may adjust.
func TestSetUnitPropertiesRefusesUnitsThePanelCantAdjust(t *testing.T) {
	ctx := context.Background()
	_, c := newLocal(t)
	tiny := []system.UnitProperty{{Name: "TasksMax", Value: "1"}}
	for _, unit := range []string{
		system.AgentUnit, system.PanelUnit, system.PanelSocketUnit, system.EdgeUnit, system.EdgeSocketUnit,
		system.PlatformSlice, system.AccountsSlice, "racksail-addon@site.service",
		"nftables.service", "systemd-networkd.service", "systemd-timesyncd.service",
		"racksail-.slice", "racksail-accounts-.slice",
	} {
		for _, runtime := range []bool{false, true} {
			in := SetUnitPropertiesInput{Name: unit, Runtime: runtime, Properties: tiny}
			if _, err := SetUnitProperties.Call(ctx, c, in, CallOptions{Check: true}); !errors.Is(err, ErrInvalidInput) {
				t.Errorf("limits for %s (runtime %v) should be refused: %v", unit, runtime, err)
			}
		}
	}
	newAccount(t, c, "blog", 20001)
	slice, _ := system.AccountSliceSpec("blog", system.ResourceLimits{}).Render()
	if _, err := EnsureUnit.Call(ctx, c, EnsureUnitInput{Name: system.AccountSlice("blog"), Content: slice}, CallOptions{}); err != nil {
		t.Fatal(err)
	}
	in := SetUnitPropertiesInput{Name: system.AccountSlice("blog"), Properties: tiny}
	if res, err := SetUnitProperties.Call(ctx, c, in, CallOptions{Check: true}); err != nil || !res.Changed {
		t.Errorf("limits for an account's slice = %+v, %v", res, err)
	}
}

// Which home rule holds for an existing user comes from the user on the
// machine, not from the request: an account can't be moved into the panel's
// state by calling it a system user, a racksail-* service user can't be
// homed in the accounts area by calling it an account, and an account's home
// stays in its own directory.
func TestEnsureUserTakesItsRulesFromTheUser(t *testing.T) {
	ctx := context.Background()
	m, c := newLocal(t)
	b := m.Backend()
	newAccount(t, c, "blog", 20001)
	newAccount(t, c, "shop", 20002)
	for _, in := range []EnsureUserInput{
		{Name: "blog", System: true, Home: system.StateDir + "/x"},
		{Name: "blog", System: true, Home: "/nonexistent"},
		// Even a request that would change nothing else.
		{Name: "blog", System: true, Home: system.AccountDir("blog")},
	} {
		if _, err := EnsureUser.Call(ctx, c, in, CallOptions{}); !errors.Is(err, ErrConflict) {
			t.Errorf("EnsureUser(%+v) should be refused: %v", in, err)
		}
	}
	// A system user can't have an account's UID in the first place.
	if _, err := EnsureUser.Call(ctx, c, EnsureUserInput{Name: "blog", UID: 20001, System: true, Home: system.AccountDir("blog")}, CallOptions{}); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("a system user with an account's uid: %v", err)
	}
	for _, home := range []string{system.AccountDir("shop"), system.AccountDir("shop") + "/blog", system.AccountsDir + "/blogx"} {
		in := EnsureUserInput{Name: "blog", Home: home}
		if _, err := EnsureUser.Call(ctx, c, in, CallOptions{}); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("homing blog at %s should be refused: %v", home, err)
		}
	}
	if u, _ := b.Users.LookupUser(ctx, "blog"); u.Home != system.AccountDir("blog") {
		t.Errorf("blog's home moved to %s", u.Home)
	}
	// A directory inside its own is still the account's.
	inside := EnsureUserInput{Name: "blog", Home: system.AccountDir("blog") + "/home"}
	if res, err := EnsureUser.Call(ctx, c, inside, CallOptions{}); err != nil || !res.Changed {
		t.Errorf("a home inside the account's own directory = %+v, %v", res, err)
	}

	if _, err := EnsureUser.Call(ctx, c, EnsureUserInput{Name: "racksail-mail", System: true, Home: "/nonexistent"}, CallOptions{}); err != nil {
		t.Fatal(err)
	}
	for _, in := range []EnsureUserInput{
		{Name: "racksail-mail", Home: system.AccountDir("racksail-mail")},
		{Name: "racksail-mail", Home: "/nonexistent", Shell: "/bin/bash"},
	} {
		if _, err := EnsureUser.Call(ctx, c, in, CallOptions{}); !errors.Is(err, ErrConflict) {
			t.Errorf("EnsureUser(%+v) should be refused: %v", in, err)
		}
	}
	if u, _ := b.Users.LookupUser(ctx, "racksail-mail"); u.Home != "/nonexistent" || u.Shell != "/usr/sbin/nologin" {
		t.Errorf("racksail-mail was changed: %+v", u)
	}
}

// No account takes the name of a privileged group or of a user or group the
// system or its packages use: the account's own group would carry that name,
// and a package installed later would adopt it with the account in it.
func TestEnsureUserRefusesSystemNames(t *testing.T) {
	ctx := context.Background()
	m, c := newLocal(t)
	b := m.Backend()
	for i, name := range []string{"docker", "incus-admin", "lxd", "libvirt", "kvm", "sudo", "ssl-cert", "postgres", "www-data", "racksail-shady"} {
		uid := 20050 + i
		for _, check := range []bool{true, false} {
			in := EnsureUserInput{Name: name, UID: uid, Home: system.AccountDir(name)}
			if _, err := EnsureUser.Call(ctx, c, in, CallOptions{Check: check}); !errors.Is(err, ErrInvalidInput) {
				t.Errorf("an account named %s (check %v) should be refused: %v", name, check, err)
			}
		}
		// Some of these the server already has, such as sudo; none was made
		// for the account.
		if g, err := b.Users.LookupGroup(ctx, name); err == nil && g.GID == uid {
			t.Errorf("a group %s was made: %+v", name, g)
		}
		if name == "racksail-shady" {
			continue
		}
		if !ReservedName(name) {
			t.Errorf("ReservedName(%s) = false", name)
		}
		if _, err := EnsureGroup.Call(ctx, c, EnsureGroupInput{Name: name, GID: uid}, CallOptions{}); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("an account group named %s should be refused: %v", name, err)
		}
	}
	if ReservedName("blog") {
		t.Error("blog is free for an account")
	}
	// The installer still makes the platform's own users, and a no-op on an
	// existing system user stays one.
	if res, err := EnsureUser.Call(ctx, c, EnsureUserInput{Name: "racksail", System: true, Home: system.StateDir, Comment: "Racksail panel", Locked: true}, CallOptions{}); err != nil || res.Changed {
		t.Errorf("the panel's user as installed = %+v, %v", res, err)
	}
}

// Subordinate IDs are the account's own range (decision D3), worked out from
// its UID, so no account can be given another one's or anyone else's.
func TestSubordinateIDsAreTheAccountsOwn(t *testing.T) {
	ctx := context.Background()
	m, c := newLocal(t)
	b := m.Backend()
	own, other := system.AccountSubIDs(20001), system.AccountSubIDs(20002)
	for _, in := range []EnsureUserInput{
		{Name: "blog", UID: 20001, Home: system.AccountDir("blog"), SubIDs: &other},
		{Name: "blog", UID: 20001, Home: system.AccountDir("blog"), SubIDs: &system.IDRange{Start: own.Start, Count: own.Count + 1}},
		{Name: "racksail-pods", System: true, Home: "/nonexistent", SubIDs: &own},
	} {
		if _, err := EnsureUser.Call(ctx, c, in, CallOptions{Check: true}); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("EnsureUser(%+v) should be refused: %v", in, err)
		}
	}
	// A create whose UID is still to be chosen can't name its range yet.
	if _, err := EnsureUser.Call(ctx, c, EnsureUserInput{Name: "blog", Home: system.AccountDir("blog"), SubIDs: &own}, CallOptions{Check: true}); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("subordinate ids for an account without a uid: %v", err)
	}
	acct := EnsureUserInput{Name: "blog", UID: 20001, Home: system.AccountDir("blog"), SubIDs: &own}
	if _, err := EnsureUser.Call(ctx, c, acct, CallOptions{}); err != nil {
		t.Fatal(err)
	}
	// An update that leaves the UID out is held to the user's own range.
	if _, err := EnsureUser.Call(ctx, c, EnsureUserInput{Name: "blog", Home: system.AccountDir("blog"), SubIDs: &other}, CallOptions{}); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("another account's range for blog: %v", err)
	}
	if u, _ := b.Users.LookupUser(ctx, "blog"); u.SubIDs == nil || *u.SubIDs != own {
		t.Errorf("blog's subordinate ids = %+v", u.SubIDs)
	}
	if res, err := EnsureUser.Call(ctx, c, EnsureUserInput{Name: "blog", Home: system.AccountDir("blog"), SubIDs: &own}, CallOptions{}); err != nil || res.Changed {
		t.Errorf("blog's own range again = %+v, %v", res, err)
	}
}

// Racksail's own units are written only for callers in the agent's process,
// the installer and the self-update code: the agent's socket refuses every
// local operation, and records the refusal.
func TestLocalOperationsAreRefusedOnTheSocket(t *testing.T) {
	ctx := context.Background()
	m := sim.New(sim.Options{Hostname: "local-only"})
	srv := NewServer(m.Backend(), "test", nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	client := NewHTTPClient(ts.URL, ts.Client())

	if !InstallPlatformUnits.Local() {
		t.Fatal("InstallPlatformUnits must be local")
	}
	in := PlatformUnitsInput{Binary: "/usr/bin/racksail"}
	for _, check := range []bool{true, false} {
		if _, err := InstallPlatformUnits.Call(ctx, client, in, CallOptions{Check: check}); !errors.Is(err, ErrNotAllowed) {
			t.Errorf("InstallPlatformUnits on the socket (check %v): %v", check, err)
		}
	}
	for _, e := range Operations() {
		if !e.Local() {
			continue
		}
		if _, err := client.Call(ctx, e.Name(), json.RawMessage(`{}`), CallOptions{Check: true}); !errors.Is(err, ErrNotAllowed) {
			t.Errorf("%s on the socket: %v", e.Name(), err)
		}
	}
	records, _ := srv.Runtime().Audit().Recent(AuditFilter{Op: InstallPlatformUnits.Name()})
	if len(records) == 0 || records[0].Code != string(ErrNotAllowed.Code) {
		t.Errorf("the refusals aren't in the audit trail: %+v", records)
	}
	if _, err := m.Backend().Units.ReadUnit(ctx, system.AgentUnit); !errors.Is(err, system.ErrNotFound) {
		t.Errorf("the agent's unit was written: %v", err)
	}
	// In the agent's own process the same call goes through.
	if res, err := InstallPlatformUnits.Call(ctx, NewLocalRuntime(srv.Runtime()), in, CallOptions{}); err != nil || !res.Changed {
		t.Errorf("InstallPlatformUnits in process = %+v, %v", res, err)
	}
}

// A unit written for the panel only runs as a racksail-* user when that user
// really is one of Racksail's system users, since the unit checks spare such
// users the account sandbox.
func TestRacksailNamedUsersMustBeServiceUsers(t *testing.T) {
	ctx := context.Background()
	m, c := newLocal(t)
	b := m.Backend()
	if err := b.Users.CreateGroup(ctx, system.Group{Name: "racksail-shady", GID: 20060}); err != nil {
		t.Fatal(err)
	}
	if err := b.Users.CreateUser(ctx, system.User{Name: "racksail-shady", UID: 20060, GID: 20060, Home: system.AccountDir("racksail-shady")}); err != nil {
		t.Fatal(err)
	}
	unit := EnsureUnitInput{Name: "racksail-shady.service", Content: []byte("[Service]\nUser=racksail-shady\nExecStart=/usr/bin/true\n")}
	if _, err := EnsureUnit.Call(ctx, c, unit, CallOptions{}); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("an unsandboxed service as an account named racksail-*: %v", err)
	}
	if _, err := EnsureUser.Call(ctx, c, EnsureUserInput{Name: "racksail-web", System: true, Home: "/nonexistent"}, CallOptions{}); err != nil {
		t.Fatal(err)
	}
	unit = EnsureUnitInput{Name: "racksail-web.service", Content: []byte("[Service]\nUser=racksail-web\nExecStart=/usr/bin/true\n")}
	if _, err := EnsureUnit.Call(ctx, c, unit, CallOptions{}); err != nil {
		t.Errorf("a service as one of Racksail's service users: %v", err)
	}
}

// Every server runs an agent, and nothing but the agent could start it
// again, so it can't be kept from starting at boot; the edge, which the
// panel turns on and off with its sites, can.
func TestTheAgentStaysEnabled(t *testing.T) {
	ctx := context.Background()
	m, c := newLocal(t)
	if _, err := DisableUnits.Call(ctx, c, UnitsInput{Names: []string{system.AgentUnit}}, CallOptions{Check: true}); !errors.Is(err, ErrNotAllowed) {
		t.Errorf("disabling the agent: %v", err)
	}
	if _, err := DisableUnits.Call(ctx, c, UnitsInput{Names: []string{system.EdgeSocketUnit, system.AgentUnit}}, CallOptions{}); !errors.Is(err, ErrNotAllowed) {
		t.Errorf("disabling the agent with another unit: %v", err)
	}
	if st, _ := m.Backend().Units.Status(ctx, system.EdgeSocketUnit); st.UnitFileState != "enabled" {
		t.Errorf("a refused call changed %s: %+v", system.EdgeSocketUnit, st)
	}
	edge := UnitsInput{Names: []string{system.EdgeUnit}}
	if _, err := EnableUnits.Call(ctx, c, edge, CallOptions{}); err != nil {
		t.Fatal(err)
	}
	if res, err := DisableUnits.Call(ctx, c, edge, CallOptions{}); err != nil || !res.Changed {
		t.Errorf("disabling the edge = %+v, %v", res, err)
	}
}

// A Btrfs pool needs btrfs-progs, which Debian 13 only installs with a
// system on Btrfs: formatting one installs it, and so does the first mount
// of a Btrfs disk brought from elsewhere.
func TestBtrfsToolsComeWithTheFirstBtrfsDisk(t *testing.T) {
	ctx := context.Background()
	m, c := newLocal(t)
	b := m.Backend()
	removeBtrfsProgs := func() {
		t.Helper()
		if err := b.Packages.Remove(ctx, false, "btrfs-progs"); err != nil {
			t.Fatal(err)
		}
	}
	removeBtrfsProgs()
	format := FormatInput{Spec: system.FormatSpec{Type: "btrfs", Devices: []string{"/dev/sdb"}}}
	if res, err := FormatDevice.Call(ctx, c, format, CallOptions{Check: true}); err != nil || !installs(res.Effects, "btrfs-progs") {
		t.Errorf("btrfs format preview = %+v, %v", res, err)
	}
	if _, err := FormatDevice.Call(ctx, c, format, CallOptions{}); err != nil {
		t.Fatal(err)
	}
	if ins, _ := b.Packages.Installed(ctx, "btrfs-progs"); len(ins) == 0 {
		t.Error("formatting Btrfs didn't install btrfs-progs")
	}

	removeBtrfsProgs()
	if _, err := EnsureDir.Call(ctx, c, EnsureDirInput{Path: "/mnt/racksail/pool", Mode: 0o755}, CallOptions{}); err != nil {
		t.Fatal(err)
	}
	mount := MountInput{Mount: system.Mount{Source: "/dev/disk/by-id/ata-SIM_HDD_4TB_H0002", Target: "/mnt/racksail/pool", FSType: "btrfs"}}
	if res, err := EnsureMounted.Call(ctx, c, mount, CallOptions{Check: true}); err != nil || !installs(res.Effects, "btrfs-progs") {
		t.Errorf("btrfs mount preview = %+v, %v", res, err)
	}
	if _, err := EnsureMounted.Call(ctx, c, mount, CallOptions{}); err != nil {
		t.Fatal(err)
	}
	if ins, _ := b.Packages.Installed(ctx, "btrfs-progs"); len(ins) == 0 {
		t.Error("mounting a Btrfs disk didn't install btrfs-progs")
	}
	// Once mounted, mounting again neither installs nor changes anything.
	if res, err := EnsureMounted.Call(ctx, c, mount, CallOptions{}); err != nil || res.Changed {
		t.Errorf("mounting again = %+v, %v", res, err)
	}
}

// DeleteUser removes a home with its user only where a managed user's home
// may be, so one that points at another account's directory, as an older
// release allowed, or at the panel's state doesn't take that with it.
func TestDeleteUserRemovesOnlyItsOwnHome(t *testing.T) {
	ctx := context.Background()
	m, c := newLocal(t)
	b := m.Backend()
	newAccount(t, c, "shop", 20002)
	if _, err := EnsureDir.Call(ctx, c, EnsureDirInput{Path: system.AccountDir("shop"), Mode: 0o750}, CallOptions{}); err != nil {
		t.Fatal(err)
	}
	for _, u := range []system.User{
		{Name: "blog", UID: 20001, Home: system.AccountDir("shop")},
		{Name: "racksail-old", UID: 950, System: true, Home: system.StateDir},
	} {
		if err := b.Users.CreateUser(ctx, u); err != nil {
			t.Fatal(err)
		}
		if _, err := DeleteUser.Call(ctx, c, DeleteUserInput{Name: u.Name, RemoveHome: true}, CallOptions{}); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("deleting %s with its home %s: %v", u.Name, u.Home, err)
		}
	}
	if _, err := b.Files.Stat(ctx, system.AccountDir("shop")); err != nil {
		t.Errorf("shop's directory went: %v", err)
	}
	if _, err := b.Files.Stat(ctx, system.StateDir); err != nil {
		t.Errorf("the panel's state went: %v", err)
	}
	// Without its home the user goes, and an account's own home goes with it.
	if res, err := DeleteUser.Call(ctx, c, DeleteUserInput{Name: "blog"}, CallOptions{}); err != nil || !res.Changed {
		t.Errorf("deleting blog alone = %+v, %v", res, err)
	}
	if res, err := DeleteUser.Call(ctx, c, DeleteUserInput{Name: "shop", RemoveHome: true}, CallOptions{}); err != nil || !res.Changed {
		t.Errorf("deleting shop with its own home = %+v, %v", res, err)
	}
	if _, err := b.Files.Stat(ctx, system.AccountDir("shop")); !errors.Is(err, system.ErrNotFound) {
		t.Errorf("shop's own home stayed: %v", err)
	}
}
