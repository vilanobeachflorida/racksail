// Command qemu is the end-to-end harness. It installs Racksail on a virtual
// machine the way people do and tests the result.
//
// With -iso it boots the installer image with an answers file on a USB
// stick, lets the unattended install run until the installer reboots, boots
// the installed system, waits for the panel, checks that Racksail's own
// services idle within the memory budget, and runs the API and browser
// suites against it. With -cloud-image it boots one of Debian's cloud images
// and sets it up the way people set up a server they already have: with
// -binary, a cloud-init seed converts it with racksail install --convert;
// with -deb, the harness installs the Debian package with apt. With
// -upgrade-from it installs an earlier release's package first, checks it,
// upgrades it to -deb and checks that the server came back on the new
// version with its state. Then it tests the server the same way. The
// consoles, the server's journal and the suites' output are saved in the
// work directory's logs, whatever happens.
//
//	go run ./test/qemu -iso dist/racksail-1.2.3-amd64.iso -firmware secureboot
//	go run ./test/qemu -iso dist/racksail-1.2.3-amd64.iso -disks 2 -answers test/fixtures/answers/install-mirror.yaml
//	go run ./test/qemu -cloud-image debian-13-genericcloud-amd64.qcow2 -binary bin/racksail-linux-amd64
//	go run ./test/qemu -cloud-image debian-13-genericcloud-amd64.qcow2 -deb dist/racksail_1.3.0_amd64.deb \
//		-upgrade-from racksail_1.2.4_amd64.deb -version 1.3.0
//
// It needs QEMU, qemu-img, the UEFI firmware (ovmf, qemu-efi-aarch64),
// dosfstools, mtools and OpenSSH, and runs fastest on Linux with KVM.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
)

// Config is one run of the harness.
type Config struct {
	ISO        string
	CloudImage string
	Binary     string
	Answers    string
	Convert    string
	Arch       string
	Firmware   string
	Accel      string
	CPUs       int
	MemoryMB   int
	Disks      int
	DiskBus    string
	DiskSizeGB int
	Code, Vars string
	Work       string
	Repo       string
	Suites     []string
	Version    string

	// MemoryBudget is how much memory, in MiB, Racksail's services may hold
	// once the server has settled for Settle; 0 skips the check.
	MemoryBudget int
	Settle       time.Duration

	// Deb is the Debian package the cloud image is set up with, and
	// UpgradeFrom an earlier release's package installed before it.
	Deb, UpgradeFrom string

	InstallTimeout, BootTimeout time.Duration
	Keep                        bool
}

func main() {
	log.SetFlags(log.Ltime)
	log.SetPrefix("harness: ")
	cfg, err := parseFlags(os.Args[1:])
	if errors.Is(err, flag.ErrHelp) {
		os.Exit(0)
	}
	if err != nil {
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, cfg); err != nil {
		log.Printf("failed: %v", err)
		log.Printf("logs are in %s", filepath.Join(cfg.Work, "logs"))
		os.Exit(1)
	}
	log.Printf("passed; logs are in %s", filepath.Join(cfg.Work, "logs"))
}

func parseFlags(args []string) (Config, error) {
	var c Config
	var suites string
	flags := flag.NewFlagSet("qemu", flag.ContinueOnError)
	flags.StringVar(&c.ISO, "iso", "", "installer image to install from")
	flags.StringVar(&c.CloudImage, "cloud-image", "", "Debian 13 cloud image (qcow2) to convert, instead of -iso")
	flags.StringVar(&c.Binary, "binary", "", "racksail binary the cloud image is converted with")
	flags.StringVar(&c.Deb, "deb", "", "Debian package to install on the cloud image with apt, instead of -binary")
	flags.StringVar(&c.UpgradeFrom, "upgrade-from", "", "an earlier release's Debian package to install first and upgrade to -deb")
	flags.StringVar(&c.Answers, "answers", "test/fixtures/answers/install-single.yaml", "answers file for the ISO; the harness puts in its own SSH key and the machine's disks")
	flags.StringVar(&c.Convert, "convert-answers", "test/fixtures/answers/convert.yaml", "convert answers file for the cloud image")
	flags.StringVar(&c.Arch, "arch", "", "amd64 or arm64; taken from the image's name by default")
	flags.StringVar(&c.Firmware, "firmware", "uefi", "bios, uefi or secureboot")
	flags.StringVar(&c.Accel, "accel", "auto", "kvm, hvf, tcg, or auto to pick the fastest available")
	flags.IntVar(&c.CPUs, "cpus", 2, "virtual CPUs")
	flags.IntVar(&c.MemoryMB, "memory", 4096, "memory in MB")
	flags.IntVar(&c.Disks, "disks", 1, "number of disks")
	flags.StringVar(&c.DiskBus, "disk-bus", "virtio", "virtio, sata or nvme")
	flags.IntVar(&c.DiskSizeGB, "disk-size", 24, "size of each disk in GB")
	flags.StringVar(&c.Code, "firmware-code", "", "UEFI code image, instead of the one found on this machine")
	flags.StringVar(&c.Vars, "firmware-vars", "", "UEFI variables template, instead of the one found on this machine")
	flags.StringVar(&c.Work, "work", "build/e2e", "work directory for disks, keys and logs")
	flags.StringVar(&c.Repo, "repo", ".", "the repository, for the suites")
	flags.StringVar(&suites, "suites", "api,ui", "suites to run against the panel: api, ui, or none")
	flags.StringVar(&c.Version, "version", "", "version the panel must report; with -upgrade-from, the version -deb holds")
	flags.IntVar(&c.MemoryBudget, "memory-budget", 100, "MiB Racksail's services may hold once the server has settled, every module off; 0 skips the check")
	flags.DurationVar(&c.Settle, "settle", time.Minute, "how long the server settles before its idle memory is measured")
	flags.DurationVar(&c.InstallTimeout, "install-timeout", time.Hour, "how long the install may take")
	flags.DurationVar(&c.BootTimeout, "boot-timeout", 20*time.Minute, "how long the installed system may take to serve the panel")
	flags.BoolVar(&c.Keep, "keep", false, "keep the machine running after the suites, until interrupted")
	if err := flags.Parse(args); err != nil {
		return c, err
	}
	switch {
	case c.ISO == "" && c.CloudImage == "":
		return c, errors.New("give -iso or -cloud-image")
	case c.ISO != "" && c.CloudImage != "":
		return c, errors.New("give -iso or -cloud-image, not both")
	case c.ISO != "" && (c.Binary != "" || c.Deb != "" || c.UpgradeFrom != ""):
		return c, errors.New("-binary, -deb and -upgrade-from set up a cloud image; the ISO brings its own racksail")
	case c.CloudImage != "" && (c.Binary == "") == (c.Deb == ""):
		return c, errors.New("-cloud-image needs -binary, to convert the server with, or -deb, the Debian package to install")
	case c.UpgradeFrom != "" && c.Deb == "":
		return c, errors.New("-upgrade-from needs -deb, the package to upgrade to")
	case c.UpgradeFrom != "" && c.Version == "":
		return c, errors.New("-upgrade-from needs -version, the version -deb holds, which the panel must report after the upgrade")
	}
	if c.Arch == "" {
		c.Arch = ArchOf(filepath.Base(c.ISO + c.CloudImage))
	}
	if c.Arch == "" {
		c.Arch = runtime.GOARCH
	}
	if suites != "none" {
		c.Suites = strings.Split(suites, ",")
	}
	if c.Accel == "auto" {
		c.Accel = PickAccel(runtime.GOOS, runtime.GOARCH, c.Arch, kvmUsable())
	}
	return c, nil
}

// kvmUsable reports whether this user can open /dev/kvm.
func kvmUsable() bool {
	f, err := os.OpenFile("/dev/kvm", os.O_RDWR, 0)
	if err != nil {
		return false
	}
	f.Close()
	return true
}

// workMarker marks a work directory as the harness's, which it may empty
// at the start of the next run.
const workMarker = ".racksail-e2e"

// prepareWork empties the work directory of an earlier run, and refuses to
// touch a directory the harness didn't make.
func prepareWork(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if len(entries) > 0 {
		if _, err := os.Stat(filepath.Join(dir, workMarker)); err != nil {
			return fmt.Errorf("%s isn't empty and isn't a work directory of the harness; choose another with -work", dir)
		}
		for _, e := range entries {
			if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
				return err
			}
		}
	}
	if err := os.MkdirAll(filepath.Join(dir, "logs"), 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, workMarker), nil, 0o644)
}

// checkInputs checks that the files the run starts from exist, before it
// spends minutes booting a machine that can't get them.
func checkInputs(c Config) error {
	inputs := []struct{ flag, file string }{
		{"-iso", c.ISO}, {"-cloud-image", c.CloudImage}, {"-binary", c.Binary}, {"-deb", c.Deb}, {"-upgrade-from", c.UpgradeFrom},
	}
	var errs []error
	for _, in := range inputs {
		if in.file == "" {
			continue
		}
		if info, err := os.Stat(in.file); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", in.flag, err))
		} else if !info.Mode().IsRegular() {
			errs = append(errs, fmt.Errorf("%s: %s isn't a file", in.flag, in.file))
		}
	}
	return errors.Join(errs...)
}

func run(ctx context.Context, c Config) error {
	if err := checkInputs(c); err != nil {
		return err
	}
	if err := prepareWork(c.Work); err != nil {
		return err
	}
	logs := filepath.Join(c.Work, "logs")
	m := Machine{Arch: c.Arch, Firmware: c.Firmware, Accel: c.Accel, CPUs: c.CPUs, MemoryMB: c.MemoryMB, DiskBus: c.DiskBus}
	if c.Firmware != "bios" {
		code, vars := c.Code, c.Vars
		if code == "" || vars == "" {
			var err error
			if code, vars, err = FindFirmware(c.Arch, c.Firmware, exists); err != nil {
				return err
			}
		}
		m.FirmwareCode = code
		m.FirmwareVars = filepath.Join(c.Work, "vars.fd")
		if err := copyFile(vars, m.FirmwareVars); err != nil {
			return fmt.Errorf("copying the UEFI variables: %w", err)
		}
	}
	for i := range c.Disks {
		m.Disks = append(m.Disks, filepath.Join(c.Work, fmt.Sprintf("disk%d.qcow2", i)))
	}
	var err error
	if m.PanelPort, err = FreePort(); err != nil {
		return err
	}
	if m.SSHPort, err = FreePort(); err != nil {
		return err
	}
	if err := m.Check(); err != nil {
		return err
	}
	key := filepath.Join(c.Work, "id_ed25519")
	pub, err := SSHKey(ctx, key)
	if err != nil {
		return err
	}
	log.Printf("%s machine, %s firmware, %s, %d %s disks", m.Arch, m.Firmware, m.Accel, len(m.Disks), m.DiskBus)

	var media Media
	var files string
	if c.ISO != "" {
		if err := install(ctx, c, m, pub); err != nil {
			return err
		}
	} else {
		base := c.CloudImage
		if err := Disk(ctx, m.Disks[0], base, c.DiskSizeGB); err != nil {
			return err
		}
		for _, d := range m.Disks[1:] {
			if err := Disk(ctx, d, "", c.DiskSizeGB); err != nil {
				return err
			}
		}
		seed, server, stopServer, err := cloudSeed(ctx, c, pub)
		if err != nil {
			return err
		}
		defer stopServer()
		media.Seed, files = seed, server
	}

	m.SerialLog = filepath.Join(logs, "boot-serial.log")
	log.Print("booting the server")
	vm, err := start(ctx, m, media, filepath.Join(logs, "qemu-boot.txt"))
	if err != nil {
		return err
	}
	defer vm.stop()

	panel := fmt.Sprintf("https://127.0.0.1:%d", m.PanelPort)
	ssh := SSH{Key: key, Port: m.SSHPort, User: "admin"}
	collectLogs := func() {
		if err := ssh.CollectLogs(context.WithoutCancel(ctx), logs); err != nil {
			log.Printf("collecting logs: %v", err)
		}
	}
	bootCtx, cancel := context.WithTimeout(ctx, c.BootTimeout)
	defer cancel()
	sshReady := false
	if c.Deb != "" {
		// Debian boots as it comes, and the package sets it up: the
		// earlier release's first when the run tests an upgrade.
		if err := ssh.Wait(bootCtx, 5*time.Second); err != nil {
			return fmt.Errorf("%w\nlast console lines:\n%s", err, tail(m.SerialLog, 30))
		}
		sshReady = true
		defer collectLogs()
		first, name := c.Deb, packageName
		if c.UpgradeFrom != "" {
			first, name = c.UpgradeFrom, earlierName
		}
		log.Printf("installing %s", filepath.Base(first))
		if err := InstallPackage(bootCtx, ssh, files+"/"+name, filepath.Join(logs, "apt-install.txt")); err != nil {
			return err
		}
	}
	if err := WaitForPanel(bootCtx, panel, 5*time.Second); err != nil {
		return fmt.Errorf("%w\nlast console lines:\n%s", err, tail(m.SerialLog, 30))
	}
	log.Printf("the panel answers at %s", panel)

	var errs []error
	if !sshReady {
		if err := ssh.Wait(bootCtx, 5*time.Second); err != nil {
			errs = append(errs, err)
		} else {
			sshReady = true
			defer collectLogs()
		}
	}
	if sshReady && c.Firmware == "secureboot" {
		on, err := ssh.SecureBootOn(ctx)
		switch {
		case err != nil:
			errs = append(errs, err)
		case !on:
			errs = append(errs, errors.New("the installed system didn't boot with Secure Boot on"))
		default:
			log.Print("the installed system booted with Secure Boot on")
		}
	}
	if c.UpgradeFrom != "" {
		if err := upgrade(ctx, ssh, panel, files+"/"+packageName, c.Version, c.BootTimeout, logs); err != nil {
			// The suites would only test the earlier release.
			return errors.Join(append(errs, err)...)
		}
	}
	// Before the suites, which leave the panel busier than an idle server.
	if sshReady && c.MemoryBudget > 0 {
		if err := checkIdleMemory(ctx, ssh, "/sys/fs/cgroup", c.Settle, memorySamples, memoryEvery, c.MemoryBudget, logs); err != nil {
			errs = append(errs, err)
		}
	}
	if len(c.Suites) > 0 {
		log.Printf("running the %s suites", strings.Join(c.Suites, " and "))
		if err := RunSuites(ctx, c.Repo, panel, c.Version, logs, c.Suites); err != nil {
			errs = append(errs, err)
		}
	}
	if c.Keep {
		log.Printf("the machine keeps running: ssh -i %s -p %d admin@127.0.0.1, panel at %s; interrupt to stop it", key, m.SSHPort, panel)
		<-ctx.Done()
	}
	return errors.Join(errs...)
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// install runs the unattended install from the ISO, which ends when the
// installer reboots the machine.
func install(ctx context.Context, c Config, m Machine, sshKey string) error {
	for _, d := range m.Disks {
		if err := Disk(ctx, d, "", c.DiskSizeGB); err != nil {
			return err
		}
	}
	template, err := os.ReadFile(c.Answers)
	if err != nil {
		return err
	}
	answers, err := RenderAnswers(template, sshKey, m.DevicePaths())
	if err != nil {
		return err
	}
	logs := filepath.Join(c.Work, "logs")
	if err := os.WriteFile(filepath.Join(logs, "answers.yaml"), answers, 0o644); err != nil {
		return err
	}
	stick := filepath.Join(c.Work, "answers.img")
	if err := FATImage(ctx, stick, "RACKSAIL", map[string][]byte{"answers.yaml": answers}); err != nil {
		return err
	}

	m.SerialLog = filepath.Join(logs, "install-serial.log")
	log.Printf("installing from %s", c.ISO)
	installCtx, cancel := context.WithTimeout(ctx, c.InstallTimeout)
	defer cancel()
	vm, err := start(installCtx, m, Media{ISO: c.ISO, Answers: stick, NoReboot: true}, filepath.Join(logs, "qemu-install.txt"))
	if err != nil {
		return err
	}
	if err := vm.wait(); err != nil {
		if installCtx.Err() != nil {
			return fmt.Errorf("the install didn't finish within %s\nlast console lines:\n%s", c.InstallTimeout, tail(m.SerialLog, 40))
		}
		return fmt.Errorf("QEMU stopped during the install: %w\n%s", err, tail(filepath.Join(logs, "qemu-install.txt"), 20))
	}
	if c.Firmware == "secureboot" {
		console, _ := os.ReadFile(m.SerialLog)
		if !strings.Contains(string(console), "Secure boot enabled") {
			return errors.New("the installer image didn't boot with Secure Boot on; its kernel didn't report it on the serial console")
		}
		log.Print("the installer image booted with Secure Boot on")
	}
	log.Print("the installer finished and rebooted")
	return nil
}

// The names the harness serves the run's files under.
const (
	binaryName  = "racksail"
	packageName = "racksail.deb"
	earlierName = "racksail-earlier.deb"
)

// cloudSeed serves the run's racksail binary or Debian packages to the guest
// and builds the cloud-init seed: an admin who signs in with the harness's
// key and, with -binary, the conversion of the server. QEMU's user network
// shows the host's loopback interface to the guest as 10.0.2.2. It returns
// the seed and the address the guest downloads the files from.
func cloudSeed(ctx context.Context, c Config, sshKey string) (string, string, func(), error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", "", nil, err
	}
	mux := http.NewServeMux()
	for name, file := range map[string]string{binaryName: c.Binary, packageName: c.Deb, earlierName: c.UpgradeFrom} {
		if file == "" {
			continue
		}
		mux.HandleFunc("GET /"+name, func(w http.ResponseWriter, r *http.Request) {
			http.ServeFile(w, r, file)
		})
	}
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go srv.Serve(l)
	stopServer := func() { srv.Close() }
	fail := func(err error) (string, string, func(), error) {
		stopServer()
		return "", "", nil, err
	}

	server := fmt.Sprintf("http://10.0.2.2:%d", l.Addr().(*net.TCPAddr).Port)
	u := UserData{Hostname: "racksail-cloud", SSHKey: sshKey}
	if c.Binary != "" {
		answers, err := os.ReadFile(c.Convert)
		if err != nil {
			return fail(err)
		}
		u.BinaryURL, u.Answers = server+"/"+binaryName, string(answers)
	}
	userData, err := RenderUserData(u)
	if err != nil {
		return fail(err)
	}
	logs := filepath.Join(c.Work, "logs")
	if err := os.WriteFile(filepath.Join(logs, "user-data.yaml"), userData, 0o644); err != nil {
		return fail(err)
	}
	seed := filepath.Join(c.Work, "seed.img")
	files := map[string][]byte{"user-data": userData, "meta-data": MetaData("racksail-cloud")}
	if err := FATImage(ctx, seed, "CIDATA", files); err != nil {
		return fail(err)
	}
	return seed, server, stopServer, nil
}

// vm is a running QEMU.
type vm struct {
	cmd  *exec.Cmd
	out  *os.File
	done chan error
}

func start(ctx context.Context, m Machine, media Media, output string) (*vm, error) {
	out, err := os.Create(output)
	if err != nil {
		return nil, err
	}
	args := m.Args(media)
	fmt.Fprintf(out, "%s %s\n\n", m.Binary(), strings.Join(args, " "))
	cmd := exec.CommandContext(ctx, m.Binary(), args...)
	cmd.Stdout = out
	cmd.Stderr = out
	// QEMU gets a moment to close its files when the harness is stopped.
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 15 * time.Second
	if err := cmd.Start(); err != nil {
		out.Close()
		return nil, fmt.Errorf("starting %s: %w", m.Binary(), err)
	}
	v := &vm{cmd: cmd, out: out, done: make(chan error, 1)}
	go func() { v.done <- cmd.Wait(); out.Close() }()
	return v, nil
}

func (v *vm) wait() error { return <-v.done }

func (v *vm) stop() {
	select {
	case <-v.done:
		return
	default:
	}
	v.cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-v.done:
	case <-time.After(15 * time.Second):
		v.cmd.Process.Kill()
		<-v.done
	}
}
