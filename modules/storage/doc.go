// Package storage manages disks and pools: disk inventory with SMART health
// and alerts, burn-in tests and guided replacement, Btrfs, mdadm and ZFS
// pools, SnapRAID parity arrays pooled with mergerfs, cache tiering, scrubs
// and rebalances with capacity forecasts, snapshots and quotas, spin-down, and
// read-only imports of existing arrays.
//
// Its screens live in ui/src/modules/storage and its documentation in
// docs/site/src/content/docs/modules/storage.md.
package storage
