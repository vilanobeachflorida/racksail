package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// FreePort finds a port on 127.0.0.1 nothing listens on, for QEMU to
// forward.
func FreePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// WaitForPanel polls the panel's health endpoint until it answers 200 or the
// context ends. The panel's certificate is its own, so it isn't verified.
func WaitForPanel(ctx context.Context, url string, every time.Duration) error {
	client := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
	var last string
	for {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url+"/api/v1/health", nil)
		resp, err := client.Do(req)
		if err == nil {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
			last = fmt.Sprintf("%s: %s", resp.Status, body)
		} else {
			last = err.Error()
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("the panel didn't become healthy (last answer: %s): %w", last, ctx.Err())
		case <-time.After(every):
		}
	}
}

// SSH runs commands on the server as the admin the answers file created.
type SSH struct {
	Key  string
	Port int
	User string
}

func (s SSH) command(ctx context.Context, remote string) *exec.Cmd {
	return exec.CommandContext(ctx, "ssh",
		"-i", s.Key, "-p", strconv.Itoa(s.Port),
		"-o", "StrictHostKeyChecking=no", "-o", "UserKnownHostsFile=/dev/null", "-o", "LogLevel=ERROR",
		"-o", "BatchMode=yes", "-o", "ConnectTimeout=10",
		s.User+"@127.0.0.1", remote)
}

// Run runs a command and returns its output.
func (s SSH) Run(ctx context.Context, remote string) ([]byte, error) {
	var out bytes.Buffer
	cmd := s.command(ctx, remote)
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	return out.Bytes(), err
}

// Wait retries until the server accepts the key.
func (s SSH) Wait(ctx context.Context, every time.Duration) error {
	var last []byte
	for {
		out, err := s.Run(ctx, "true")
		if err == nil {
			return nil
		}
		last = out
		select {
		case <-ctx.Done():
			return fmt.Errorf("SSH didn't accept the harness's key (%s): %w", strings.TrimSpace(string(last)), ctx.Err())
		case <-time.After(every):
		}
	}
}

// secureBootVariable holds 1 when the firmware booted with Secure Boot on.
const secureBootVariable = "/sys/firmware/efi/efivars/SecureBoot-8be4df61-93ca-11d2-aa0d-00e098032b8c"

// SecureBootOn asks the server whether it booted with Secure Boot on. The
// variable's last byte is the state, after four bytes of attributes.
func (s SSH) SecureBootOn(ctx context.Context) (bool, error) {
	out, err := s.Run(ctx, "od -An -tu1 "+secureBootVariable)
	if err != nil {
		return false, fmt.Errorf("reading the Secure Boot state: %w: %s", err, out)
	}
	fields := strings.Fields(string(out))
	return len(fields) > 0 && fields[len(fields)-1] == "1", nil
}

// diagnostics are collected from the server after the suites, whatever
// happened.
var diagnostics = map[string]string{
	"journal.txt":      "journalctl -b --no-pager -o short-iso",
	"units.txt":        "systemctl list-units --all --no-pager 'racksail*'",
	"failed-units.txt": "systemctl --failed --no-pager",
	"version.txt":      "/usr/local/bin/racksail version || /usr/bin/racksail version",
	"disks.txt":        "lsblk -o NAME,SIZE,TYPE,FSTYPE,LABEL,MOUNTPOINTS && findmnt --real",
	"network.txt":      "ip -br address && ip route",
	"os-release.txt":   "cat /etc/os-release",
}

// CollectLogs saves the server's diagnostics into dir. It keeps going when
// a command fails, so one missing tool doesn't hide the rest.
func (s SSH) CollectLogs(ctx context.Context, dir string) error {
	var errs []error
	for name, remote := range diagnostics {
		out, err := s.Run(ctx, remote)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
		}
		if werr := os.WriteFile(filepath.Join(dir, name), out, 0o644); werr != nil {
			errs = append(errs, werr)
		}
	}
	return errors.Join(errs...)
}

// RunSuites runs the API and browser suites against the panel, writing
// their output into the logs directory.
func RunSuites(ctx context.Context, repo, panel, version, logs string, suites []string) error {
	env := append(os.Environ(),
		"RACKSAIL_E2E_URL="+panel,
		"RACKSAIL_E2E_INSECURE=1",
		"RACKSAIL_E2E_VERSION="+version,
		"RACKSAIL_E2E_REPORT="+filepath.Join(logs, "playwright-report"),
		"RACKSAIL_E2E_RESULTS="+filepath.Join(logs, "playwright-results"),
		"CI=1",
	)
	var errs []error
	for _, suite := range suites {
		var cmd *exec.Cmd
		switch suite {
		case "api":
			cmd = exec.CommandContext(ctx, "go", "test", "-count=1", "-v", "./test/e2e/api")
			cmd.Dir = repo
		case "ui":
			cmd = exec.CommandContext(ctx, "npx", "playwright", "test")
			cmd.Dir = filepath.Join(repo, "test", "e2e")
		default:
			return fmt.Errorf("unknown suite %q; the suites are api and ui", suite)
		}
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		if werr := os.WriteFile(filepath.Join(logs, suite+".txt"), out, 0o644); werr != nil {
			errs = append(errs, werr)
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("the %s suite failed (see %s.txt): %w", suite, suite, err))
		}
	}
	return errors.Join(errs...)
}

// tail returns the last lines of a log, for error messages.
func tail(file string, lines int) string {
	data, err := os.ReadFile(file)
	if err != nil {
		return ""
	}
	all := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(all) > lines {
		all = all[len(all)-lines:]
	}
	return strings.Join(all, "\n")
}
