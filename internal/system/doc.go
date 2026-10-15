// Package system defines the adapters the agent uses to act on a machine.
//
// Each area of the machine has an interface: facts, users and groups, files,
// systemd units (services, slices, sockets and timers), packages and vendor
// archives, Btrfs, mounts, disks, the firewall, the network, sysctl,
// processes, certificates and running external programs. A Backend groups one
// implementation of every area.
//
// There are two backends. internal/system/linux talks to a real Debian 13
// server. internal/system/sim is a simulated server that keeps its state in
// memory, so every feature can be developed and tested on any machine without
// root.
//
// Adapters are thin: they do exactly what they are asked and report errors.
// Idempotency, argument validation and check mode live in the agent's typed
// operations, which are written once on top of these interfaces. Rendering
// (unit files, rulesets, network files, apt sources) is pure and lives with
// the code that owns the configuration, so it is covered by golden tests that
// run anywhere.
package system
