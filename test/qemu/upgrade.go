package main

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	neturl "net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/installer"
	"github.com/vilanobeachflorida/racksail/internal/updates"
)

// packageScript downloads a Debian package the harness serves and installs
// it with apt, as people install Racksail's package; over an earlier
// version, apt upgrades it. cloud-init may still be using apt when SSH first
// answers, so the script waits for it.
func packageScript(url string) string {
	file := "/tmp/" + path.Base(url)
	return strings.Join([]string{
		"set -e",
		// cloud-init exits with 2 when it finished with warnings.
		"cloud-init status --wait >/dev/null || [ $? -eq 2 ]",
		"curl --fail --silent --show-error --retry 5 --output " + file + " " + url,
		// The packages Racksail recommends come from Debian's mirrors;
		// leaving them out keeps the test to Racksail's own package.
		"sudo DEBIAN_FRONTEND=noninteractive apt-get install --yes --no-install-recommends -o DPkg::Lock::Timeout=600 " + file,
		"dpkg-query --show --showformat='${Package} ${Version} ${Status}\\n' racksail",
	}, "\n")
}

// InstallPackage runs packageScript on the server and saves apt's output in
// logFile.
func InstallPackage(ctx context.Context, s SSH, url, logFile string) error {
	out, err := s.Run(ctx, packageScript(url))
	if werr := os.WriteFile(logFile, out, 0o644); werr != nil {
		return werr
	}
	if err != nil {
		return fmt.Errorf("installing %s failed (see %s): %w\n%s", path.Base(url), filepath.Base(logFile), err, tail(logFile, 20))
	}
	return nil
}

// panelClient talks to a panel with its own certificate, which nothing
// signed.
var panelClient = &http.Client{
	Timeout:   10 * time.Second,
	Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
}

// PanelVersion asks the panel which version of Racksail it runs.
func PanelVersion(ctx context.Context, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url+"/api/v1/version", nil)
	if err != nil {
		return "", err
	}
	resp, err := panelClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("asking the panel for its version: %s: %s", resp.Status, body)
	}
	var info struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(body, &info); err != nil || info.Version == "" {
		return "", fmt.Errorf("the panel's version answer has no version: %s", body)
	}
	return info.Version, nil
}

// PanelFingerprint returns the SHA-256 fingerprint of the certificate the
// panel serves. The panel keeps it in its state directory, so it survives
// restarts and upgrades, and browsers that trusted it keep trusting it.
func PanelFingerprint(ctx context.Context, url string) (string, error) {
	u, err := neturl.Parse(url)
	if err != nil {
		return "", err
	}
	d := tls.Dialer{Config: &tls.Config{InsecureSkipVerify: true}}
	conn, err := d.DialContext(ctx, "tcp", u.Host)
	if err != nil {
		return "", fmt.Errorf("reading the panel's certificate: %w", err)
	}
	defer conn.Close()
	certs := conn.(*tls.Conn).ConnectionState().PeerCertificates
	if len(certs) == 0 {
		return "", errors.New("the panel sent no certificate")
	}
	sum := sha256.Sum256(certs[0].Raw)
	return hex.EncodeToString(sum[:]), nil
}

// WaitForVersion polls the panel until it reports version, while it
// restarts after an upgrade.
func WaitForVersion(ctx context.Context, url, version string, every time.Duration) error {
	last := "none"
	for {
		v, err := PanelVersion(ctx, url)
		switch {
		case err == nil && v == version:
			return nil
		case ctx.Err() != nil:
			// The deadline cut this request short; the answer before it
			// says more about what went wrong.
		case err != nil:
			last = err.Error()
		default:
			last = "version " + v
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("the panel didn't come back on %s (last answer: %s): %w", version, last, ctx.Err())
		case <-time.After(every):
		}
	}
}

// checkOlder checks that the earlier package really is an earlier release
// than the one the server upgrades to.
func checkOlder(before, after string) error {
	b, err := updates.ParseVersion(before)
	if err != nil {
		return fmt.Errorf("the panel reports version %q: %w", before, err)
	}
	a, err := updates.ParseVersion(after)
	if err != nil {
		return fmt.Errorf("-version %q: %w", after, err)
	}
	if b.Compare(a) >= 0 {
		return fmt.Errorf("the earlier package runs %s, which isn't older than %s", before, after)
	}
	return nil
}

// packageUnits are the units the package enables and starts.
var packageUnits = []string{installer.AgentUnit, installer.PanelUnit, installer.EdgeSocket}

// checkServices checks that the package's units are still enabled and
// running.
func checkServices(ctx context.Context, s SSH) error {
	units := strings.Join(packageUnits, " ")
	out, err := s.Run(ctx, "systemctl is-enabled "+units+" && systemctl is-active "+units)
	if err != nil {
		return fmt.Errorf("not all of Racksail's services are enabled and running: %w\n%s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// upgrade upgrades the server from the earlier release it runs to the
// package at url, as apt upgrades people's servers, and checks that it came
// back on version with what it had: the panel's certificate, which lives in
// its state directory, and its services, enabled and running. All of it
// must finish within timeout.
func upgrade(ctx context.Context, s SSH, panel, url, version string, timeout time.Duration, logs string) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	before, err := PanelVersion(ctx, panel)
	if err != nil {
		return err
	}
	if err := checkOlder(before, version); err != nil {
		return err
	}
	certificate, err := PanelFingerprint(ctx, panel)
	if err != nil {
		return err
	}
	if err := checkServices(ctx, s); err != nil {
		return fmt.Errorf("before the upgrade: %w", err)
	}
	log.Printf("the panel runs %s; upgrading it to %s", before, version)
	if err := InstallPackage(ctx, s, url, filepath.Join(logs, "apt-upgrade.txt")); err != nil {
		return err
	}
	if err := WaitForVersion(ctx, panel, version, 2*time.Second); err != nil {
		return err
	}
	now, err := PanelFingerprint(ctx, panel)
	if err != nil {
		return err
	}
	if now != certificate {
		return errors.New("the panel serves another certificate after the upgrade, so its state didn't carry over and browsers that trusted it warn again")
	}
	if err := checkServices(ctx, s); err != nil {
		return fmt.Errorf("after the upgrade: %w", err)
	}
	log.Printf("upgraded from %s to %s", before, version)
	return nil
}
