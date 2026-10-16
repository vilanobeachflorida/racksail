package updates

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/agent"
	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/changeset"
	"github.com/vilanobeachflorida/racksail/internal/events"
	"github.com/vilanobeachflorida/racksail/internal/hooks"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/jobs"
	"github.com/vilanobeachflorida/racksail/internal/reconcile"
	"github.com/vilanobeachflorida/racksail/internal/secrets"
	"github.com/vilanobeachflorida/racksail/internal/store"
	"github.com/vilanobeachflorida/racksail/internal/system"
	"github.com/vilanobeachflorida/racksail/internal/system/sim"
	"github.com/vilanobeachflorida/racksail/internal/updates/sigstore/sigstoretest"
)

func TestChannelOf(t *testing.T) {
	for v, want := range map[string]Channel{
		"v1.2.3": Stable, "1.3.0-beta.1": Beta, "1.3.0-rc.2": Beta,
		"1.3.0-alpha.nightly.20261015.42": Nightly, "1.3.0-rc.1.nightly.20261015.7": Nightly,
	} {
		if got, err := ChannelOf(v); err != nil || got != want {
			t.Errorf("ChannelOf(%s) = %s, %v", v, got, err)
		}
	}
	for _, bad := range []string{"1.2", "1.3.0-alpha", "1.2.3+build", "1.3.0-preview.1"} {
		if _, err := ChannelOf(bad); err == nil {
			t.Errorf("%s isn't a release", bad)
		}
	}
	id, _ := Identity("v1.2.3")
	if id.Subject != Workflow+"@refs/tags/v1.2.3" || id.Issuer != Issuer {
		t.Errorf("identity = %+v", id)
	}
	if id, _ := Identity("v1.3.0-alpha.nightly.20261015.42"); id.Subject != Workflow+"@refs/heads/main" {
		t.Errorf("nightly builds are signed from main: %+v", id)
	}
}

// The panel checks releases against the trusted root install.sh pins, so
// a server installed with the script keeps trusting what it trusted.
func TestPinMatchesInstallScript(t *testing.T) {
	script, err := os.ReadFile("../../packaging/install.sh")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^SIGSTORE_ROOT_SHA256=([0-9a-f]{64})$`).FindSubmatch(script)
	if m == nil || string(m[1]) != SigstoreRootSHA256 {
		t.Errorf("install.sh pins %s; internal/updates pins %s", m, SigstoreRootSHA256)
	}
}

func TestWindow(t *testing.T) {
	night := &Window{Start: "23:00", End: "02:00", TimeZone: "UTC"}
	for at, want := range map[string]bool{"23:30": true, "01:59": true, "02:00": false, "12:00": false} {
		ts, _ := time.Parse("15:04", at)
		if got := night.Open(ts); got != want {
			t.Errorf("%s: %v", at, got)
		}
	}
	var none *Window
	if !none.Open(time.Now()) {
		t.Error("no window means any time")
	}
}

// release is a published release served by a fake GitHub.
type release struct {
	tag    string
	binary []byte
}

// The tests' own Sigstore signs the releases they publish, and the agent
// trusts its root as it trusts Sigstore's.
var (
	testSigstore = sigstoretest.New()
	testRootJSON = testSigstore.TrustedRoot()
	testRoot     = func() string { sum := sha256.Sum256(testRootJSON); return hex.EncodeToString(sum[:]) }()
)

func init() { TrustRoot(testRoot) }

// github serves releases, their manifests and Sigstore's trusted root, to
// the panel over HTTP and to the agents of the servers attached to it as
// their simulated downloads.
type github struct {
	t        *testing.T
	srv      *httptest.Server
	sig      *sigstoretest.Sigstore
	root     []byte
	mu       sync.Mutex
	files    map[string][]byte
	tags     []string
	fetched  []string
	machines []*sim.Machine
}

// attach lets a simulated server's agent download what GitHub serves.
func (g *github) attach(m *sim.Machine) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.machines = append(g.machines, m)
	m.SetDownload(g.srv.URL+"/tuf/targets/"+testRoot+".trusted_root.json", g.root)
	for p, data := range g.files {
		m.SetDownload(g.srv.URL+p, data)
	}
}

// put serves data at a path. The caller holds g.mu.
func (g *github) put(p string, data []byte) {
	g.files[p] = data
	for _, m := range g.machines {
		m.SetDownload(g.srv.URL+p, data)
	}
}

func newGitHub(t *testing.T, releases ...release) *github {
	g := &github{t: t, sig: testSigstore, root: testRootJSON, files: map[string][]byte{}}
	g.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		defer g.mu.Unlock()
		g.fetched = append(g.fetched, r.URL.Path)
		switch {
		case r.URL.Path == "/api/repos/"+Repository+"/releases":
			var list []map[string]any
			for _, tag := range g.tags {
				list = append(list, map[string]any{"tag_name": tag, "draft": false})
			}
			list = append(list, map[string]any{"tag_name": "v9.9.9", "draft": true})
			json.NewEncoder(w).Encode(list)
		case r.URL.Path == "/tuf/targets/"+testRoot+".trusted_root.json":
			w.Write(g.root)
		default:
			data, ok := g.files[r.URL.Path]
			if !ok {
				http.NotFound(w, r)
				return
			}
			w.Write(data)
		}
	}))
	t.Cleanup(g.srv.Close)
	for _, r := range releases {
		g.publish(r.tag, r.binary)
	}
	return g
}

// publish signs and serves a release, as the release workflow does.
func (g *github) publish(tag string, binary []byte) {
	g.mu.Lock()
	defer g.mu.Unlock()
	id, err := Identity(tag)
	if err != nil {
		g.t.Fatal(err)
	}
	ch, _ := ChannelOf(tag)
	base := "/dl/" + tag + "/"
	rel := Release{Tag: tag, Channel: ch, Published: time.Now().UTC().Truncate(time.Second)}
	for _, arch := range []string{"amd64", "arm64"} {
		name := "racksail-linux-" + arch
		sum := sha256.Sum256(binary)
		g.put(base+name, binary)
		g.put(base+name+BundleSuffix, g.sig.Sign(binary, id.Subject, id.Issuer, sigstoretest.Options{}))
		rel.Assets = append(rel.Assets, Asset{Name: name, URL: g.srv.URL + base + name, Size: int64(len(binary)), SHA256: hex.EncodeToString(sum[:]), Bundle: g.srv.URL + base + name + BundleSuffix})
	}
	manifest, _ := json.Marshal(rel)
	g.put(base+ManifestFile, manifest)
	g.put(base+ManifestFile+BundleSuffix, g.sig.Sign(manifest, id.Subject, id.Issuer, sigstoretest.Options{}))
	g.tags = append(g.tags, tag)
}

func (g *github) client(mirrors []string) *Client {
	return &Client{HTTP: g.srv.Client(), API: g.srv.URL + "/api", Downloads: g.srv.URL + "/dl", TUF: g.srv.URL + "/tuf", Mirrors: mirrors, Root: testRoot}
}

// server is a panel and the simulated server it updates.
type server struct {
	machine *sim.Machine
	agent   agent.Client
	store   store.Store
	log     *events.Log
	engine  *changeset.Engine
	jobs    *jobs.Manager
	hooks   *hooks.Registry
	updater *Updater
	admin   *auth.Actor
	state   string
	// backups are where the panel backed up its database.
	backups []string
}

const binaryPath = "/usr/local/bin/racksail"

func newServer(t *testing.T, g *github, version string, configure func(*Options)) *server {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	log := slog.New(slog.DiscardHandler)
	s := &server{machine: sim.New(sim.Options{}), state: t.TempDir(), hooks: hooks.NewRegistry(),
		admin: &auth.Actor{Type: auth.ActorUser, ID: store.NewID("user"), Source: auth.SourceUI, Grants: []auth.Grant{{Role: auth.RoleAdmin, Scope: store.ServerScope()}}}}
	g.attach(s.machine)
	files := s.machine.Backend().Files
	for _, dir := range []string{"/usr/local/bin", "/etc/apt/sources.list.d", "/etc/apt/preferences.d"} {
		if err := mkdirAll(ctx, files, dir); err != nil {
			t.Fatal(err)
		}
	}
	if err := files.WriteFile(ctx, binaryPath, []byte("racksail "+version), 0o755, system.Owner{}); err != nil {
		t.Fatal(err)
	}
	s.machine.SetProgram("/usr/bin/systemd-run", func(ctx context.Context, m *sim.Machine, cmd system.Command) (system.Output, error) {
		var unit string
		for _, a := range cmd.Args {
			if v, ok := strings.CutPrefix(a, "--unit="); ok {
				unit = v
			}
		}
		m.Log(unit+".service", 6, "scheduled: "+strings.Join(cmd.Args, " "))
		return system.Output{}, nil
	})
	s.agent = agent.NewLocal(s.machine.Backend(), log)
	kinds := store.NewKindRegistry()
	if err := kinds.Register(Kind); err != nil {
		t.Fatal(err)
	}
	s.hooks.DeclareKind(Kind)
	st := store.NewMemory(kinds)
	s.store = st
	s.log = events.NewMemory(1000)
	s.jobs = jobs.NewMemory(ctx, jobs.MemoryOptions{Workers: 2, Log: log})
	t.Cleanup(func() { s.jobs.Stop(context.Background()) })
	key, _ := secrets.NewKey()
	box, _ := secrets.NewBox(key)
	vault := secrets.NewMemory(box)
	runner := &reconcile.Runner{
		Store: st, Agent: s.agent, Registry: reconcile.NewRegistry(), Secrets: vault, Events: s.log, Log: log,
		Facts: func(ctx context.Context, server store.ID) (system.MachineFacts, error) {
			return agent.GetFacts.Call(ctx, s.agent, agent.Empty{}, agent.CallOptions{})
		},
	}
	hookRunner := &hooks.Runner{Registry: s.hooks, Events: s.log, Audit: s.log, Log: log}
	var err error
	s.engine, err = changeset.NewEngine(changeset.Options{
		Store: st, Kinds: kinds, Runner: runner, Jobs: s.jobs, Events: s.log, Audit: s.log, Secrets: vault,
		Hooks: hookRunner, Authorizer: auth.NewRoleAuthorizer(nil), Log: log,
	})
	if err != nil {
		t.Fatal(err)
	}
	opts := Options{
		Store: st, Agent: s.agent, Events: s.log, Hooks: hookRunner, Channel: Stable, StateDir: s.state, Binary: binaryPath,
		Submit: func(ctx context.Context, req changeset.Request) (*changeset.ChangeSet, error) {
			return s.engine.Submit(ctx, req)
		},
		Backup: func(ctx context.Context, dest string) error {
			s.backups = append(s.backups, dest)
			return os.WriteFile(dest, []byte("database"), 0o600)
		},
		Health: func(ctx context.Context) error { return nil },
		Build:  BuildInfo{Version: version, Channel: "stable"}, Arch: "amd64", Logger: log, ConfirmWithin: 200 * time.Millisecond,
		NewClient: g.client,
	}
	if configure != nil {
		configure(&opts)
	}
	s.updater = NewUpdater(opts)
	if err := runner.Registry.Register(s.updater.Reconciler()); err != nil {
		t.Fatal(err)
	}
	s.jobs.Start()
	if err := s.updater.Boot(ctx); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the settings exist", func() bool {
		set, _ := s.updater.Settings(ctx)
		return set != nil
	})
	return s
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("never: %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// action runs an action on the settings through the change pipeline.
func (s *server) action(t *testing.T, name string, input any) (*changeset.ChangeSet, error) {
	t.Helper()
	ctx := context.Background()
	set, err := s.updater.Settings(ctx)
	if err != nil || set == nil {
		t.Fatalf("settings: %v", err)
	}
	var raw json.RawMessage
	if input != nil {
		raw, _ = json.Marshal(input)
	}
	cs, err := s.engine.Submit(ctx, changeset.Request{Actor: s.admin, Changes: []changeset.Change{{Op: changeset.OpAction, Kind: Kind.Name(), ID: set.ID, Action: name, Input: raw}}})
	if err != nil {
		return nil, err
	}
	wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if _, err := s.jobs.Wait(wctx, cs.Job); err != nil {
		t.Fatal(err)
	}
	return s.engine.Get(ctx, cs.ID)
}

func (s *server) read(t *testing.T, p string) string {
	t.Helper()
	data, err := s.machine.Backend().Files.ReadFile(context.Background(), p, 1<<20)
	if err != nil {
		t.Fatalf("reading %s: %v", p, err)
	}
	return string(data)
}

func (s *server) eventTypes() []string {
	evs, _, _ := s.log.QueryEvents(context.Background(), events.Query{Limit: 100})
	var out []string
	for _, e := range evs {
		out = append(out, e.Type)
	}
	return out
}

func TestUpdateConfirmAndRollBack(t *testing.T) {
	ctx := context.Background()
	g := newGitHub(t, release{"v1.2.0", []byte("racksail 1.2.0")}, release{"v1.3.0", []byte("racksail 1.3.0")}, release{"v1.4.0-beta.1", []byte("racksail beta")})
	s := newServer(t, g, "1.2.0", nil)
	told := make(chan string, 4)
	for _, p := range []hooks.Point{hooks.CoreUpdateBefore, hooks.CoreUpdateAfter} {
		err := s.hooks.Register(hooks.Registration{Point: p, Kind: hooks.Observe, Owner: "module:mail", OnObserve: func(ctx context.Context, c *hooks.Context) {
			d := c.Data.(UpdateHookData)
			told <- string(c.Point) + " " + d.From + " " + d.To
		}})
		if err != nil {
			t.Fatal(err)
		}
	}

	cs, err := s.action(t, "check", nil)
	if err != nil {
		t.Fatal(err)
	}
	var found CheckOutput
	if err := json.Unmarshal(cs.Changes[0].Output, &found); err != nil || found.Available == nil || found.Available.Tag != "v1.3.0" {
		t.Fatalf("check = %s, %v: a stable server is offered the newest stable release", cs.Changes[0].Output, err)
	}
	if !strings.Contains(strings.Join(s.eventTypes(), ","), EventFound.Name) {
		t.Error("an available release is announced")
	}

	// A preview shows what the update does without doing it.
	preview, err := s.engine.Submit(ctx, changeset.Request{Actor: s.admin, DryRun: true, Changes: []changeset.Change{{Op: changeset.OpAction, Kind: Kind.Name(), ID: found.ID(t, s), Action: "install"}}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(mustJSON(preview)), "racksail-linux-amd64") || s.read(t, binaryPath) != "racksail 1.2.0" {
		t.Errorf("the preview lists the download and changes nothing: %s", mustJSON(preview))
	}

	cs, err = s.action(t, "install", UpdateInput{Version: "1.3.0"})
	if err != nil {
		t.Fatal(err)
	}
	if cs.State != changeset.StateApplied {
		t.Fatalf("update: %s %s", cs.State, mustJSON(cs.Error))
	}
	if got := s.read(t, binaryPath); got != "racksail 1.3.0" {
		t.Errorf("binary = %q", got)
	}
	if got := s.read(t, PreviousBinary); got != "racksail 1.2.0" {
		t.Errorf("the binary it replaced is kept: %q", got)
	}
	var st UpdateState
	json.Unmarshal([]byte(s.read(t, StateFile)), &st)
	if st.State != StateInstalled || st.Tag != "v1.3.0" || st.Binary != binaryPath {
		t.Errorf("state = %+v", st)
	}
	if len(s.backups) != 1 || s.backups[0] != filepath.Join(s.state, "updates", "v1.3.0", "state.db") {
		t.Errorf("the panel's database is backed up first: %v", s.backups)
	}
	if _, err := os.Stat(s.backups[0]); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the panel's own backup is gone once the agent was asked: %v", err)
	}
	journal := s.journal(t)
	if !strings.Contains(journal, "--unit="+GuardUnit) || !strings.Contains(journal, "try-restart racksail-agent.service") {
		t.Errorf("the guard is armed and the services restart: %s", journal)
	}

	// The panel restarts onto 1.3.0, reports healthy and confirms.
	restarted := s.restartAs(t, g, "1.3.0", nil)
	if err := restarted.updater.Confirm(ctx); err != nil {
		t.Fatal(err)
	}
	json.Unmarshal([]byte(s.read(t, StateFile)), &st)
	if st.State != StateConfirmed {
		t.Errorf("state = %+v", st)
	}
	if !strings.Contains(strings.Join(s.eventTypes(), ","), EventInstalled.Name) {
		t.Error("the update is announced once it works")
	}
	// Modules hear about the update before and after.
	// The agent records the version of the binary it replaced, its own.
	for _, want := range []string{"core.update.before 1.2.0 1.3.0", "core.update.after " + Build().Version + " 1.3.0"} {
		select {
		case got := <-told:
			if got != want {
				t.Errorf("hook got %q, want %q", got, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("no %s", want)
		}
	}
	// The guard finds nothing to do.
	if rolled, err := Guard(ctx, s.machine.Backend(), false); err != nil || rolled {
		t.Errorf("guard after a confirmed update: %v, %v", rolled, err)
	}

	// An admin rolls it back by hand: the guard runs at once.
	cs, err = s.action(t, "rollback", RollbackInput{Reason: "testing"})
	if err != nil || cs.State != changeset.StateApplied {
		t.Fatalf("rollback: %v %s", err, mustJSON(cs))
	}
	if !strings.Contains(s.journal(t), "guard --now") {
		t.Error("a rollback runs the guard from the previous binary")
	}
}

func (o CheckOutput) ID(t *testing.T, s *server) store.ID {
	set, _ := s.updater.Settings(context.Background())
	return set.ID
}

func (s *server) journal(t *testing.T) string {
	page, err := s.machine.Backend().Journal.Read(context.Background(), system.JournalQuery{Limit: 1000})
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, e := range page.Entries {
		lines = append(lines, e.Message)
	}
	return strings.Join(lines, "\n")
}

// restartAs is the same server's panel started again on another version.
func (s *server) restartAs(t *testing.T, g *github, version string, health func(context.Context) error) *server {
	opts := s.updater.opts
	opts.Build = BuildInfo{Version: version, Channel: "stable"}
	if health != nil {
		opts.Health = health
	}
	return &server{machine: s.machine, agent: s.agent, store: s.store, log: s.log, engine: s.engine, jobs: s.jobs, updater: NewUpdater(opts), admin: s.admin, state: s.state}
}

func TestUnhealthyReleaseRollsBack(t *testing.T) {
	ctx := context.Background()
	g := newGitHub(t, release{"v1.3.0", []byte("racksail 1.3.0")})
	s := newServer(t, g, "1.2.0", nil)
	if _, err := s.action(t, "check", nil); err != nil {
		t.Fatal(err)
	}
	if cs, err := s.action(t, "install", nil); err != nil || cs.State != changeset.StateApplied {
		t.Fatalf("update: %v %s", err, mustJSON(cs))
	}
	sick := s.restartAs(t, g, "1.3.0", func(context.Context) error { return errors.New("the database won't open") })
	if err := sick.updater.Confirm(ctx); err != nil {
		t.Fatal(err)
	}
	var st UpdateState
	json.Unmarshal([]byte(s.read(t, StateFile)), &st)
	if !strings.Contains(st.Reason, "the database won't open") {
		t.Errorf("the reason is recorded: %+v", st)
	}
	// The guard, run from the previous binary, puts it back.
	rolled, err := Guard(ctx, s.machine.Backend(), true)
	if err != nil || !rolled {
		t.Fatalf("guard: %v, %v", rolled, err)
	}
	if got := s.read(t, binaryPath); got != "racksail 1.2.0" {
		t.Errorf("binary after the rollback = %q", got)
	}
	json.Unmarshal([]byte(s.read(t, StateFile)), &st)
	if st.State != StateRolledBack || st.RolledBackAt == nil {
		t.Errorf("state = %+v", st)
	}
	// The old panel, started again, says what happened.
	old := s.restartAs(t, g, "1.2.0", nil)
	if err := old.updater.Confirm(ctx); err != nil {
		t.Fatal(err)
	}
	if err := old.updater.Confirm(ctx); err != nil {
		t.Fatal(err)
	}
	n := strings.Count(strings.Join(s.eventTypes(), ","), EventRolledBack.Name)
	if n != 1 {
		t.Errorf("the rollback is announced once, not %d times", n)
	}
}

func TestInstallRefusals(t *testing.T) {
	ctx := context.Background()
	g := newGitHub(t, release{"v1.3.0", []byte("racksail 1.3.0")})
	s := newServer(t, g, "1.2.0", nil)
	if _, err := s.action(t, "check", nil); err != nil {
		t.Fatal(err)
	}
	set, _ := s.updater.Settings(ctx)
	in := s.updater.installInput(set.Spec, set.Status.Available, binaryPath, "")

	// What a mirror or a man in the middle serves must be what was signed.
	g.mu.Lock()
	good := g.files["/dl/v1.3.0/racksail-linux-amd64"]
	g.put("/dl/v1.3.0/racksail-linux-amd64", []byte("racksail evil"))
	g.mu.Unlock()
	if _, err := InstallRelease.Call(ctx, s.agent, in, agent.CallOptions{}); err == nil || !strings.Contains(err.Error(), "isn't the file the release lists") {
		t.Errorf("a tampered binary is refused: %v", err)
	}
	if got := s.read(t, binaryPath); got != "racksail 1.2.0" {
		t.Errorf("nothing was installed: %q", got)
	}
	// A binary signed by someone else, with a checksum to match, is
	// refused too.
	evil := []byte("racksail evil")
	sum := sha256.Sum256(evil)
	other := sigstoretest.New()
	id, _ := Identity("v1.3.0")
	g.mu.Lock()
	g.put("/dl/v1.3.0/racksail-linux-amd64", evil)
	g.put("/dl/v1.3.0/racksail-linux-amd64"+BundleSuffix, other.Sign(evil, id.Subject, id.Issuer, sigstoretest.Options{}))
	g.mu.Unlock()
	forged := in
	forged.SHA256, forged.Size = hex.EncodeToString(sum[:]), int64(len(evil))
	if _, err := InstallRelease.Call(ctx, s.agent, forged, agent.CallOptions{}); err == nil || !strings.Contains(err.Error(), "isn't signed by Racksail's release workflow") {
		t.Errorf("a binary signed elsewhere is refused: %v", err)
	}
	g.mu.Lock()
	g.put("/dl/v1.3.0/racksail-linux-amd64", good)
	g.mu.Unlock()
	// Signed by the workflow, but for another tag.
	wrongTag := in
	wrongTag.Tag = "v1.3.1"
	if _, err := InstallRelease.Call(ctx, s.agent, wrongTag, agent.CallOptions{}); err == nil {
		t.Error("a binary signed for another release is refused")
	}
	pkg := in
	pkg.Binary = "/usr/bin/racksail"
	if _, err := InstallRelease.Call(ctx, s.agent, pkg, agent.CallOptions{}); err == nil || !strings.Contains(err.Error(), "apt") {
		t.Errorf("the Debian package's binary is left to apt: %v", err)
	}
	bad := in
	bad.Binary = "/usr/bin/bash"
	if _, err := InstallRelease.Call(ctx, s.agent, bad, agent.CallOptions{}); err == nil {
		t.Error("only the racksail binary can be replaced")
	}
	// A release that isn't newer isn't installed.
	if _, err := s.action(t, "install", UpdateInput{Version: "1.3.1"}); err == nil || !strings.Contains(err.Error(), "found 1.3.0") {
		t.Errorf("a version the last check didn't find is refused: %v", err)
	}
	dev := newServer(t, g, "0.0.0-dev", func(o *Options) { o.Build = BuildInfo{Version: "0.0.0-dev", Channel: "dev"} })
	if _, err := dev.updater.Check(ctx); !errors.Is(err, ErrDevBuild) {
		t.Errorf("development builds don't update: %v", err)
	}
}

// The panel's backup of its database lives in the panel's own state, which
// its user can change, so the agent takes a copy into its own state, and
// only from a regular file of the panel's reached without following links.
// A rollback puts back that copy, whatever the panel's state holds by then.
func TestThePanelsBackupIsKeptByTheAgent(t *testing.T) {
	ctx := context.Background()
	g := newGitHub(t, release{"v1.3.0", []byte("racksail 1.3.0")})
	s := newServer(t, g, "1.2.0", nil)
	if _, err := s.action(t, "check", nil); err != nil {
		t.Fatal(err)
	}
	set, _ := s.updater.Settings(ctx)
	backup := PanelBackupPath("v1.3.0")
	in := s.updater.installInput(set.Spec, set.Status.Available, binaryPath, backup)
	if in.PanelBackup != backup {
		t.Fatalf("the panel's backup isn't passed on: %+v", in)
	}
	files := s.machine.Backend().Files
	panel := system.Owner{User: PanelUser, Group: PanelUser}
	dir := path.Dir(backup)
	for _, d := range []string{PanelBackups, dir} {
		if err := files.Mkdir(ctx, d, 0o750, panel); err != nil && !errors.Is(err, system.ErrExists) {
			t.Fatal(err)
		}
	}
	if err := files.WriteFile(ctx, "/etc/shadow", []byte("root:secret"), 0o600, system.Owner{}); err != nil {
		t.Fatal(err)
	}
	refused := func(what string, in InstallReleaseInput) {
		t.Helper()
		if _, err := InstallRelease.Call(ctx, s.agent, in, agent.CallOptions{}); err == nil || !strings.Contains(err.Error(), "panel") {
			t.Errorf("%s is taken: %v", what, err)
		}
		if got := s.read(t, binaryPath); got != "racksail 1.2.0" {
			t.Fatalf("%s installed the release: %q", what, got)
		}
	}

	// Only the backup of the release, in its usual place.
	other := in
	other.PanelBackup = PanelBackups + "/x/shadow"
	refused("a backup somewhere else", other)
	// Not through a link, at the end or on the way.
	if err := files.Symlink(ctx, "/etc/shadow", backup); err != nil {
		t.Fatal(err)
	}
	refused("a link to another file", in)
	files.Remove(ctx, backup)
	files.Remove(ctx, dir)
	if err := files.WriteFile(ctx, "/etc/state.db", []byte("root's"), 0o600, system.Owner{}); err != nil {
		t.Fatal(err)
	}
	if err := files.Symlink(ctx, "/etc", dir); err != nil {
		t.Fatal(err)
	}
	refused("a backup through a link to another directory", in)
	files.Remove(ctx, dir)
	if err := files.Mkdir(ctx, dir, 0o750, panel); err != nil {
		t.Fatal(err)
	}
	// Not a file the panel's user doesn't own, as a hard link would be.
	if err := files.WriteFile(ctx, backup, []byte("root:secret"), 0o600, system.Owner{}); err != nil {
		t.Fatal(err)
	}
	refused("someone else's file", in)

	if err := files.WriteFile(ctx, backup, []byte("the panel's database"), 0o640, panel); err != nil {
		t.Fatal(err)
	}
	res, err := InstallRelease.Call(ctx, s.agent, in, agent.CallOptions{})
	if err != nil {
		t.Fatal(err)
	}
	kept := UpdateDir + "/v1.3.0/state.db"
	if res.State == nil || res.State.PanelBackup != kept || s.read(t, kept) != "the panel's database" {
		t.Fatalf("the agent keeps its own copy: %+v", res.State)
	}
	if info, err := files.Stat(ctx, kept); err != nil || info.Owner != "root" || info.Perm() != 0o600 {
		t.Errorf("the copy is root's alone: %+v, %v", info, err)
	}

	// Whatever the panel's state holds by the time of the rollback, what
	// goes back is the copy, into the panel's state and as the panel's.
	files.Remove(ctx, backup)
	if err := files.Symlink(ctx, "/etc/shadow", backup); err != nil {
		t.Fatal(err)
	}
	if rolled, err := Guard(ctx, s.machine.Backend(), true); err != nil || !rolled {
		t.Fatalf("guard: %v, %v", rolled, err)
	}
	db := system.StateDir + "/state.db"
	if got := s.read(t, db); got != "the panel's database" {
		t.Errorf("the panel's database after the rollback = %q", got)
	}
	if info, _ := files.Stat(ctx, db); info.Owner != PanelUser {
		t.Errorf("the database belongs to the panel again: %+v", info)
	}

	// A record that names anything else puts nothing back.
	var st UpdateState
	json.Unmarshal([]byte(s.read(t, StateFile)), &st)
	st.State, st.PanelBackup = StateInstalled, "/etc/shadow"
	if err := writeState(ctx, files, &st); err != nil {
		t.Fatal(err)
	}
	if _, err := Guard(ctx, s.machine.Backend(), true); err == nil {
		t.Error("the guard puts back a file the agent didn't keep")
	}
	if got := s.read(t, db); got != "the panel's database" {
		t.Errorf("the panel's database = %q", got)
	}
}

func TestMirrorsComeFirst(t *testing.T) {
	ctx := context.Background()
	g := newGitHub(t, release{"v1.3.0", []byte("racksail 1.3.0")})
	listed := true
	mirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The mirror serves what GitHub serves, under the same names.
		g.mu.Lock()
		defer g.mu.Unlock()
		if p, ok := strings.CutPrefix(r.URL.Path, "/m/sigstore/"); ok && p == testRoot+".trusted_root.json" {
			w.Write(g.root)
			return
		}
		if r.URL.Path == "/m/"+ReleasesFile && listed {
			json.NewEncoder(w).Encode([]map[string]any{{"tag_name": "v1.3.0", "draft": false}})
			return
		}
		data, ok := g.files["/dl/"+strings.TrimPrefix(r.URL.Path, "/m/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(data)
	}))
	defer mirror.Close()
	c := g.client([]string{mirror.URL + "/m"})
	g.mu.Lock()
	g.fetched = nil
	g.mu.Unlock()
	releases, err := c.Releases(ctx)
	if err != nil || len(releases) != 1 || releases[0].Tag != "v1.3.0" {
		t.Fatalf("releases = %+v, %v", releases, err)
	}
	root, _, err := c.TrustedRoot(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	rel, err := c.Manifest(ctx, root, "v1.3.0")
	if err != nil || rel.Version.String() != "1.3.0" {
		t.Fatalf("manifest = %+v, %v", rel, err)
	}
	if len(g.fetched) != 0 {
		t.Errorf("a working mirror spares GitHub: %v", g.fetched)
	}
	files, _ := c.AssetURLs("v1.3.0", rel.Assets[0])
	if !strings.HasPrefix(files[0], mirror.URL) {
		t.Errorf("downloads try the mirror first: %v", files)
	}

	// A mirror that doesn't keep the list leaves it to GitHub.
	g.mu.Lock()
	listed = false
	g.mu.Unlock()
	if releases, err := c.Releases(ctx); err != nil || len(releases) != 1 {
		t.Fatalf("releases from GitHub = %+v, %v", releases, err)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if !slices.Contains(g.fetched, "/api/repos/"+Repository+"/releases") {
		t.Errorf("GitHub lists the releases when the mirror doesn't: %v", g.fetched)
	}
}

func TestUpgradeDebian(t *testing.T) {
	ctx := context.Background()
	g := newGitHub(t)
	s := newServer(t, g, "1.2.0", nil)
	files := s.machine.Backend().Files
	sources := "Types: deb\nURIs: http://deb.debian.org/debian\nSuites: trixie trixie-updates\nComponents: main\n\n" +
		"Types: deb\nURIs: http://security.debian.org/debian-security\nSuites: trixie-security\nComponents: main\n"
	pref := "Package: *\nPin: release o=Debian,n=trixie\nPin-Priority: 990\n\nPackage: *\nPin: release o=Debian,n=trixie-security\nPin-Priority: 990\n"
	files.Mkdir(ctx, "/etc/apt/sources.list.d", 0o755, system.Owner{})
	files.Mkdir(ctx, "/etc/apt/preferences.d", 0o755, system.Owner{})
	if err := files.WriteFile(ctx, "/etc/apt/sources.list.d/debian.sources", []byte(sources), 0o644, system.Owner{}); err != nil {
		t.Fatal(err)
	}
	if err := files.WriteFile(ctx, debianPrefFile, []byte(pref), 0o644, system.Owner{}); err != nil {
		t.Fatal(err)
	}
	// A module that isn't ready refuses, and the upgrade doesn't start.
	err := s.hooks.Register(hooks.Registration{Point: hooks.OSUpgradeCheck, Kind: hooks.Validate, Owner: "module:databases", OnValidate: func(ctx context.Context, c *hooks.Context) error {
		if c.Data.(OSUpgradeData).To == "forky" {
			return i18n.ErrConflict.New("what", "MariaDB 10.11")
		}
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.action(t, "upgrade_os", UpgradeOSInput{To: "forky"}); err == nil || !strings.Contains(err.Error(), "MariaDB") {
		t.Fatalf("a module can refuse the upgrade: %v", err)
	}
	if got := s.read(t, "/etc/apt/sources.list.d/debian.sources"); got != sources {
		t.Error("a refused upgrade changes nothing")
	}
	s.hooks.Unregister("module:databases")
	// Observe hooks run in the background, so they report through channels.
	told := map[hooks.Point]chan OSUpgradeData{hooks.OSUpgradeBefore: make(chan OSUpgradeData, 4), hooks.OSUpgradeAfter: make(chan OSUpgradeData, 4)}
	for p, ch := range told {
		err := s.hooks.Register(hooks.Registration{Point: p, Kind: hooks.Observe, Owner: "module:mail", OnObserve: func(ctx context.Context, c *hooks.Context) {
			ch <- c.Data.(OSUpgradeData)
		}})
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.action(t, "upgrade_os", UpgradeOSInput{To: "duke"}); err == nil {
		t.Error("Debian is upgraded one release at a time")
	}
	cs, err := s.action(t, "upgrade_os", UpgradeOSInput{To: "forky"})
	if err != nil || cs.State != changeset.StateApplied {
		t.Fatalf("upgrade: %v %s", err, mustJSON(cs))
	}
	got := s.read(t, "/etc/apt/sources.list.d/debian.sources")
	if !strings.Contains(got, "Suites: forky forky-updates") || !strings.Contains(got, "Suites: forky-security") || strings.Contains(got, "trixie") {
		t.Errorf("sources = %s", got)
	}
	if p := s.read(t, debianPrefFile); !strings.Contains(p, "n=forky\n") || !strings.Contains(p, "n=forky-security") {
		t.Errorf("the release pin follows: %s", p)
	}
	if s.read(t, "/etc/apt/sources.list.d/debian.sources.racksail-trixie") != sources {
		t.Error("the sources from before are kept")
	}
	var out UpgradeOSOutput
	json.Unmarshal(cs.Changes[0].Output, &out)
	if !out.RebootRequired || out.From != "trixie" || out.To != "forky" {
		t.Errorf("output = %+v", out)
	}
	for _, p := range []hooks.Point{hooks.OSUpgradeBefore, hooks.OSUpgradeAfter} {
		select {
		case d := <-told[p]:
			if d.From != "trixie" || d.To != "forky" || (p == hooks.OSUpgradeAfter && d.Outcome != "upgraded") {
				t.Errorf("%s = %+v", p, d)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("modules aren't told at %s", p)
		}
	}
	select {
	case d := <-told[hooks.OSUpgradeBefore]:
		t.Errorf("modules are told once before: %+v", d)
	case d := <-told[hooks.OSUpgradeAfter]:
		t.Errorf("modules are told once after: %+v", d)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestMoveRelease(t *testing.T) {
	in := "deb http://deb.debian.org/debian trixie main\ndeb http://security.debian.org/debian-security trixie-security main\n# trixie stays in comments\nSuites: trixie trixie-updates trixie-backports\nURIs: http://example.com/trixie-mirror/\n"
	got := string(moveRelease([]byte(in), "trixie", "forky"))
	want := "deb http://deb.debian.org/debian forky main\ndeb http://security.debian.org/debian-security forky-security main\n# trixie stays in comments\nSuites: forky forky-updates forky-backports\nURIs: http://example.com/trixie-mirror/\n"
	if got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
}

func mustJSON(v any) []byte {
	raw, _ := json.Marshal(v)
	return raw
}
