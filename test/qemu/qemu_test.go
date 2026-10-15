package main

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/vilanobeachflorida/racksail/internal/installer"
)

var update = flag.Bool("update", false, "rewrite golden files")

func golden(t *testing.T, name, got string) {
	t.Helper()
	file := filepath.Join("testdata", name+".golden")
	if *update {
		if err := os.WriteFile(file, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Errorf("%s differs from %s; run go test ./test/qemu -update after an intended change\n%s", name, file, got)
	}
}

func machine(arch, firmware, bus string, disks int) Machine {
	m := Machine{Arch: arch, Firmware: firmware, Accel: "kvm", CPUs: 2, MemoryMB: 4096, DiskBus: bus,
		PanelPort: 18443, SSHPort: 10022, SerialLog: "work/logs/serial.log"}
	switch {
	case arch == "arm64":
		m.FirmwareCode, m.FirmwareVars = "/usr/share/AAVMF/AAVMF_CODE.fd", "work/vars.fd"
	case firmware == "secureboot":
		m.FirmwareCode, m.FirmwareVars = "/usr/share/OVMF/OVMF_CODE_4M.secboot.fd", "work/vars.fd"
	case firmware == "uefi":
		m.FirmwareCode, m.FirmwareVars = "/usr/share/OVMF/OVMF_CODE_4M.fd", "work/vars.fd"
	}
	for i := range disks {
		m.Disks = append(m.Disks, "work/disk"+string(rune('0'+i))+".qcow2")
	}
	return m
}

// The QEMU command lines of the scenarios CI runs.
func TestQEMUArgs(t *testing.T) {
	cases := map[string]struct {
		m     Machine
		media Media
	}{
		"install-amd64-bios":       {machine("amd64", "bios", "virtio", 1), Media{ISO: "racksail.iso", Answers: "work/answers.img", NoReboot: true}},
		"install-amd64-secureboot": {machine("amd64", "secureboot", "virtio", 2), Media{ISO: "racksail.iso", Answers: "work/answers.img", NoReboot: true}},
		"install-amd64-sata":       {machine("amd64", "uefi", "sata", 2), Media{ISO: "racksail.iso", Answers: "work/answers.img", NoReboot: true}},
		"install-arm64-uefi":       {machine("arm64", "uefi", "nvme", 1), Media{ISO: "racksail.iso", Answers: "work/answers.img", NoReboot: true}},
		"boot-amd64-uefi":          {machine("amd64", "uefi", "virtio", 1), Media{}},
		"cloud-arm64-tcg": {func() Machine {
			m := machine("arm64", "uefi", "virtio", 1)
			m.Accel = "tcg"
			return m
		}(), Media{Seed: "work/seed.img"}},
	}
	for name, tc := range cases {
		if err := tc.m.Check(); err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		golden(t, name, formatArgs(tc.m.Binary(), tc.m.Args(tc.media)))
	}
}

// formatArgs writes a command line one option and its value per line.
func formatArgs(binary string, args []string) string {
	lines := []string{binary}
	for i := 0; i < len(args); i++ {
		if i+1 < len(args) && strings.HasPrefix(args[i], "-") && !strings.HasPrefix(args[i+1], "-") {
			lines = append(lines, args[i]+" "+args[i+1])
			i++
			continue
		}
		lines = append(lines, args[i])
	}
	return strings.Join(lines, " \\\n  ") + "\n"
}

func TestMachineChecks(t *testing.T) {
	bad := map[string]Machine{
		"bios on arm64":    machine("arm64", "bios", "virtio", 1),
		"no firmware":      {Arch: "amd64", Firmware: "uefi", Accel: "kvm", DiskBus: "virtio", Disks: []string{"d"}},
		"unknown bus":      machine("amd64", "bios", "scsi", 1),
		"no disks":         machine("amd64", "bios", "virtio", 0),
		"unknown arch":     machine("riscv64", "uefi", "virtio", 1),
		"unknown firmware": machine("amd64", "coreboot", "virtio", 1),
	}
	for name, m := range bad {
		if err := m.Check(); err == nil {
			t.Errorf("%s: Check should fail", name)
		}
	}
}

func TestDevicePaths(t *testing.T) {
	cases := map[string]string{
		"virtio": "/dev/disk/by-id/virtio-racksail1",
		"sata":   "/dev/disk/by-id/ata-QEMU_HARDDISK_racksail1",
		"nvme":   "/dev/disk/by-id/nvme-QEMU_NVMe_Ctrl_racksail1",
	}
	for bus, want := range cases {
		paths := machine("amd64", "uefi", bus, 2).DevicePaths()
		if len(paths) != 2 || paths[1] != want {
			t.Errorf("%s: %v", bus, paths)
		}
	}
}

func TestFindFirmware(t *testing.T) {
	present := map[string]bool{
		"/usr/share/OVMF/OVMF_CODE_4M.fd":               true,
		"/usr/share/OVMF/OVMF_VARS_4M.fd":               true,
		"/usr/share/OVMF/OVMF_CODE_4M.secboot.fd":       true,
		"/usr/share/OVMF/OVMF_VARS_4M.ms.fd":            true,
		"/opt/homebrew/share/qemu/edk2-aarch64-code.fd": true,
		"/opt/homebrew/share/qemu/edk2-arm-vars.fd":     true,
	}
	exists := func(p string) bool { return present[p] }
	cases := map[[2]string][2]string{
		{"amd64", "uefi"}:       {"/usr/share/OVMF/OVMF_CODE_4M.fd", "/usr/share/OVMF/OVMF_VARS_4M.fd"},
		{"amd64", "secureboot"}: {"/usr/share/OVMF/OVMF_CODE_4M.secboot.fd", "/usr/share/OVMF/OVMF_VARS_4M.ms.fd"},
		{"arm64", "uefi"}:       {"/opt/homebrew/share/qemu/edk2-aarch64-code.fd", "/opt/homebrew/share/qemu/edk2-arm-vars.fd"},
		{"amd64", "bios"}:       {"", ""},
	}
	for in, want := range cases {
		code, vars, err := FindFirmware(in[0], in[1], exists)
		if err != nil || code != want[0] || vars != want[1] {
			t.Errorf("FindFirmware(%v) = %s, %s, %v", in, code, vars, err)
		}
	}
	if _, _, err := FindFirmware("arm64", "secureboot", exists); err == nil || !strings.Contains(err.Error(), "qemu-efi-aarch64") {
		t.Errorf("missing firmware must say what to install: %v", err)
	}
}

func TestPickAccel(t *testing.T) {
	cases := []struct {
		goos, host, guest string
		kvm               bool
		want              string
	}{
		{"linux", "amd64", "amd64", true, "kvm"},
		{"linux", "amd64", "amd64", false, "tcg"},
		{"linux", "amd64", "arm64", true, "tcg"},
		{"darwin", "arm64", "arm64", false, "hvf"},
		{"darwin", "arm64", "amd64", false, "tcg"},
	}
	for _, tc := range cases {
		if got := PickAccel(tc.goos, tc.host, tc.guest, tc.kvm); got != tc.want {
			t.Errorf("PickAccel(%+v) = %s", tc, got)
		}
	}
	if ArchOf("racksail-1.2.3-arm64-firmware.iso") != "arm64" || ArchOf("debian-13-genericcloud-amd64.qcow2") != "amd64" || ArchOf("image.iso") != "" {
		t.Error("ArchOf")
	}
}

const harnessKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIEXAMPLEexampleEXAMPLEexampleEXAMPLEexample0 racksail-e2e"

func TestRenderAnswers(t *testing.T) {
	template, err := os.ReadFile("../fixtures/answers/install-mirror.yaml")
	if err != nil {
		t.Fatal(err)
	}
	devices := machine("amd64", "uefi", "sata", 2).DevicePaths()
	out, err := RenderAnswers(template, harnessKey+"\n", devices)
	if err != nil {
		t.Fatal(err)
	}
	a, err := installer.ParseAnswers(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Account.SSHKeys) != 1 || a.Account.SSHKeys[0] != harnessKey || !a.Reboot || a.Disks.Devices[1] != devices[1] || a.Hostname != "racksail-mirror.example.test" {
		t.Errorf("answers = %+v", a)
	}

	if _, err := RenderAnswers(template, harnessKey, devices[:1]); err == nil || !strings.Contains(err.Error(), "-disks") {
		t.Errorf("a mirror on one disk must say to add disks: %v", err)
	}
	convert, _ := os.ReadFile("../fixtures/answers/convert.yaml")
	if _, err := RenderAnswers(convert, harnessKey, devices); err == nil {
		t.Error("the ISO scenario can't convert")
	}
	single, _ := os.ReadFile("../fixtures/answers/install-single.yaml")
	out, err = RenderAnswers(single, harnessKey, devices)
	if err != nil {
		t.Fatal(err)
	}
	if a, _ := installer.ParseAnswers(out); len(a.Disks.Devices) != 1 {
		t.Errorf("a single layout uses one disk: %v", a.Disks.Devices)
	}
}

func TestRenderUserData(t *testing.T) {
	answers, err := os.ReadFile("../fixtures/answers/convert.yaml")
	if err != nil {
		t.Fatal(err)
	}
	out, err := RenderUserData(UserData{Hostname: "racksail-cloud", SSHKey: harnessKey, BinaryURL: "http://10.0.2.2:8080/racksail", Answers: string(answers)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(out), "#cloud-config\n") {
		t.Error("user data must start with #cloud-config")
	}
	var cfg struct {
		Users []struct {
			Name string   `yaml:"name"`
			Keys []string `yaml:"ssh_authorized_keys"`
		} `yaml:"users"`
		WriteFiles []struct {
			Path    string `yaml:"path"`
			Content string `yaml:"content"`
		} `yaml:"write_files"`
		Runcmd [][]string `yaml:"runcmd"`
	}
	if err := yaml.Unmarshal(out, &cfg); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Users) != 1 || cfg.Users[0].Keys[0] != harnessKey {
		t.Errorf("users = %+v", cfg.Users)
	}
	written, err := installer.ParseAnswers([]byte(cfg.WriteFiles[0].Content))
	if err != nil || written.Mode != installer.ModeConvert {
		t.Errorf("the answers the server gets = %+v, %v", written, err)
	}
	last := cfg.Runcmd[len(cfg.Runcmd)-1]
	if strings.Join(last, " ") != "/usr/local/bin/racksail install --convert --answers /root/racksail-answers.yaml" {
		t.Errorf("runcmd = %v", cfg.Runcmd)
	}
	golden(t, "user-data", string(out))
	if _, err := RenderUserData(UserData{Answers: "mode: nonsense"}); err == nil {
		t.Error("invalid answers must be refused")
	}
}

func TestFlags(t *testing.T) {
	c, err := parseFlags([]string{"-iso", "dist/racksail-1.2.3-arm64.iso", "-accel", "tcg", "-suites", "api"})
	if err != nil || c.Arch != "arm64" || c.Accel != "tcg" || len(c.Suites) != 1 {
		t.Errorf("config = %+v, %v", c, err)
	}
	for _, args := range [][]string{nil, {"-iso", "a.iso", "-cloud-image", "b.qcow2"}, {"-cloud-image", "b.qcow2"}} {
		if _, err := parseFlags(args); err == nil {
			t.Errorf("parseFlags(%v) should fail", args)
		}
	}
	if c, _ := parseFlags([]string{"-iso", "a.iso", "-suites", "none"}); len(c.Suites) != 0 {
		t.Error("-suites none runs no suites")
	}
}
