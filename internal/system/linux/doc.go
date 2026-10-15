// Package linux implements the system adapters for a real Debian 13 server.
//
// Adapters read the machine's state from files (/etc/passwd, /proc,
// /sys/class/net and so on), talk to systemd over D-Bus, and run the
// standard Debian tools (useradd, apt-get, btrfs, nft, lsblk, smartctl and
// others) with argument vectors, never through a shell.
//
// Every adapter takes its dependencies from Options: the filesystem root, a
// Runner for external programs, a systemd connection and an HTTP client.
// New wires the real ones and only builds on Linux; tests build adapters
// with NewWith, a temporary root and fakes, so the parsing and the argument
// vectors are checked on any operating system.
package linux
