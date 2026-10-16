package agent

import (
	"context"
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

// An account's home has to stay inside the accounts area, so it can't point
// at a location the panel itself writes.
func TestEnsureUserKeepsAccountHomesInTheAccountsArea(t *testing.T) {
	ctx := context.Background()
	_, c := newLocal(t)
	for _, home := range []string{"/etc/racksail/generated/blog", "/opt/racksail/runtimes/blog", "/var/lib/racksail/blog"} {
		in := EnsureUserInput{Name: "blog", UID: 20002, GID: 20002, Home: home}
		if _, err := EnsureUser.Call(ctx, c, in, CallOptions{}); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("home %s should be refused: %v", home, err)
		}
	}
	if _, err := EnsureUser.Call(ctx, c, EnsureUserInput{Name: "blog", UID: 20002, GID: 20002, Home: system.AccountDir("blog")}, CallOptions{Check: true}); err != nil {
		t.Errorf("a home in the accounts area: %v", err)
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
	// A path no handler serves.
	resp, err = http.Post(ts.URL+"/v1/nonsense", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	records, _ := srv.Runtime().Audit().Recent(AuditFilter{})
	want := map[string]bool{"streams/Download": false, "streams/Upload": false, "/v1/nonsense": false}
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
