package updates

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/changeset"
	"github.com/vilanobeachflorida/racksail/internal/system"
)

// update changes the settings through the change pipeline.
func (s *server) update(t *testing.T, change func(spec *SettingsSpec)) {
	t.Helper()
	ctx := context.Background()
	set, err := s.updater.Settings(ctx)
	if err != nil || set == nil {
		t.Fatalf("settings: %v", err)
	}
	spec := set.Spec
	change(&spec)
	raw, _ := json.Marshal(spec)
	cs, err := s.engine.Submit(ctx, changeset.Request{Actor: s.admin, Changes: []changeset.Change{{Op: changeset.OpUpdate, Kind: Kind.Name(), ID: set.ID, Spec: raw, Revision: set.Revision}}})
	if err != nil {
		t.Fatal(err)
	}
	wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if _, err := s.jobs.Wait(wctx, cs.Job); err != nil {
		t.Fatal(err)
	}
}

func TestSecurityUpdatesFromTheSettings(t *testing.T) {
	ctx := context.Background()
	s := newServer(t, newGitHub(t), "1.2.0", nil)
	set, _ := s.updater.Settings(ctx)
	if p := set.Spec.Packages; p == nil || !p.Security || p.Scheduled || p.Reboot {
		t.Fatalf("new settings take security updates only: %+v", p)
	}
	waitFor(t, "unattended-upgrades is set up", func() bool {
		data, err := s.machine.Backend().Files.ReadFile(ctx, system.AutoUpgradesFile, 1<<20)
		return err == nil && strings.Contains(string(data), `APT::Periodic::Unattended-Upgrade "1";`)
	})
	if installed, _ := s.machine.Backend().Packages.Installed(ctx, system.UnattendedPackage); len(installed) == 0 {
		t.Error("unattended-upgrades is installed")
	}
	conf := s.read(t, system.UnattendedConfigFile)
	if !strings.Contains(conf, "Debian-Security") || !strings.Contains(conf, `Automatic-Reboot "false"`) {
		t.Errorf("only security updates, without reboots: %s", conf)
	}

	s.update(t, func(spec *SettingsSpec) {
		spec.Packages = &PackageUpdates{Security: true, Reboot: true, RebootTime: "02:30"}
	})
	conf = s.read(t, system.UnattendedConfigFile)
	if !strings.Contains(conf, `Automatic-Reboot "true"`) || !strings.Contains(conf, `Automatic-Reboot-Time "02:30"`) {
		t.Errorf("reboots at the reboot time: %s", conf)
	}
	s.update(t, func(spec *SettingsSpec) { spec.Packages = &PackageUpdates{} })
	if got := s.read(t, system.AutoUpgradesFile); !strings.Contains(got, `APT::Periodic::Unattended-Upgrade "0";`) {
		t.Errorf("security updates can be turned off: %s", got)
	}
	// Settings left empty take security updates.
	s.update(t, func(spec *SettingsSpec) { spec.Packages = nil })
	if got := s.read(t, system.AutoUpgradesFile); !strings.Contains(got, `APT::Periodic::Unattended-Upgrade "1";`) {
		t.Errorf("empty settings take security updates: %s", got)
	}
}

func TestUpgradePackages(t *testing.T) {
	ctx := context.Background()
	s := newServer(t, newGitHub(t), "1.2.0", nil)
	s.update(t, func(spec *SettingsSpec) {
		spec.Packages = &PackageUpdates{Security: true, Reboot: true, RebootTime: "04:00"}
	})
	if err := s.machine.Backend().Packages.Install(ctx, system.PackageRequest{Name: "openssh-server"}); err != nil {
		t.Fatal(err)
	}
	s.machine.OfferUpgrade("openssh-server", "1:10.0p1-8")

	// The preview says what will happen, without doing it.
	set, _ := s.updater.Settings(ctx)
	cs, err := s.engine.Submit(ctx, changeset.Request{Actor: s.admin, DryRun: true, Changes: []changeset.Change{{Op: changeset.OpAction, Kind: Kind.Name(), ID: set.ID, Action: "upgrade_packages"}}})
	if err != nil {
		t.Fatal(err)
	}
	var snapshots, upgrades int
	for _, e := range cs.Effects {
		switch {
		case e.Type == system.EffectStorage && e.Action == "snapshot":
			snapshots++
		case e.Type == system.EffectPackage && e.Action == "upgrade":
			upgrades++
		}
	}
	if snapshots == 0 || upgrades != 1 {
		t.Errorf("the preview shows snapshots and the upgrade: %s", mustJSON(cs.Effects))
	}

	// An update asks for a reboot, which is scheduled for the reboot time.
	files := s.machine.Backend().Files
	if err := mkdirAll(ctx, files, "/run"); err != nil {
		t.Fatal(err)
	}
	if err := files.WriteFile(ctx, "/run/reboot-required", nil, 0o644, system.Owner{}); err != nil {
		t.Fatal(err)
	}
	cs, err = s.action(t, "upgrade_packages", nil)
	if err != nil || cs.State != changeset.StateApplied {
		t.Fatalf("upgrade: %v %s", err, mustJSON(cs))
	}
	set, _ = s.updater.Settings(ctx)
	got := set.Status.Packages
	if got == nil || got.Upgraded != 1 || got.Error != "" || len(got.Snapshots) == 0 {
		t.Fatalf("the status says what was upgraded: %+v", got)
	}
	if v, _ := s.machine.Backend().Packages.Installed(ctx, "openssh-server"); len(v) == 0 || v[0].Version != "1:10.0p1-8" {
		t.Errorf("the package was upgraded: %+v", v)
	}
	os := set.Status.OS
	if os == nil || !os.RebootRequired || os.RebootAt == nil || os.RebootAt.Hour() != 4 || os.RebootAt.Minute() != 0 {
		t.Fatalf("a reboot is scheduled for 04:00: %+v", os)
	}
	if _, err := s.machine.Backend().Units.ReadUnit(ctx, "racksail-reboot.timer"); err != nil {
		t.Errorf("the reboot timer is in place: %v", err)
	}
	types := s.eventTypes()
	for _, want := range []string{EventPackagesUpgraded.Name, EventRebootScheduled.Name} {
		if !slices.Contains(types, want) {
			t.Errorf("%s is published: %v", want, types)
		}
	}

	// Nothing else starts while Racksail restarts onto a new release.
	s.updater.mu.Lock()
	s.updater.installed = time.Now()
	s.updater.mu.Unlock()
	if _, err := s.updater.UpgradePackages(ctx); err == nil || !strings.Contains(err.Error(), "restarting") {
		t.Errorf("an upgrade waits for the restart: %v", err)
	}
}

func TestScheduledPackageUpgrades(t *testing.T) {
	ctx := context.Background()
	var mu sync.Mutex
	now := time.Date(2026, 10, 16, 1, 0, 0, 0, time.UTC)
	clock := func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return now
	}
	set := func(t time.Time) {
		mu.Lock()
		now = t
		mu.Unlock()
	}
	s := newServer(t, newGitHub(t), "1.2.0", func(o *Options) { o.Now = clock })
	if err := s.machine.Backend().Packages.Install(ctx, system.PackageRequest{Name: "openssh-server"}); err != nil {
		t.Fatal(err)
	}
	s.machine.OfferUpgrade("openssh-server", "1:10.0p1-8")
	ran := func() *PackagesStatus {
		st, _ := s.updater.Settings(ctx)
		return st.Status.Packages
	}

	// Off by default.
	s.updater.scheduled(ctx)
	time.Sleep(50 * time.Millisecond)
	if ran() != nil {
		t.Fatal("scheduled upgrades are off unless asked for")
	}
	s.update(t, func(spec *SettingsSpec) {
		spec.Window = &Window{Start: "03:00", End: "05:00"}
		spec.Packages = &PackageUpdates{Security: true, Scheduled: true}
	})
	// Outside the window nothing happens.
	s.updater.scheduled(ctx)
	time.Sleep(50 * time.Millisecond)
	if ran() != nil {
		t.Fatal("scheduled upgrades wait for the window")
	}
	set(time.Date(2026, 10, 16, 3, 10, 0, 0, time.UTC))
	s.updater.scheduled(ctx)
	waitFor(t, "the scheduled upgrade ran", func() bool { p := ran(); return p != nil && p.Upgraded == 1 })
	first := ran().At

	// Once a day: an hour later in the same window, nothing runs again.
	set(time.Date(2026, 10, 16, 4, 10, 0, 0, time.UTC))
	s.updater.scheduled(ctx)
	time.Sleep(50 * time.Millisecond)
	if !ran().At.Equal(first) {
		t.Error("packages are upgraded once a day")
	}
	set(time.Date(2026, 10, 17, 3, 5, 0, 0, time.UTC))
	s.updater.scheduled(ctx)
	waitFor(t, "the next day's upgrade ran", func() bool { return ran().At.After(first) })
}

func TestNextAt(t *testing.T) {
	now := time.Date(2026, 10, 16, 3, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		hhmm string
		want time.Time
	}{
		{"04:00", time.Date(2026, 10, 16, 4, 0, 0, 0, time.UTC)},
		{"02:00", time.Date(2026, 10, 17, 2, 0, 0, 0, time.UTC)},
		// A time too close to now waits for the next day.
		{"03:00", time.Date(2026, 10, 17, 3, 0, 0, 0, time.UTC)},
		{"nonsense", time.Date(2026, 10, 16, 4, 0, 0, 0, time.UTC)},
	} {
		if got := nextAt(now, c.hhmm); !got.Equal(c.want) {
			t.Errorf("nextAt(%s) = %s, want %s", c.hhmm, got, c.want)
		}
	}
}
