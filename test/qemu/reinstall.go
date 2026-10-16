package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// removeScript removes the package the way people do, which keeps its
// configuration and the panel's state.
const removeScript = "set -e\n" +
	"sudo DEBIAN_FRONTEND=noninteractive apt-get remove --yes -o DPkg::Lock::Timeout=600 racksail\n" +
	"dpkg-query --show --showformat='${Package} ${Version} ${Status}\\n' racksail"

// checkStopped checks that none of the package's units runs.
func checkStopped(ctx context.Context, s SSH) error {
	// is-active fails when a unit doesn't run, which is what's wanted here.
	out, _ := s.Run(ctx, "systemctl is-active "+strings.Join(packageUnits, " "))
	states := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(states) != len(packageUnits) {
		return fmt.Errorf("asking systemd whether Racksail's services run: %s", strings.TrimSpace(string(out)))
	}
	var running []string
	for i, state := range states {
		if strings.TrimSpace(state) == "active" {
			running = append(running, packageUnits[i])
		}
	}
	switch n := len(running); n {
	case 0:
		return nil
	case 1:
		return fmt.Errorf("%s still runs after the package was removed", running[0])
	default:
		return fmt.Errorf("%s and %s still run after the package was removed", strings.Join(running[:n-1], ", "), running[n-1])
	}
}

// reinstall removes the package and installs it again from url, as an
// administrator might to repair a server, and checks that the server comes
// back as it was: its services enabled and running, and the panel with its
// state, which shows in the certificate it keeps there. All of it must
// finish within timeout.
func reinstall(ctx context.Context, s SSH, panel, url string, timeout time.Duration, logs string) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	certificate, err := PanelFingerprint(ctx, panel)
	if err != nil {
		return err
	}
	log.Print("removing the package")
	out, err := s.Run(ctx, removeScript)
	logFile := filepath.Join(logs, "apt-remove.txt")
	if werr := os.WriteFile(logFile, out, 0o644); werr != nil {
		return werr
	}
	if err != nil {
		return fmt.Errorf("removing the package failed (see apt-remove.txt): %w\n%s", err, tail(logFile, 20))
	}
	if err := checkStopped(ctx, s); err != nil {
		return err
	}
	log.Print("installing the package again")
	if err := InstallPackage(ctx, s, url, filepath.Join(logs, "apt-reinstall.txt")); err != nil {
		return err
	}
	if err := WaitForPanel(ctx, panel, 2*time.Second); err != nil {
		return fmt.Errorf("after installing the package again: %w", err)
	}
	if err := checkServices(ctx, s); err != nil {
		return fmt.Errorf("after installing the package again: %w", err)
	}
	now, err := PanelFingerprint(ctx, panel)
	if err != nil {
		return err
	}
	if now != certificate {
		return errors.New("the panel serves another certificate after the package was removed and installed again, so its state didn't survive the removal")
	}
	log.Print("the server came back after removing the package and installing it again")
	return nil
}
