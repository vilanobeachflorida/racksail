package updates

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/agent"
	"github.com/vilanobeachflorida/racksail/internal/system"
	"github.com/vilanobeachflorida/racksail/internal/updates/sigstore"
)

// The agent replaces the racksail binary, since only root may. It
// downloads the release and checks its signature itself, against the
// trusted root it pins, so a panel that was taken over can't make it
// install anything else. What it did is kept in its own state, where the
// guard that rolls a release back reads it.
const (
	// UpdateDir holds the agent's downloads, the binary an update replaced
	// and the record of the update.
	UpdateDir = system.AgentStateDir + "/updates"
	// StateFile records the last update.
	StateFile = UpdateDir + "/state.json"
	// PreviousBinary is the binary an update replaced, which a rollback
	// puts back and the guard runs as.
	PreviousBinary = UpdateDir + "/racksail.previous"
	// PanelBackups is where the panel keeps a copy of its database from
	// before an update. The agent takes it into its own state, where the
	// panel can't reach, and a rollback puts that copy back.
	PanelBackups = system.StateDir + "/updates"
	// PanelUser owns the panel's state, its backup among it.
	PanelUser = "racksail"
	// GuardUnit runs the guard once an update has had time to prove
	// itself, and RestartUnit restarts the services onto a new binary.
	GuardUnit   = "racksail-update-guard"
	RestartUnit = "racksail-update-restart"
	// GuardDelay is how long a new release has to confirm it works before
	// the guard rolls it back.
	GuardDelay = 15 * time.Minute
)

// Binaries are where the racksail binary may be: where install.sh and the
// installer put it, and where the Debian package does.
var Binaries = []string{"/usr/local/bin/racksail", "/usr/bin/racksail"}

// packageBinary is the Debian package's, which apt updates.
const packageBinary = "/usr/bin/racksail"

// Services run the racksail binary and restart onto a new one, the agent
// first since the others depend on it.
var Services = []string{"racksail-agent.service", "racksail-panel.service", "racksail-edge.service"}

// Update states.
const (
	StateInstalled  = "installed"
	StateConfirmed  = "confirmed"
	StateRolledBack = "rolled-back"
)

// UpdateState is what the agent last did to the racksail binary.
type UpdateState struct {
	State string `json:"state" enum:"installed,confirmed,rolled-back" doc:"installed until the new release confirms it works, then confirmed; rolled-back when it didn't"`
	Tag   string `json:"tag"`
	// From and To are the versions before and after.
	From   string `json:"from"`
	To     string `json:"to"`
	Binary string `json:"binary"`
	// Snapshots were taken of the system first.
	Snapshots   []agent.Snapshot `json:"snapshots,omitempty"`
	Unprotected []string         `json:"unprotected,omitempty"`
	// PanelBackup is the copy of the panel's database from before.
	PanelBackup  string     `json:"panelBackup,omitempty"`
	InstalledAt  time.Time  `json:"installedAt"`
	ConfirmedAt  *time.Time `json:"confirmedAt,omitempty"`
	RolledBackAt *time.Time `json:"rolledBackAt,omitempty"`
	Reason       string     `json:"reason,omitempty" doc:"Why the release was rolled back"`
}

// releaseClient downloads releases; tests point it at their own servers.
var releaseClient = &http.Client{Timeout: 15 * time.Minute}

// InstallReleaseInput names the release to install and where to get it.
type InstallReleaseInput struct {
	Tag string `json:"tag" doc:"The release's tag, such as v1.2.3"`
	// Asset is the binary for this server's architecture.
	Asset string `json:"asset" doc:"The binary's name, such as racksail-linux-amd64"`
	Size  int64  `json:"size"`
	// SHA256 is the binary's checksum from the release manifest.
	SHA256 string `json:"sha256"`
	// URLs and BundleURLs are tried in order.
	URLs       []string `json:"urls" doc:"Where the binary can be downloaded, mirrors first"`
	BundleURLs []string `json:"bundleUrls" doc:"Where its signature bundle can be downloaded"`
	// TrustedRootURLs serve the Sigstore trusted root the agent pins.
	TrustedRootURLs []string `json:"trustedRootUrls"`
	Binary          string   `json:"binary" doc:"Where the running racksail binary is"`
	// Snapshot labels snapshots of /usr and /var/lib taken first.
	Snapshot string `json:"snapshot,omitempty" doc:"Take read-only snapshots of the system first, under this label"`
	// PanelBackup is the copy of the panel's database the panel made, at
	// PanelBackupPath for the release's tag.
	PanelBackup string `json:"panelBackup,omitempty"`
}

// PanelBackupPath is where the panel keeps its database from before it
// installs a release.
func PanelBackupPath(tag string) string { return PanelBackups + "/" + tag + "/state.db" }

// keptBackup is where the agent keeps the panel's backup for a release.
func keptBackup(tag string) string { return UpdateDir + "/" + tag + "/state.db" }

var (
	tagPattern        = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$`)
	assetPattern      = regexp.MustCompile(`^racksail-linux-(amd64|arm64)$`)
	sha256Hex         = regexp.MustCompile(`^[0-9a-f]{64}$`)
	snapshotLabel     = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
	codenamePattern   = regexp.MustCompile(`^[a-z]{3,16}$`)
	debianSourceFiles = []string{"/etc/apt/sources.list.d/debian.sources", "/etc/apt/sources.list"}
	debianPrefFile    = "/etc/apt/preferences.d/racksail-debian.pref"
)

func checkURLs(field string, urls []string) error {
	if len(urls) == 0 || len(urls) > 8 {
		return fmt.Errorf("give between 1 and 8 %s", field)
	}
	for _, u := range urls {
		if err := CheckURL(u); err != nil {
			return err
		}
	}
	return nil
}

func checkBinary(p string) error {
	for _, b := range Binaries {
		if p == b {
			return nil
		}
	}
	return fmt.Errorf("the racksail binary is at %s or %s, not %s", Binaries[0], Binaries[1], p)
}

// Validate implements agent.Input.
func (in InstallReleaseInput) Validate() error {
	if _, err := ChannelOf(in.Tag); err != nil || !tagPattern.MatchString(in.Tag) {
		return fmt.Errorf("%q isn't a release tag", in.Tag)
	}
	switch {
	case !assetPattern.MatchString(in.Asset):
		return fmt.Errorf("%q isn't a racksail binary", in.Asset)
	case in.Size <= 0 || in.Size > maxBinary:
		return fmt.Errorf("a binary of %d bytes isn't plausible", in.Size)
	case !sha256Hex.MatchString(in.SHA256):
		return errors.New("the checksum isn't a SHA-256 in hex")
	case in.Snapshot != "" && !snapshotLabel.MatchString(in.Snapshot):
		return fmt.Errorf("label %q must be lowercase letters, digits and underscores, starting with a letter", in.Snapshot)
	}
	if in.PanelBackup != "" && in.PanelBackup != PanelBackupPath(in.Tag) {
		return fmt.Errorf("the panel's backup is %s, not %s", PanelBackupPath(in.Tag), in.PanelBackup)
	}
	for field, urls := range map[string][]string{"download URLs": in.URLs, "bundle URLs": in.BundleURLs, "trusted root URLs": in.TrustedRootURLs} {
		if err := checkURLs(field, urls); err != nil {
			return err
		}
	}
	return checkBinary(in.Binary)
}

// InstallReleaseResult is what an install did.
type InstallReleaseResult struct {
	agent.Result
	State *UpdateState `json:"state,omitempty"`
}

// mkdirAll creates a directory and its parents for root alone.
func mkdirAll(ctx context.Context, files system.Files, dir string) error {
	if dir == "/" {
		return nil
	}
	info, err := files.Stat(ctx, dir)
	if err == nil {
		if !info.IsDir() {
			return fmt.Errorf("%s isn't a directory", dir)
		}
		return nil
	}
	if !errors.Is(err, system.ErrNotFound) {
		return err
	}
	if err := mkdirAll(ctx, files, path.Dir(dir)); err != nil {
		return err
	}
	if err := files.Mkdir(ctx, dir, 0o700, system.Owner{}); err != nil && !errors.Is(err, system.ErrExists) {
		return err
	}
	return nil
}

// download fetches the first URL that answers into dest, checking size and
// checksum on the way.
func download(ctx context.Context, files system.Files, urls []string, dest string, size int64, sum string) error {
	var errs []error
	for _, u := range urls {
		err := func() error {
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
			if err != nil {
				return err
			}
			req.Header.Set("User-Agent", "Racksail/"+Build().Version)
			resp, err := releaseClient.Do(req)
			if err != nil {
				return err
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				return fmt.Errorf("%s answered %s", u, resp.Status)
			}
			h := sha256.New()
			counted := &countingReader{r: io.TeeReader(io.LimitReader(resp.Body, size+1), h)}
			if err := files.WriteFrom(ctx, dest, counted, 0o700, system.Owner{}); err != nil {
				return err
			}
			if counted.n != size || hex.EncodeToString(h.Sum(nil)) != sum {
				files.Remove(ctx, dest)
				return fmt.Errorf("what %s served isn't the file the release lists", u)
			}
			return nil
		}()
		if err == nil {
			return nil
		}
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// fetchSmall fetches a small file from the first URL that answers.
func fetchSmall(ctx context.Context, urls []string) ([]byte, error) {
	c := &Client{HTTP: releaseClient}
	return c.fetch(ctx, urls, maxSmall)
}

func readState(ctx context.Context, files system.Files) (*UpdateState, error) {
	data, err := files.ReadFile(ctx, StateFile, maxSmall)
	if err != nil {
		return nil, err
	}
	var st UpdateState
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, fmt.Errorf("reading %s: %w", StateFile, err)
	}
	return &st, nil
}

func writeState(ctx context.Context, files system.Files, st *UpdateState) error {
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	if err := mkdirAll(ctx, files, UpdateDir); err != nil {
		return err
	}
	return files.WriteFile(ctx, StateFile, append(data, '\n'), 0o600, system.Owner{})
}

// copyFile copies a file through the backend, replacing dest atomically.
func copyFile(ctx context.Context, files system.Files, src, dest string, perm fs.FileMode, owner system.Owner) error {
	r, err := files.Open(ctx, src)
	if err != nil {
		return err
	}
	defer r.Close()
	return files.WriteFrom(ctx, dest, r, perm, owner)
}

// panelState is the view of the panel's state the agent works through.
// The panel's user owns that state and could put a symbolic link anywhere
// in it, so the view follows none; the state directory itself sits in a
// directory only root may change.
func panelState(files system.Files) system.Files { return files.Beneath(system.StateDir) }

// keepPanelBackup copies the panel's backup of its database into the
// agent's own state, which the panel can't reach, so a rollback only ever
// puts back what was the panel's database. The backup must be a regular
// file the panel's user owns, reached without following a link.
func keepPanelBackup(ctx context.Context, files system.Files, from, to string) error {
	panel := panelState(files)
	info, err := panel.Stat(ctx, from)
	if err != nil {
		return err
	}
	if !info.Mode.IsRegular() || info.Owner != PanelUser {
		return fmt.Errorf("%s isn't a file the panel made: %w", from, system.ErrInvalid)
	}
	r, err := panel.Open(ctx, from)
	if err != nil {
		return err
	}
	defer r.Close()
	return files.WriteFrom(ctx, to, r, 0o600, system.Owner{})
}

// removeOtherUpdates deletes what the agent kept for earlier updates: their
// downloads and the panel's backups from before them.
func removeOtherUpdates(ctx context.Context, files system.Files, tag string) {
	entries, err := files.ReadDir(ctx, UpdateDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if name := path.Base(e.Path); e.IsDir() && name != tag && tagPattern.MatchString(name) {
			files.RemoveAll(ctx, UpdateDir+"/"+name)
		}
	}
}

// schedule runs a command once, after a delay, as a transient systemd
// unit of its own, so it outlives the agent restarting under it.
func schedule(ctx context.Context, env *agent.Env, unit string, delay time.Duration, args ...string) error {
	cmd := append([]string{
		"--unit=" + unit, "--collect", "--quiet",
		fmt.Sprintf("--on-active=%ds", int(delay.Seconds())), "--timer-property=AccuracySec=1s",
	}, args...)
	_, err := env.System.Exec.Run(ctx, system.Command{Path: "/usr/bin/systemd-run", Args: cmd, Timeout: time.Minute})
	return err
}

// InstallRelease replaces the racksail binary with a release's.
var InstallRelease = agent.Register(agent.Def[InstallReleaseInput, InstallReleaseResult]{
	Name:    "InstallRelease",
	Summary: "Install a Racksail release",
	Description: "Downloads a release's binary, checks its checksum and its Sigstore signature by Racksail's release workflow against the " +
		"trusted root the agent pins, snapshots the system, keeps the running binary and a copy of the panel's database for a rollback, " +
		"puts the new binary in its place and restarts Racksail's services onto it. A guard rolls the release back unless it confirms it " +
		"works within fifteen minutes.",
	Effects: func(in InstallReleaseInput) []system.Effect {
		return installEffects(in)
	},
	Run: func(ctx context.Context, env *agent.Env, in InstallReleaseInput) (InstallReleaseResult, error) {
		files := env.System.Files
		effects := installEffects(in)
		if in.Binary == packageBinary {
			return InstallReleaseResult{}, fmt.Errorf("%s belongs to the Debian package; update it with apt", packageBinary)
		}
		if _, err := files.Stat(ctx, in.Binary); err != nil {
			return InstallReleaseResult{}, fmt.Errorf("the racksail binary isn't at %s: %w", in.Binary, err)
		}
		next, _ := ParseVersion(in.Tag)
		current, err := ParseVersion(Build().Version)
		if err == nil && next.Compare(current) <= 0 {
			return InstallReleaseResult{}, fmt.Errorf("%s is no newer than the running %s", next, current)
		}
		if env.Check {
			return InstallReleaseResult{Result: agent.Changed(effects...)}, nil
		}
		rootJSON, err := fetchSmall(ctx, in.TrustedRootURLs)
		if err != nil {
			return InstallReleaseResult{}, fmt.Errorf("fetching Sigstore's trusted root: %w", err)
		}
		root, err := PinnedRoot(rootJSON)
		if err != nil {
			return InstallReleaseResult{}, err
		}
		bundle, err := fetchSmall(ctx, in.BundleURLs)
		if err != nil {
			return InstallReleaseResult{}, fmt.Errorf("fetching the binary's signature: %w", err)
		}
		dir := UpdateDir + "/" + in.Tag
		if err := mkdirAll(ctx, files, dir); err != nil {
			return InstallReleaseResult{}, err
		}
		staged := dir + "/" + in.Asset
		if err := download(ctx, files, in.URLs, staged, in.Size, in.SHA256); err != nil {
			return InstallReleaseResult{}, fmt.Errorf("downloading %s: %w", in.Asset, err)
		}
		sum, _ := hex.DecodeString(in.SHA256)
		id, err := Identity(in.Tag)
		if err != nil {
			return InstallReleaseResult{}, err
		}
		if _, err := sigstore.Verify(root, bundle, sum, id); err != nil {
			files.Remove(ctx, staged)
			return InstallReleaseResult{}, fmt.Errorf("%s isn't signed by Racksail's release workflow for %s: %w", in.Asset, in.Tag, err)
		}
		st := &UpdateState{
			State: StateInstalled, Tag: in.Tag, From: Build().Version, To: next.String(), Binary: in.Binary,
			InstalledAt: time.Now().UTC(),
		}
		if in.PanelBackup != "" {
			kept := keptBackup(in.Tag)
			if err := keepPanelBackup(ctx, files, in.PanelBackup, kept); err != nil {
				return InstallReleaseResult{}, fmt.Errorf("keeping the panel's database from before the update: %w", err)
			}
			st.PanelBackup = kept
		}
		if in.Snapshot != "" {
			snaps, err := snapshotSystem(ctx, env, in.Snapshot)
			if err != nil {
				return InstallReleaseResult{}, fmt.Errorf("snapshotting the system before updating: %w", err)
			}
			st.Snapshots, st.Unprotected = snaps.Snapshots, snaps.Unprotected
		}
		if err := copyFile(ctx, files, in.Binary, PreviousBinary, 0o700, system.Owner{}); err != nil {
			return InstallReleaseResult{}, fmt.Errorf("keeping the running binary: %w", err)
		}
		if err := writeState(ctx, files, st); err != nil {
			return InstallReleaseResult{}, err
		}
		// Only the last update can be rolled back, so what earlier ones
		// kept goes once this one is recorded.
		removeOtherUpdates(ctx, files, in.Tag)
		if err := copyFile(ctx, files, staged, in.Binary, 0o755, system.Owner{}); err != nil {
			return InstallReleaseResult{}, fmt.Errorf("installing the new binary: %w", err)
		}
		files.Remove(ctx, staged)
		// The guard runs the binary this one replaced, which is known to
		// work, to undo the update unless the new release confirms it.
		if err := schedule(ctx, env, GuardUnit, GuardDelay, PreviousBinary, "update", "guard"); err != nil {
			return InstallReleaseResult{}, fmt.Errorf("arming the rollback: %w", err)
		}
		restart := append([]string{"/usr/bin/systemctl", "try-restart"}, Services...)
		if err := schedule(ctx, env, RestartUnit, 3*time.Second, restart...); err != nil {
			return InstallReleaseResult{}, fmt.Errorf("restarting onto the new binary: %w", err)
		}
		return InstallReleaseResult{Result: agent.Changed(effects...), State: st}, nil
	},
})

func installEffects(in InstallReleaseInput) []system.Effect {
	effects := []system.Effect{{Type: system.EffectDownload, Action: "download", Target: UpdateDir + "/" + in.Tag + "/" + in.Asset, Source: firstOf(in.URLs), DownloadBytes: in.Size}}
	if in.Snapshot != "" {
		effects = append(effects, system.Effect{Type: system.EffectStorage, Action: "snapshot", Target: "/usr", Detail: in.Snapshot},
			system.Effect{Type: system.EffectStorage, Action: "snapshot", Target: "/var/lib", Detail: in.Snapshot})
	}
	if in.PanelBackup != "" {
		effects = append(effects, system.Effect{Type: system.EffectFile, Action: "write", Target: keptBackup(in.Tag), Detail: "the panel's database from before the update"})
	}
	effects = append(effects, system.Effect{Type: system.EffectFile, Action: "write", Target: in.Binary, Detail: in.Tag, DiskBytes: in.Size * 2})
	for _, s := range Services {
		effects = append(effects, system.Effect{Type: system.EffectService, Action: "restart", Target: s})
	}
	return effects
}

func firstOf(list []string) string {
	if len(list) == 0 {
		return ""
	}
	return list[0]
}

// snapshotSystem snapshots /usr and /var/lib through the agent's own
// operation, which knows where snapshots go and what Btrfs holds.
func snapshotSystem(ctx context.Context, env *agent.Env, label string) (agent.SnapshotResult, error) {
	var out agent.SnapshotResult
	op, ok := agent.Lookup(agent.SnapshotPaths.Name())
	if !ok {
		return out, errors.New("the agent can't take snapshots")
	}
	in, err := json.Marshal(agent.SnapshotPathsInput{Paths: []string{"/usr", "/var/lib"}, Label: label})
	if err != nil {
		return out, err
	}
	raw, err := op.Invoke(ctx, env, in)
	if err != nil {
		return out, err
	}
	return out, json.Unmarshal(raw, &out)
}

// ReleaseState reports what the agent last did to the racksail binary.
var ReleaseState = agent.Register(agent.Def[agent.Empty, UpdateStateOutput]{
	Name: "ReleaseState", Summary: "Report the last update", ReadOnly: true,
	Run: func(ctx context.Context, env *agent.Env, _ agent.Empty) (UpdateStateOutput, error) {
		st, err := readState(ctx, env.System.Files)
		if errors.Is(err, system.ErrNotFound) {
			return UpdateStateOutput{}, nil
		}
		if err != nil {
			return UpdateStateOutput{}, err
		}
		_, prevErr := env.System.Files.Stat(ctx, PreviousBinary)
		return UpdateStateOutput{State: st, CanRollBack: prevErr == nil && st.State != StateRolledBack}, nil
	},
})

// UpdateStateOutput is the last update, if any.
type UpdateStateOutput struct {
	State       *UpdateState `json:"state,omitempty"`
	CanRollBack bool         `json:"canRollBack" doc:"Whether the binary the update replaced is still kept"`
}

// ConfirmReleaseInput names the release that works.
type ConfirmReleaseInput struct {
	Tag string `json:"tag"`
}

// Validate implements agent.Input.
func (in ConfirmReleaseInput) Validate() error {
	if !tagPattern.MatchString(in.Tag) {
		return fmt.Errorf("%q isn't a release tag", in.Tag)
	}
	return nil
}

// ConfirmRelease records that a new release works, which disarms the
// guard. The binary it replaced is kept for a rollback by hand.
var ConfirmRelease = agent.Register(agent.Def[ConfirmReleaseInput, agent.Result]{
	Name: "ConfirmRelease", Summary: "Confirm an update works",
	Effects: func(ConfirmReleaseInput) []system.Effect {
		return []system.Effect{{Type: system.EffectService, Action: "stop", Target: GuardUnit + ".timer"}}
	},
	Run: func(ctx context.Context, env *agent.Env, in ConfirmReleaseInput) (agent.Result, error) {
		st, err := readState(ctx, env.System.Files)
		if err != nil {
			return agent.Result{}, fmt.Errorf("there is no update to confirm: %w", err)
		}
		if st.Tag != in.Tag || st.State != StateInstalled {
			return agent.Unchanged(), nil
		}
		effect := system.Effect{Type: system.EffectService, Action: "stop", Target: GuardUnit + ".timer"}
		if env.Check {
			return agent.Changed(effect), nil
		}
		if err := env.System.Units.Stop(ctx, GuardUnit+".timer"); err != nil && !errors.Is(err, system.ErrNotFound) {
			return agent.Result{}, fmt.Errorf("disarming the rollback: %w", err)
		}
		now := time.Now().UTC()
		st.State, st.ConfirmedAt = StateConfirmed, &now
		return agent.Changed(effect), writeState(ctx, env.System.Files, st)
	},
})

// RollbackReleaseInput says why a release goes.
type RollbackReleaseInput struct {
	Reason string `json:"reason" maxLength:"500"`
}

// Validate implements agent.Input.
func (in RollbackReleaseInput) Validate() error {
	if len(in.Reason) > 500 {
		return errors.New("the reason is too long")
	}
	return nil
}

// RollbackRelease puts the binary the last update replaced back, with the
// panel's database from before it, through the guard, which stops the
// panel first.
var RollbackRelease = agent.Register(agent.Def[RollbackReleaseInput, agent.Result]{
	Name: "RollbackRelease", Summary: "Roll back the last update",
	Effects: func(RollbackReleaseInput) []system.Effect {
		return rollbackEffects("")
	},
	Run: func(ctx context.Context, env *agent.Env, in RollbackReleaseInput) (agent.Result, error) {
		st, err := readState(ctx, env.System.Files)
		if err != nil {
			return agent.Result{}, fmt.Errorf("there is no update to roll back: %w", err)
		}
		if st.State == StateRolledBack {
			return agent.Unchanged(), nil
		}
		if _, err := env.System.Files.Stat(ctx, PreviousBinary); err != nil {
			return agent.Result{}, fmt.Errorf("the binary the update replaced is gone: %w", err)
		}
		effects := rollbackEffects(st.Binary)
		if env.Check {
			return agent.Changed(effects...), nil
		}
		reason := in.Reason
		if reason == "" {
			reason = "rolled back by an admin"
		}
		st.Reason = reason
		if err := writeState(ctx, env.System.Files, st); err != nil {
			return agent.Result{}, err
		}
		env.System.Units.Stop(ctx, GuardUnit+".timer")
		if err := schedule(ctx, env, GuardUnit+"-now", time.Second, PreviousBinary, "update", "guard", "--now"); err != nil {
			return agent.Result{}, fmt.Errorf("starting the rollback: %w", err)
		}
		return agent.Changed(effects...), nil
	},
})

func rollbackEffects(binary string) []system.Effect {
	if binary == "" {
		binary = Binaries[0]
	}
	effects := []system.Effect{
		{Type: system.EffectService, Action: "stop", Target: "racksail-panel.service"},
		{Type: system.EffectFile, Action: "write", Target: binary, Detail: "the binary from before the update"},
		{Type: system.EffectFile, Action: "write", Target: system.StateDir + "/state.db", Detail: "the panel's database from before the update"},
	}
	for _, s := range Services {
		effects = append(effects, system.Effect{Type: system.EffectService, Action: "restart", Target: s})
	}
	return effects
}

// Guard rolls back an update the new release never confirmed, or, with
// now, the one an admin rolls back. It runs as root from the binary the
// update replaced, as a transient unit: it stops the panel, puts back its
// database and the binary, and starts Racksail's services again. It
// reports whether it rolled anything back.
func Guard(ctx context.Context, sys *system.Backend, now bool) (bool, error) {
	files := sys.Files
	st, err := readState(ctx, files)
	if errors.Is(err, system.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if st.State != StateInstalled {
		return false, nil
	}
	if !now && st.Reason == "" {
		st.Reason = fmt.Sprintf("%s didn't confirm it works within %s", st.To, GuardDelay)
	}
	if err := sys.Units.Stop(ctx, "racksail-panel.service"); err != nil && !errors.Is(err, system.ErrNotFound) {
		return false, fmt.Errorf("stopping the panel: %w", err)
	}
	if st.PanelBackup != "" {
		// Only a copy the agent kept goes back, and into the panel's state
		// without following a link the panel's user may have left there.
		if st.PanelBackup != keptBackup(st.Tag) {
			return false, fmt.Errorf("%s isn't the panel's database the agent kept before the update", st.PanelBackup)
		}
		panel := panelState(files)
		db := system.StateDir + "/state.db"
		r, err := files.Open(ctx, st.PanelBackup)
		if err != nil {
			return false, fmt.Errorf("putting back the panel's database: %w", err)
		}
		err = panel.WriteFrom(ctx, db, r, 0o640, system.Owner{User: PanelUser, Group: PanelUser})
		r.Close()
		if err != nil {
			return false, fmt.Errorf("putting back the panel's database: %w", err)
		}
		for _, suffix := range []string{"-wal", "-shm"} {
			if err := panel.Remove(ctx, db+suffix); err != nil && !errors.Is(err, system.ErrNotFound) {
				return false, err
			}
		}
	}
	if err := copyFile(ctx, files, PreviousBinary, st.Binary, 0o755, system.Owner{}); err != nil {
		return false, fmt.Errorf("putting back the binary: %w", err)
	}
	at := time.Now().UTC()
	st.State, st.RolledBackAt = StateRolledBack, &at
	if err := writeState(ctx, files, st); err != nil {
		return false, err
	}
	for _, s := range Services[:2] {
		if err := sys.Units.Restart(ctx, s); err != nil {
			return true, fmt.Errorf("restarting %s: %w", s, err)
		}
	}
	if edge, err := sys.Units.Status(ctx, Services[2]); err == nil && edge.Active() {
		if err := sys.Units.Restart(ctx, Services[2]); err != nil {
			return true, err
		}
	}
	return true, nil
}

// SetDebianReleaseInput moves apt from one Debian release to the next.
type SetDebianReleaseInput struct {
	From string `json:"from" doc:"The code name of the release the server runs, such as trixie"`
	To   string `json:"to" doc:"The code name of the release to move to, such as forky"`
}

// Validate implements agent.Input.
func (in SetDebianReleaseInput) Validate() error {
	if !codenamePattern.MatchString(in.From) || !codenamePattern.MatchString(in.To) || in.From == in.To {
		return errors.New("give two different Debian code names")
	}
	return nil
}

// suiteWord matches a suite of the release, such as trixie-security, as a
// whole word.
func suiteWord(codename string) *regexp.Regexp {
	return regexp.MustCompile(`(^|[\s/=])` + codename + `(-updates|-security|-backports|-proposed-updates)?(\s|$)`)
}

// moveRelease rewrites the suites of one release in an apt file.
func moveRelease(content []byte, from, to string) []byte {
	re := suiteWord(from)
	var out bytes.Buffer
	for _, line := range strings.SplitAfter(string(content), "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "#"):
		case strings.HasPrefix(trimmed, "Suites:"), strings.HasPrefix(trimmed, "deb ") || strings.HasPrefix(trimmed, "deb-src "),
			strings.HasPrefix(trimmed, "Pin: release"):
			// Replacing twice catches suites next to each other, which
			// share the space between them.
			for range 2 {
				line = re.ReplaceAllString(line, "${1}"+to+"${2}${3}")
			}
		}
		out.WriteString(line)
	}
	return out.Bytes()
}

// SetDebianRelease points apt at the next Debian release: the suites of
// Debian's own sources and Racksail's pin that keeps a server on its
// release. Each file it changes is kept as it was, beside it, for a
// rollback.
var SetDebianRelease = agent.Register(agent.Def[SetDebianReleaseInput, agent.Result]{
	Name: "SetDebianRelease", Summary: "Point apt at another Debian release",
	Description: "Rewrites the suites in Debian's apt sources and in Racksail's release pin from one Debian release to another, " +
		"keeping each file as it was beside it with a .racksail-<release> suffix.",
	Effects: func(in SetDebianReleaseInput) []system.Effect {
		var out []system.Effect
		for _, f := range append(slicesClone(debianSourceFiles), debianPrefFile) {
			out = append(out, system.Effect{Type: system.EffectFile, Action: "write", Target: f, Detail: in.From + " to " + in.To})
		}
		return out
	},
	Run: func(ctx context.Context, env *agent.Env, in SetDebianReleaseInput) (agent.Result, error) {
		files := env.System.Files
		var effects []system.Effect
		type change struct {
			path      string
			old, next []byte
			perm      fs.FileMode
		}
		var changes []change
		for _, f := range append(slicesClone(debianSourceFiles), debianPrefFile) {
			info, err := files.Stat(ctx, f)
			if errors.Is(err, system.ErrNotFound) {
				continue
			}
			if err != nil {
				return agent.Result{}, err
			}
			old, err := files.ReadFile(ctx, f, 1<<20)
			if err != nil {
				return agent.Result{}, err
			}
			next := moveRelease(old, in.From, in.To)
			if bytes.Equal(old, next) {
				continue
			}
			changes = append(changes, change{f, old, next, info.Perm()})
			effects = append(effects, system.Effect{Type: system.EffectFile, Action: "write", Target: f, Detail: in.From + " to " + in.To})
		}
		if len(changes) == 0 {
			return agent.Unchanged(), nil
		}
		if env.Check {
			return agent.Changed(effects...), nil
		}
		for _, c := range changes {
			if err := files.WriteFile(ctx, c.path+".racksail-"+in.From, c.old, c.perm, system.Owner{}); err != nil {
				return agent.Result{}, err
			}
			if err := files.WriteFile(ctx, c.path, c.next, c.perm, system.Owner{}); err != nil {
				return agent.Result{}, err
			}
		}
		return agent.Changed(effects...), nil
	},
})

func slicesClone(s []string) []string { return append([]string(nil), s...) }
