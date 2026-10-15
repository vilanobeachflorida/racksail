// Package sim is a simulated Debian 13 server that implements every system
// adapter in memory.
//
// A Machine starts as a freshly installed Racksail server: Btrfs subvolumes
// on the system disk, two empty NVMe and two empty hard disks, one network
// interface with documentation addresses, the base users, units and
// packages. Everything the agent does changes that state the way it would
// change a real server: writing a unit file makes it loadable, installing a
// package makes its services available, starting a service creates its main
// process, snapshots copy files, and so on. Development mode, demos and every
// simulated end-to-end test run against it.
//
// External programs are simulated by Programs registered for their path.
// Modules register programs for the tools they run (knotc, postmap, restic
// and so on) with RegisterProgram in an init function; running a program
// nobody simulates fails with ErrNotSimulated.
//
// Tests can inspect what happened with Calls and make the next operation on
// a target fail with FailNext.
package sim
