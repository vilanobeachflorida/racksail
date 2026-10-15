package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"text/template"

	"go.yaml.in/yaml/v3"

	"github.com/vilanobeachflorida/racksail/internal/installer"
)

// RenderAnswers prepares an answers file for a machine: the harness's SSH
// key in place of the file's keys, the machine's disks in place of its
// devices, and a reboot at the end, which ends the install phase. The result
// is checked as the installer would check it.
func RenderAnswers(template []byte, sshKey string, devices []string) ([]byte, error) {
	a, err := installer.ParseAnswers(template)
	if err != nil {
		return nil, fmt.Errorf("the answers file: %w", err)
	}
	if a.Mode == installer.ModeConvert {
		return nil, fmt.Errorf("the answers file is for converting a running server; the ISO scenario installs, so give an install or restore answers file")
	}
	want := 1
	if a.Disks.Layout == "mirror" {
		want = 2
	} else if a.Disks.Layout == "custom" {
		want = len(a.Disks.Devices)
	}
	if want > len(devices) {
		return nil, fmt.Errorf("the answers file's %s layout needs %d disks, and the machine has %d; use -disks", a.Disks.Layout, want, len(devices))
	}
	a.Disks.Devices = slices.Clone(devices[:want])
	a.Account.SSHKeys = []string{strings.TrimSpace(sshKey)}
	a.Reboot = true
	out, err := yaml.Marshal(a)
	if err != nil {
		return nil, err
	}
	if _, err := installer.ParseAnswers(out); err != nil {
		return nil, fmt.Errorf("the prepared answers file isn't valid: %w", err)
	}
	return out, nil
}

// UserData is what the cloud scenario's seed asks cloud-init to do.
type UserData struct {
	Hostname string
	SSHKey   string
	// BinaryURL is where the guest downloads racksail from to convert
	// itself: the harness, at QEMU's address for the host. Without it the
	// server boots as plain Debian, for the harness to install the package.
	BinaryURL string
	// Answers is the convert answers file the server is set up with.
	Answers string
}

var userDataTemplate = template.Must(template.New("user-data").Parse(`#cloud-config
hostname: {{.Hostname}}
users:
  - name: admin
    groups: [sudo, adm, systemd-journal]
    sudo: "ALL=(ALL) NOPASSWD:ALL"
    shell: /bin/bash
    lock_passwd: true
    ssh_authorized_keys:
      - {{.SSHKey}}
{{- if .BinaryURL}}
write_files:
  - path: /root/racksail-answers.yaml
    permissions: "0600"
    content: |
{{.Answers}}
runcmd:
  - [curl, --fail, --silent, --show-error, --retry, "5", --output, /usr/local/bin/racksail, "{{.BinaryURL}}"]
  - [chmod, "0755", /usr/local/bin/racksail]
  - [/usr/local/bin/racksail, install, --convert, --answers, /root/racksail-answers.yaml]
{{- end}}
`))

// RenderUserData writes the cloud-init user data of the cloud scenario: an
// admin who signs in with the harness's key and, with a binary to download,
// the conversion.
func RenderUserData(u UserData) ([]byte, error) {
	if u.BinaryURL != "" {
		if _, err := installer.ParseAnswers([]byte(u.Answers)); err != nil {
			return nil, fmt.Errorf("the convert answers: %w", err)
		}
		u.Answers = "      " + strings.ReplaceAll(strings.TrimRight(u.Answers, "\n"), "\n", "\n      ")
	}
	u.SSHKey = strings.TrimSpace(u.SSHKey)
	var b bytes.Buffer
	if err := userDataTemplate.Execute(&b, u); err != nil {
		return nil, err
	}
	var check map[string]any
	if err := yaml.Unmarshal(b.Bytes(), &check); err != nil {
		return nil, fmt.Errorf("the user data isn't valid YAML: %w", err)
	}
	return b.Bytes(), nil
}

// MetaData is the seed's meta-data.
func MetaData(hostname string) []byte {
	return []byte("instance-id: racksail-e2e\nlocal-hostname: " + hostname + "\n")
}

// runTool runs a helper program and reports its output when it fails.
func runTool(ctx context.Context, name string, args ...string) error {
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %w\n%s", name, strings.Join(args, " "), err, out)
	}
	return nil
}

// FATImage writes a small FAT file system with a label and files at its
// root: the USB stick with the answers file, or the cloud-init seed.
func FATImage(ctx context.Context, path, label string, files map[string][]byte) error {
	os.Remove(path)
	if err := runTool(ctx, "mkfs.fat", "-C", "-n", label, path, "8192"); err != nil {
		return err
	}
	dir, err := os.MkdirTemp(filepath.Dir(path), "fat-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		src := filepath.Join(dir, name)
		if err := os.WriteFile(src, files[name], 0o644); err != nil {
			return err
		}
		if err := runTool(ctx, "mcopy", "-i", path, src, "::"+name); err != nil {
			return err
		}
	}
	return nil
}

// SSHKey creates the key the harness signs in to the server with.
func SSHKey(ctx context.Context, path string) (string, error) {
	os.Remove(path)
	os.Remove(path + ".pub")
	if err := runTool(ctx, "ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "racksail-e2e", "-f", path); err != nil {
		return "", err
	}
	pub, err := os.ReadFile(path + ".pub")
	return strings.TrimSpace(string(pub)), err
}

// Disk creates an empty disk image, or one on top of a base image.
func Disk(ctx context.Context, path, base string, sizeGB int) error {
	os.Remove(path)
	args := []string{"create", "-q", "-f", "qcow2"}
	if base != "" {
		abs, err := filepath.Abs(base)
		if err != nil {
			return err
		}
		args = append(args, "-b", abs, "-F", "qcow2")
	}
	return runTool(ctx, "qemu-img", append(args, path, fmt.Sprintf("%dG", sizeGB))...)
}

// copyFile copies the firmware's variables template, which the machine
// writes to.
func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o644)
}
