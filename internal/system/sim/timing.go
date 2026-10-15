package sim

import (
	"context"
	"time"
)

// Operations whose duration a realistic machine simulates.
const (
	opInstall     = "packages.install"
	opUpdateIndex = "packages.update_index"
	opRemove      = "packages.remove"
	opUpgrade     = "packages.upgrade"
	opArchive     = "packages.install_archive"
	opStart       = "units.start"
	opStop        = "units.stop"
	opRestart     = "units.restart"
	opReload      = "units.reload"
	opDaemon      = "units.daemon_reload"
	opSnapshot    = "btrfs.snapshot"
	opSubvolume   = "btrfs.create_subvolume"
	opFormat      = "disks.format"
	opPartition   = "disks.partition"
	opWipe        = "disks.wipe"
	opFirewall    = "firewall.apply"
	opNetwork     = "network.apply_config"
	opCreateUser  = "users.create_user"
)

// timings are about how long the operations take on a small real server.
var timings = map[string]time.Duration{
	opInstall:     600 * time.Millisecond,
	opUpdateIndex: 1500 * time.Millisecond,
	opRemove:      500 * time.Millisecond,
	opUpgrade:     3 * time.Second,
	opArchive:     500 * time.Millisecond,
	opStart:       150 * time.Millisecond,
	opStop:        100 * time.Millisecond,
	opRestart:     250 * time.Millisecond,
	opReload:      60 * time.Millisecond,
	opDaemon:      120 * time.Millisecond,
	opSnapshot:    40 * time.Millisecond,
	opSubvolume:   25 * time.Millisecond,
	opFormat:      1500 * time.Millisecond,
	opPartition:   400 * time.Millisecond,
	opWipe:        300 * time.Millisecond,
	opFirewall:    80 * time.Millisecond,
	opNetwork:     600 * time.Millisecond,
	opCreateUser:  40 * time.Millisecond,
}

// maxDelay keeps the slowest simulated operations bearable in development.
const maxDelay = 20 * time.Second

// delay waits as long as op takes on a real server, plus extra, when the
// machine is realistic. It is called before taking the machine's lock, so
// other operations go on meanwhile, as they would on a real server.
func (m *Machine) delay(ctx context.Context, op string, extra time.Duration) {
	if !m.realistic {
		return
	}
	d := min(timings[op]+extra, maxDelay)
	if d <= 0 {
		return
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
	case <-ctx.Done():
	}
}
