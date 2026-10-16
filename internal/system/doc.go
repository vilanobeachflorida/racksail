// Package system defines the adapters the agent uses to act on a machine.
//
// Each area of the machine has an interface: facts, users and groups, files,
// systemd units (services, slices, sockets and timers), packages and vendor
// archives, Btrfs, mounts, disks, the firewall, the network, sysctl,
// processes, certificates, the journal, metrics and running external
// programs. A Backend groups one implementation of every area.
//
// There are two backends. internal/system/linux talks to a real Debian 13
// server. internal/system/sim is a simulated server, so every feature can be
// developed and tested on any machine without root.
//
// Adapters are thin: they do exactly what they are asked and report errors.
// Idempotency, argument validation and check mode live in the agent's typed
// operations, which are written once on top of these interfaces.
//
// Rendering is pure and lives here, covered by golden tests that run
// anywhere: unit files (ServiceSpec, SocketSpec, TimerSpec, SliceSpec, the
// Sandbox profiles, wake-on-request units and Racksail's own units),
// systemd-networkd files (NetworkConfig), nftables rulesets
// (FirewallPolicy), apt sources and pins (AptSource), kernel defaults, zram
// swap, unattended upgrades and time synchronization. Archives are packed
// and unpacked through a Tree, which can't be left by name or by link, so
// the same code is safe on both backends.
package system
