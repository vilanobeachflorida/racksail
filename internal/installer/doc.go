// Package installer turns a machine into a Racksail server.
//
// The same engine serves three ways in: the installer image's terminal UI,
// its browser mode protected by a one-time code, and an unattended answers
// file. All of them produce Answers, validated against
// spec/schemas/answers.schema.json, which say how to set up the network,
// which disks to use and who the admin is.
//
// Converting an existing Debian 13 install runs ConvertSteps through the
// agent: system users, directories, the configuration file and Racksail's
// own systemd units, rendered by Units. Installing onto empty disks also
// partitions them, creates the Btrfs subvolume layout, bootstraps Debian with
// mmdebstrap and installs the bootloader, from the installer image.
package installer
