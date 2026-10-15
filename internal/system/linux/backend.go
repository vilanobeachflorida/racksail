package linux

import (
	"context"
	"io"
	"net/http"
	"path/filepath"

	"github.com/vilanobeachflorida/racksail/internal/system"
)

// Runner runs external programs. Run is system.Exec; Stream connects the
// program's standard input and output to readers and writers, for commands
// that move large amounts of data such as btrfs send.
type Runner interface {
	system.Exec
	Stream(ctx context.Context, cmd system.Command, stdin io.Reader, stdout io.Writer) error
}

// Options are the dependencies of the Linux adapters.
type Options struct {
	// Root is where the machine's filesystem is; "/" on a real server.
	Root string
	// Runner runs external programs.
	Runner Runner
	// Systemd controls units.
	Systemd Systemd
	// HTTP downloads vendor archives.
	HTTP *http.Client
}

// Program paths. They are fixed: the agent never looks programs up in PATH.
const (
	progUseradd     = "/usr/sbin/useradd"
	progUsermod     = "/usr/sbin/usermod"
	progUserdel     = "/usr/sbin/userdel"
	progGroupadd    = "/usr/sbin/groupadd"
	progGroupdel    = "/usr/sbin/groupdel"
	progGpasswd     = "/usr/bin/gpasswd"
	progAptGet      = "/usr/bin/apt-get"
	progAptCache    = "/usr/bin/apt-cache"
	progDpkgQuery   = "/usr/bin/dpkg-query"
	progTar         = "/usr/bin/tar"
	progBtrfs       = "/usr/bin/btrfs"
	progMount       = "/usr/bin/mount"
	progUmount      = "/usr/bin/umount"
	progLsblk       = "/usr/bin/lsblk"
	progSmartctl    = "/usr/sbin/smartctl"
	progWipefs      = "/usr/sbin/wipefs"
	progSgdisk      = "/usr/sbin/sgdisk"
	progNft         = "/usr/sbin/nft"
	progNetworkctl  = "/usr/bin/networkctl"
	progHostnamectl = "/usr/bin/hostnamectl"
	progHostname    = "/usr/bin/hostname"
	progDetectVirt  = "/usr/bin/systemd-detect-virt"
	progChattr      = "/usr/bin/chattr"
	progUdevadm     = "/usr/bin/udevadm"
	progMkfsBtrfs   = "/usr/sbin/mkfs.btrfs"
	progMkfsExt4    = "/usr/sbin/mkfs.ext4"
	progMkfsXfs     = "/usr/sbin/mkfs.xfs"
	progMkfsVfat    = "/usr/sbin/mkfs.vfat"
	progMkswap      = "/usr/sbin/mkswap"
	progSystemctl   = "/usr/bin/systemctl"
	progIP          = "/usr/bin/ip"
)

// NewWith builds a backend from explicit dependencies.
func NewWith(opts Options) *system.Backend {
	if opts.Root == "" {
		opts.Root = "/"
	}
	if opts.HTTP == nil {
		opts.HTTP = http.DefaultClient
	}
	h := &host{root: opts.Root, run: opts.Runner, http: opts.HTTP}
	files := &files{h}
	return &system.Backend{
		Name:      "linux",
		Facts:     &facts{h},
		Users:     &users{h},
		Files:     files,
		Units:     &units{h: h, systemd: opts.Systemd},
		Packages:  &packages{h},
		Btrfs:     &btrfs{h},
		Mounts:    &mounts{h},
		Disks:     &disks{h},
		Firewall:  &firewall{h},
		Network:   &network{h},
		Sysctl:    &sysctl{h},
		Processes: &processes{h},
		Certs:     system.NewCertStore(files, system.CertsDir),
		Exec:      opts.Runner,
	}
}

// host is what every adapter shares.
type host struct {
	root string
	run  Runner
	http *http.Client
}

// path maps an absolute machine path into the root.
func (h *host) path(p string) string {
	return filepath.Join(h.root, filepath.FromSlash(filepath.Clean("/"+p)))
}

// exec runs a program and returns its standard output.
func (h *host) exec(ctx context.Context, prog string, args ...string) ([]byte, error) {
	out, err := h.run.Run(ctx, system.Command{Path: prog, Args: args})
	return out.Stdout, err
}

// execInput runs a program with data on its standard input.
func (h *host) execInput(ctx context.Context, stdin []byte, prog string, args ...string) ([]byte, error) {
	out, err := h.run.Run(ctx, system.Command{Path: prog, Args: args, Stdin: stdin})
	return out.Stdout, err
}
