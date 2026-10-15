package agent

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/system"
	"github.com/vilanobeachflorida/racksail/internal/system/sim"
)

func TestUpgradesSnapshotFirstAndSkipHeldPackages(t *testing.T) {
	ctx := context.Background()
	m, c := newLocal(t)
	b := m.Backend()
	facts, err := b.Facts.Facts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	m.OfferUpgrade("sudo", "1.9.17-1")
	m.OfferUpgrade("linux-image-"+facts.Arch, "6.12.57-1")
	if _, err := UpdatePackageIndex.Call(ctx, c, Empty{}, CallOptions{}); err != nil {
		t.Fatal(err)
	}
	list, err := UpgradablePackages.Call(ctx, c, Empty{}, CallOptions{})
	if err != nil || len(list.Packages) == 0 {
		t.Fatalf("the simulated server should have updates: %+v, %v", list, err)
	}
	held := "sudo"
	if res, err := HoldPackages.Call(ctx, c, HoldInput{Names: []string{held}, Hold: true}, CallOptions{}); err != nil || !res.Changed {
		t.Fatalf("hold = %+v, %v", res, err)
	}
	if res, _ := HoldPackages.Call(ctx, c, HoldInput{Names: []string{held}, Hold: true}, CallOptions{}); res.Changed {
		t.Error("holding a held package changes nothing")
	}

	check, err := UpgradePackages.Call(ctx, c, UpgradeInput{Full: true, Snapshot: "upgrade_1"}, CallOptions{Check: true})
	if err != nil || !check.Changed {
		t.Fatalf("check = %+v, %v", check, err)
	}
	for _, e := range check.Effects {
		if e.Target == held {
			t.Errorf("a held package would be upgraded: %+v", e)
		}
	}
	if len(check.Snapshots) == 0 || check.Effects[0].Action != "snapshot" {
		t.Errorf("the check should list the snapshot first: %+v", check)
	}

	res, err := UpgradePackages.Call(ctx, c, UpgradeInput{Full: true, Snapshot: "upgrade_1"}, CallOptions{})
	if err != nil || !res.Changed {
		t.Fatalf("upgrade = %+v, %v", res, err)
	}
	for _, s := range res.Snapshots {
		if !strings.HasPrefix(s.Path, SnapshotDir+"/upgrade_1/") {
			t.Errorf("snapshot %s isn't under the label", s.Path)
		}
		if _, err := b.Files.Stat(ctx, s.Path); err != nil {
			t.Errorf("snapshot %s wasn't taken: %v", s.Path, err)
		}
	}
	after, _ := UpgradablePackages.Call(ctx, c, Empty{}, CallOptions{})
	if len(after.Packages) != 1 || after.Packages[0].Name != held {
		t.Errorf("only the held package should be left: %+v", after.Packages)
	}
	if _, err := UpgradePackages.Call(ctx, c, UpgradeInput{Snapshot: "Not A Label"}, CallOptions{}); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("a bad label: %v", err)
	}

	// The full upgrade brought a new kernel, so the server wants a reboot.
	status, err := RebootStatus.Call(ctx, c, Empty{}, CallOptions{})
	if err != nil || !status.Required || status.Newest == status.Running || len(status.Reasons) == 0 {
		t.Fatalf("reboot status = %+v, %v", status, err)
	}
	at := time.Now().Add(2 * time.Hour).Truncate(time.Second).UTC()
	if res, err := ScheduleReboot.Call(ctx, c, ScheduleRebootInput{At: at}, CallOptions{}); err != nil || !res.Changed {
		t.Fatalf("schedule = %+v, %v", res, err)
	}
	if res, _ := ScheduleReboot.Call(ctx, c, ScheduleRebootInput{At: at}, CallOptions{}); res.Changed {
		t.Error("scheduling the same reboot again changes nothing")
	}
	status, _ = RebootStatus.Call(ctx, c, Empty{}, CallOptions{})
	if status.Scheduled == nil || !status.Scheduled.Equal(at) {
		t.Errorf("scheduled = %v, want %v", status.Scheduled, at)
	}
	if _, err := ScheduleReboot.Call(ctx, c, ScheduleRebootInput{At: time.Now().Add(-time.Minute)}, CallOptions{}); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("a time in the past: %v", err)
	}
	if res, err := CancelReboot.Call(ctx, c, Empty{}, CallOptions{}); err != nil || !res.Changed {
		t.Errorf("cancel = %+v, %v", res, err)
	}
	if res, _ := CancelReboot.Call(ctx, c, Empty{}, CallOptions{}); res.Changed {
		t.Error("cancelling twice changes nothing")
	}
	if status, _ = RebootStatus.Call(ctx, c, Empty{}, CallOptions{}); status.Scheduled != nil {
		t.Errorf("still scheduled: %v", status.Scheduled)
	}

	// After the reboot the new kernel runs.
	m.Reboot()
	if status, _ = RebootStatus.Call(ctx, c, Empty{}, CallOptions{}); status.Required {
		t.Errorf("after rebooting: %+v", status)
	}
	if res, err := HoldPackages.Call(ctx, c, HoldInput{Names: []string{held}}, CallOptions{}); err != nil || !res.Changed {
		t.Errorf("unhold = %+v, %v", res, err)
	}
}

func TestKernelDefaults(t *testing.T) {
	ctx := context.Background()
	m, c := newLocal(t)
	b := m.Backend()
	in := KernelDefaultsInput{Zram: true, Overrides: map[string]string{"net.core.somaxconn": "8192"}}
	res, err := ApplyKernelDefaults.Call(ctx, c, in, CallOptions{})
	if err != nil || !res.Changed {
		t.Fatalf("apply = %+v, %v", res, err)
	}
	for k, want := range map[string]string{
		"net.ipv4.tcp_congestion_control": "bbr",
		"net.core.default_qdisc":          "fq",
		"net.core.somaxconn":              "8192",
	} {
		if got, err := b.Sysctl.Get(ctx, k); err != nil || got != want {
			t.Errorf("%s = %q, %v; want %q", k, got, err, want)
		}
	}
	for _, p := range []string{system.KernelSysctlFile, system.ModulesLoadFile, system.ZramGeneratorFile} {
		if _, err := b.Files.Stat(ctx, p); err != nil {
			t.Errorf("%s: %v", p, err)
		}
	}
	if st, _ := b.Units.Status(ctx, zramUnit); !st.Active() {
		t.Errorf("zram swap isn't on: %+v", st)
	}
	if pkgs, _ := b.Packages.Installed(ctx, system.ZramPackage); len(pkgs) != 1 {
		t.Errorf("the zram generator isn't installed")
	}
	if res, err := ApplyKernelDefaults.Call(ctx, c, in, CallOptions{}); err != nil || res.Changed {
		t.Errorf("applying again = %+v, %v", res, err)
	}

	// Turning zram off removes its configuration and stops it.
	res, err = ApplyKernelDefaults.Call(ctx, c, KernelDefaultsInput{}, CallOptions{})
	if err != nil || !res.Changed {
		t.Fatalf("without zram = %+v, %v", res, err)
	}
	if _, err := b.Files.Stat(ctx, system.ZramGeneratorFile); !errors.Is(err, system.ErrNotFound) {
		t.Errorf("the zram configuration should be gone: %v", err)
	}
	if st, _ := b.Units.Status(ctx, zramUnit); st.Active() {
		t.Error("zram swap should be off")
	}
	if got, _ := b.Sysctl.Get(ctx, "net.core.somaxconn"); got == "8192" {
		t.Error("the override should be gone with the input that set it")
	}

	for _, bad := range []map[string]string{{"kernel.modules_disabled": "1"}, {"net.core.somaxconn": "1\nkernel.x=1"}} {
		if _, err := ApplyKernelDefaults.Call(ctx, c, KernelDefaultsInput{Overrides: bad}, CallOptions{}); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("override %v: %v", bad, err)
		}
	}
}

func TestTimeSync(t *testing.T) {
	ctx := context.Background()
	m := sim.New(sim.Options{Hostname: "clock"})
	c := NewLocal(m.Backend(), nil)
	b := m.Backend()
	// A server installed with another time zone is moved to UTC.
	if _, err := b.Exec.Run(ctx, system.Command{Path: progTimedatectl, Args: []string{"set-timezone", "Europe/Lisbon"}}); err != nil {
		t.Fatal(err)
	}
	in := TimeSyncInput{Sync: system.TimeSync{Servers: []string{"ntp.example.net"}}}
	res, err := ConfigureTimeSync.Call(ctx, c, in, CallOptions{})
	if err != nil || !res.Changed {
		t.Fatalf("configure = %+v, %v", res, err)
	}
	facts, _ := b.Facts.Facts(ctx)
	if facts.Timezone != "UTC" {
		t.Errorf("time zone = %s", facts.Timezone)
	}
	conf, err := b.Files.ReadFile(ctx, system.TimesyncdDropIn, 0)
	if err != nil || !strings.Contains(string(conf), "ntp.example.net") {
		t.Errorf("timesyncd configuration = %q, %v", conf, err)
	}
	if st, _ := b.Units.Status(ctx, timesyncdUnit); !st.Active() || st.UnitFileState != "enabled" {
		t.Errorf("timesyncd = %+v", st)
	}
	if res, err := ConfigureTimeSync.Call(ctx, c, in, CallOptions{}); err != nil || res.Changed {
		t.Errorf("configuring again = %+v, %v", res, err)
	}
}

func TestAutomaticUpdates(t *testing.T) {
	ctx := context.Background()
	m, c := newLocal(t)
	b := m.Backend()
	in := ConfigureUpdatesInput{Updates: system.UnattendedUpgrades{Enabled: true, Hold: []string{"mariadb-*"}}}
	res, err := ConfigureUpdates.Call(ctx, c, in, CallOptions{})
	if err != nil || !res.Changed {
		t.Fatalf("configure = %+v, %v", res, err)
	}
	if pkgs, _ := b.Packages.Installed(ctx, system.UnattendedPackage); len(pkgs) != 1 {
		t.Error("unattended-upgrades isn't installed")
	}
	conf, err := b.Files.ReadFile(ctx, system.UnattendedConfigFile, 0)
	if err != nil || !strings.Contains(string(conf), "mariadb-") {
		t.Errorf("configuration = %q, %v", conf, err)
	}
	if res, _ := ConfigureUpdates.Call(ctx, c, in, CallOptions{}); res.Changed {
		t.Error("the same settings change nothing")
	}
	targets := []string{}
	for _, e := range res.Effects {
		targets = append(targets, e.Target)
	}
	if !slices.Contains(targets, system.AutoUpgradesFile) {
		t.Errorf("effects = %v", targets)
	}
}
