package addons

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"path"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/agent"
	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/changeset"
	"github.com/vilanobeachflorida/racksail/internal/events"
	"github.com/vilanobeachflorida/racksail/internal/hooks"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/notify"
	"github.com/vilanobeachflorida/racksail/internal/reconcile"
	"github.com/vilanobeachflorida/racksail/internal/store"
	"github.com/vilanobeachflorida/racksail/internal/system"
)

// Event types of safe mode.
var (
	SafeModeEntered = events.Type{Name: "core.safe_mode.entered", Description: "Safe mode turned every add-on off.", Severity: events.SeverityCritical, Data: SafeModeSource{}}
	SafeModeLeft    = events.Type{Name: "core.safe_mode.left", Description: "Safe mode was turned off, so add-ons run again.", Severity: events.SeverityNotice}
)

func init() {
	events.RegisterType(SafeModeEntered)
	events.RegisterType(SafeModeLeft)
}

// Crash loops: an add-on that restarts this often this soon after it
// started turns safe mode on.
const (
	CrashLoopRestarts = 3
	CrashLoopWindow   = 2 * time.Minute
	watchInterval     = 10 * time.Second
	healthTimeout     = 3 * time.Second
)

// Options are what a Runtime needs.
type Options struct {
	Registry *hooks.Registry
	Store    store.Store
	// Kinds are the kinds the panel knows, which the kinds add-ons add
	// join; the process-wide registry when nil.
	Kinds *store.KindRegistry
	// Reconcilers are the panel's reconcilers, which those of the kinds
	// add-ons add join.
	Reconcilers *reconcile.Registry
	Agent       agent.Client
	Events      events.Bus
	Audit       events.AuditLog
	Tokens      Tokens
	SafeMode    *SafeMode
	// TokenDir is where tokens are written for systemd; SocketDir is where
	// add-ons serve the panel.
	TokenDir  string
	SocketDir string
	// Resolve looks up the addresses of the hosts add-ons may reach.
	Resolve func(ctx context.Context, host string) ([]netip.Addr, error)
	// Enqueue asks for a resource to be reconciled again, as the reconcile
	// queue's Enqueue does.
	Enqueue func(id store.ID, reason string)
	// Catalogs are what add-ons are checked against when they name the
	// catalog they come from, as every account's add-on does.
	Catalogs CatalogItems
	// Accounts returns where accounts' add-ons run, once the module that
	// keeps accounts says; until it does, they don't run.
	Accounts func() Accounts
	Log      *slog.Logger
	Now      func() time.Time
}

// Runtime runs installed add-ons: it reconciles their resources, attaches
// what their manifests declare to the hook pipeline, watches them for
// crash loops and keeps safe mode.
type Runtime struct {
	opts Options

	mu      sync.Mutex
	running map[string]*installed
	// safe says the runtime has turned the add-ons off for safe mode.
	safe bool
	// added are the kinds add-ons add.
	added addedKinds
	// frames are the frames people opened add-ons' screens in.
	frames frames
}

// installed is an add-on the runtime attached.
type installed struct {
	resource  store.ID
	kind      string
	instance  string
	manifest  *hooks.AddonManifest
	grants    Grants
	client    *Client
	startedAt time.Time
	restarts  int
}

// NewRuntime returns a runtime.
func NewRuntime(opts Options) *Runtime {
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.TokenDir == "" {
		opts.TokenDir = TokenDir
	}
	if opts.SocketDir == "" {
		opts.SocketDir = SocketDir
	}
	if opts.Tokens == nil {
		opts.Tokens = NewMemoryTokens()
	}
	if opts.Kinds == nil {
		opts.Kinds = store.Kinds()
	}
	if opts.Resolve == nil {
		opts.Resolve = func(ctx context.Context, host string) ([]netip.Addr, error) {
			return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		}
	}
	return &Runtime{
		opts: opts, running: map[string]*installed{},
		added:  addedKinds{kinds: map[string]*Kind{}, reconciling: map[string]bool{}},
		frames: frames{open: map[string]*frame{}, now: opts.Now},
	}
}

var instanceSuffix = regexp.MustCompile(`[^a-z0-9]`)

// Instance names the add-on's service: its id for add-ons a server admin
// installed, and its id with a part of the account's for account add-ons,
// which every account runs on its own.
func Instance(kind, id string, scope store.Scope) string {
	if kind != AccountAddon.Name() || scope.Level != store.ScopeAccount {
		return id
	}
	acct := instanceSuffix.ReplaceAllString(strings.ToLower(string(scope.ID)), "")
	if len(acct) > 10 {
		acct = acct[len(acct)-10:]
	}
	return id + "-" + acct
}

// SafeMode returns the runtime's safe mode.
func (r *Runtime) SafeMode() *SafeMode { return r.opts.SafeMode }

func (r *Runtime) kinds() *store.KindRegistry { return r.opts.Kinds }

func (r *Runtime) safeModeOn() bool { return r.opts.SafeMode != nil && r.opts.SafeMode.Active() }

// Reconcilers reconcile installed add-ons of both kinds.
func (r *Runtime) Reconcilers() []reconcile.Reconciler {
	return []reconcile.Reconciler{reconcile.Typed(Addon, addonReconciler{r: r}), reconcile.Typed(AccountAddon, addonReconciler{r: r})}
}

type addonReconciler struct{ r *Runtime }

// limits returns what an add-on's service may use: the approved limits,
// or the manifest's, or the defaults.
func limits(approved, asked hooks.Resources) (int, int) {
	mem, cpu := DefaultMemoryMB, DefaultCPUPercent
	for _, res := range []hooks.Resources{asked, approved} {
		if res.MemoryMB > 0 {
			mem = res.MemoryMB
		}
		if p, err := strconv.Atoi(strings.TrimSuffix(res.CPU, "%")); err == nil && p > 0 {
			cpu = p
		}
	}
	return min(mem, maxMemoryMB), min(cpu, maxCPUPercent)
}

func (a addonReconciler) Plan(ctx context.Context, obj *store.Object[AddonSpec, AddonStatus], req reconcile.Request) (reconcile.Plan, error) {
	inst := Instance(obj.Kind, obj.Spec.ID, obj.Scope)
	if obj.Deleted() {
		return reconcile.Plan{Steps: []agent.Step{
			RemoveAddonService.Titled("Stop the add-on", AddonInput{ID: inst}),
			agent.RemovePath.Titled("Remove the add-on's program", agent.RemovePathInput{Path: AddonDir(inst, ""), Recursive: true}),
		}}, nil
	}
	man, err := Check(obj.Spec, obj.Kind == AccountAddon.Name())
	if err != nil {
		return reconcile.Plan{}, err
	}
	if !obj.Spec.Enabled || a.r.safeModeOn() {
		return reconcile.Plan{Steps: []agent.Step{RemoveAddonService.Titled("Stop the add-on", AddonInput{ID: inst})}}, nil
	}
	svc, err := a.r.serviceSpec(ctx, req.Read, obj, inst, man, obj.Status.Hosts)
	if err != nil {
		return reconcile.Plan{}, err
	}
	archive := system.Archive{
		URL: obj.Spec.Package.URL, SHA256: obj.Spec.Package.SHA256, Size: obj.Spec.Package.Size, Format: obj.Spec.Package.Format,
		StripComponents: obj.Spec.Package.StripComponents, Dest: AddonDir(inst, man.Version),
	}
	return reconcile.Plan{
		Steps: []agent.Step{
			agent.InstallArchive.Titled("Download "+man.Name+" "+man.Version, agent.InstallArchiveInput{Archive: archive}),
			EnsureAddonService.Titled("Set up the add-on's sandbox", svc),
			agent.EnableUnits.Titled("Start the add-on with the server", agent.UnitsInput{Names: []string{SocketUnit(inst)}}),
			agent.StartUnit.Titled("Open the add-on's socket", agent.UnitInput{Name: SocketUnit(inst)}),
		},
		Effects: []system.Effect{{Type: system.EffectProcess, Action: "run", Target: Service(inst), Detail: man.Name + " " + man.Version, MemoryBytes: int64(svc.MemoryMB) << 20}},
		Checks:  []reconcile.HealthCheck{{Name: "socket", Type: reconcile.CheckUnit, Target: SocketUnit(inst), Timeout: 10 * time.Second}},
	}, nil
}

func (a addonReconciler) Apply(ctx context.Context, obj *store.Object[AddonSpec, AddonStatus], plan reconcile.Plan, ex reconcile.Executor) (AddonStatus, error) {
	r := a.r
	st := obj.Status
	inst := Instance(obj.Kind, obj.Spec.ID, obj.Scope)
	now := r.opts.Now()
	if err := reconcile.ApplySteps(ctx, obj.Generation, plan, ex, &st.Status, now); err != nil {
		r.detach(ctx, inst, false)
		st.State = StateFailed
		return st, err
	}
	if obj.Deleted() {
		r.detach(ctx, inst, true)
		r.retireKinds(inst)
		return st, nil
	}
	man, err := Check(obj.Spec, obj.Kind == AccountAddon.Name())
	if err != nil {
		st.State = StateFailed
		return st, err
	}
	// The kinds it adds are there whether or not it runs, so their
	// resources stay readable; they only change while it runs.
	kindProblems := r.declareKinds(inst, man)
	if !obj.Spec.Enabled || r.safeModeOn() {
		r.detach(ctx, inst, true)
		st.State, st.Health, st.Hooks = StateStopped, "", 0
		st.Slice, st.MemoryMB, st.CPUPercent = "", 0, 0
		if obj.Spec.Enabled {
			st.State = StateSafeMode
			st.Message = "Safe mode is on, so the add-on doesn't run."
		}
		return st, nil
	}
	in := &installed{
		resource: obj.ID, kind: obj.Kind, instance: inst, manifest: man,
		grants: NewGrants(obj.Spec.Approved, obj.Scope).WithKinds(r.opts.Kinds), client: NewClient(man.Name, path.Join(r.opts.SocketDir, inst+".sock")),
		startedAt: now,
	}
	// The hosts it may reach are looked up now; their addresses go into its
	// sandbox, and the service starts again when they changed.
	hosts, err := r.resolve(ctx, man.Permissions.Network, obj.Spec.Approved.Network)
	if err != nil {
		st.State = StateFailed
		e := i18n.From(err)
		st.Phase, st.Message, st.Code = store.PhaseFailed, e.Message, e.Code
		return st, err
	}
	svc, err := r.serviceSpec(ctx, r.opts.Store, obj, inst, man, hosts)
	if err != nil {
		st.State = StateFailed
		return st, err
	}
	restart := false
	if !sameHosts(hosts, st.Hosts) {
		if _, err := ex.Run(ctx, []agent.Step{EnsureAddonService.Titled("Let the add-on reach its hosts", svc)}); err != nil {
			st.State = StateFailed
			return st, err
		}
		st.Hosts, restart = hosts, true
	}
	// A running service moves to another slice, or takes new limits, when
	// it starts again.
	slice := svc.Slice
	if slice == "" {
		slice = system.PlatformSlice
	}
	if st.Slice != slice || st.MemoryMB != svc.MemoryMB || st.CPUPercent != svc.CPUPercent {
		st.Slice, st.MemoryMB, st.CPUPercent, restart = slice, svc.MemoryMB, svc.CPUPercent, true
	}
	changed, err := r.ensureToken(ctx, inst, st.Version != man.Version)
	if err != nil {
		st.State = StateFailed
		return st, err
	}
	if changed || restart || (st.Version != "" && st.Version != man.Version) {
		// A running service reads its token and sandbox when it starts.
		if us, err := agent.UnitStatus.Call(ctx, r.opts.Agent, agent.UnitStatusInput{Name: Service(inst)}, agent.CallOptions{}); err == nil && us.Active() {
			if _, err := ex.Run(ctx, []agent.Step{agent.RestartUnit.Titled("Restart the add-on", agent.UnitInput{Name: Service(inst)})}); err != nil {
				st.State = StateFailed
				return st, err
			}
		}
	}
	r.removeOtherVersions(ctx, ex, inst, man.Version)
	if us, err := agent.UnitStatus.Call(ctx, r.opts.Agent, agent.UnitStatusInput{Name: Service(inst)}, agent.CallOptions{}); err == nil {
		in.restarts = us.Restarts
	}
	count, problems := r.attach(in)
	problems = append(kindProblems, problems...)
	st.Hooks, st.Version, st.State = count, man.Version, StateRunning
	checked := now.UTC()
	st.CheckedAt = &checked
	st.Health = r.health(ctx, in)
	if len(problems) > 0 {
		st.Phase, st.Message = store.PhaseDegraded, "Some of what the add-on declares couldn't be attached: "+strings.Join(problems, "; ")
	} else if st.Health != "ok" {
		st.Phase, st.Message = store.PhaseDegraded, "The add-on doesn't answer yet."
	}
	return st, nil
}

// resolve looks up the addresses of the hosts an add-on may reach, those
// its manifest asks for and that were approved. Hosts written as addresses
// or networks are taken as they are.
func (r *Runtime) resolve(ctx context.Context, asked, approved []string) ([]Host, error) {
	var out []Host
	for _, host := range asked {
		if !slices.Contains(approved, host) {
			continue
		}
		host = strings.TrimSpace(host)
		if p, err := parseNetwork(host); err == nil {
			out = append(out, Host{Addresses: []string{p.String()}})
			continue
		}
		name := host
		if h, _, err := net.SplitHostPort(host); err == nil {
			name = h
		}
		addrs, err := r.opts.Resolve(ctx, name)
		if err != nil || len(addrs) == 0 {
			return nil, ErrManifest.Wrap(fmt.Errorf("resolving %s: %v", name, err), "reason", "the host "+name+" it may reach doesn't resolve")
		}
		h := Host{Name: strings.ToLower(name)}
		for _, a := range addrs {
			a = a.Unmap()
			h.Addresses = append(h.Addresses, netip.PrefixFrom(a, a.BitLen()).String())
		}
		sort.Strings(h.Addresses)
		h.Addresses = slices.Compact(h.Addresses)
		out = append(out, h)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return strings.Join(out[i].Addresses, " ") < strings.Join(out[j].Addresses, " ")
	})
	return out, nil
}

func sameHosts(a, b []Host) bool {
	return slices.EqualFunc(a, b, func(x, y Host) bool { return x.Name == y.Name && slices.Equal(x.Addresses, y.Addresses) })
}

// ensureToken gives an add-on a token unless it has one, or a new one
// when rotate is set. It reports whether the token changed.
func (r *Runtime) ensureToken(ctx context.Context, inst string, rotate bool) (bool, error) {
	if !rotate {
		if _, err := readTokenFile(r.opts.TokenDir, inst); err == nil {
			return false, nil
		}
	}
	token, hash, err := NewToken()
	if err != nil {
		return false, err
	}
	if err := r.opts.Tokens.Put(ctx, inst, hash); err != nil {
		return false, err
	}
	if err := writeToken(r.opts.TokenDir, inst, token); err != nil {
		return false, err
	}
	return true, nil
}

// removeOtherVersions deletes the directories of versions the add-on no
// longer runs.
func (r *Runtime) removeOtherVersions(ctx context.Context, ex reconcile.Executor, inst, version string) {
	results, err := ex.Run(ctx, []agent.Step{agent.ListDir.Step(agent.PathInput{Path: AddonDir(inst, "")})})
	if err != nil || len(results) == 0 {
		return
	}
	var list agent.DirListing
	if json.Unmarshal(results[0].Output, &list) != nil {
		return
	}
	for _, e := range list.Entries {
		if v := path.Base(e.Path); v != version && versionPattern.MatchString(v) {
			if _, err := ex.Run(ctx, []agent.Step{agent.RemovePath.Titled("Remove version "+v, agent.RemovePathInput{Path: e.Path, Recursive: true})}); err != nil {
				r.opts.Log.Warn("removing an add-on's old version", "addon", inst, "version", v, "error", err)
			}
		}
	}
}

// health asks an add-on how it is.
func (r *Runtime) health(ctx context.Context, in *installed) string {
	ctx, cancel := context.WithTimeout(ctx, healthTimeout)
	defer cancel()
	h, err := in.client.Health(ctx)
	switch {
	case err != nil:
		return "unreachable"
	case h.Status != "ok":
		return "degraded"
	}
	return "ok"
}

// attach registers what an add-on's manifest declares: its hooks, which
// call it over its socket with what its permissions grant, its providers,
// its UI entries and its API routes. It returns how many it attached and
// what couldn't be.
func (r *Runtime) attach(in *installed) (int, []string) {
	owner := hooks.OwnerAddon + ":" + in.instance
	reg := r.opts.Registry
	reg.Unregister(owner)
	label := in.manifest.Name + " " + in.manifest.Version
	server := in.grants.Scope.Level == store.ScopeServer
	var problems []string
	count := 0
	add := func(what string, h hooks.Registration) {
		if err := reg.Register(h); err != nil {
			problems = append(problems, what+": "+i18n.From(err).Message)
			return
		}
		count++
	}
	for _, decl := range in.manifest.Hooks {
		// Check refuses these already; an account's add-on never reaches
		// what belongs to the whole server, whatever its resource says.
		if in.grants.Account() && !hooks.AccountPoint(hooks.Point(decl.Point)) {
			problems = append(problems, decl.Point+": an account's add-ons only take part in changes")
			continue
		}
		deadline, _ := decl.DeadlineDuration()
		h := hooks.Registration{
			Point: hooks.Point(decl.Point), Kind: decl.Kind, Owner: owner, Label: label, Priority: decl.Priority,
			Deadline: deadline, Failure: decl.FailureMode, Security: decl.Security, Override: server && in.grants.perms.Override,
		}
		r.handlers(in, &h)
		add(decl.Point, h)
	}
	for _, capability := range in.manifest.Provides {
		if in.grants.Account() {
			problems = append(problems, capability+": what add-ons provide serves the whole server")
			continue
		}
		point, ok := hooks.Capabilities[capability]
		if !ok {
			problems = append(problems, capability+": not a capability")
			continue
		}
		in := in
		add(capability, hooks.Registration{Point: point, Kind: hooks.Provide, Owner: owner, Label: label, Provider: &hooks.Provider{
			Name: in.instance, Title: in.manifest.Name, Description: in.manifest.Description,
			Settings: map[string]any{},
			Open: func(ctx context.Context, settings json.RawMessage) (any, error) {
				return newRemote(in, capability, settings), nil
			},
		}})
	}
	for _, ui := range in.manifest.UI {
		ui := ui
		point, ok := hooks.ExtendPoint(ui.Type)
		if !ok {
			problems = append(problems, ui.Type+": not a place for UI entries")
			continue
		}
		add("ui "+ui.Label, hooks.Registration{Point: point, Kind: hooks.Extend, Owner: owner, Label: label, Extension: &hooks.Extension{UI: &ui}})
	}
	if len(in.manifest.Routes) > 0 {
		add("routes", hooks.Registration{Point: hooks.ExtendRoutes, Kind: hooks.Extend, Owner: owner, Label: label, Extension: &hooks.Extension{
			Routes: in.manifest.Routes, Handler: in.client.Proxy("/routes/", actorHeaders),
		}})
	}
	r.mu.Lock()
	r.running[in.instance] = in
	r.mu.Unlock()
	return count, problems
}

// actorHeaders tell an add-on who is asking, or looking at one of its
// screens, without anything that would let it act as them.
func actorHeaders(req *http.Request) http.Header {
	who := "{}"
	if a, ok := auth.ActorFrom(req.Context()); ok {
		raw, _ := json.Marshal(redactActor(a))
		who = string(raw)
	}
	return http.Header{"Racksail-Actor": {who}}
}

// handlers fills in a registration's handler, which calls the add-on. An
// account's add-on is only ever called about changes to its account: never
// with what lifecycle and identity points describe, and never about a
// change set none of whose changes it may see.
func (r *Runtime) handlers(in *installed, h *hooks.Registration) {
	g := in.grants
	switch h.Kind {
	case hooks.Validate:
		h.OnValidate = func(ctx context.Context, c *hooks.Context) error {
			if g.Account() && (c.ChangeSet == nil || c.Change == nil) {
				return nil
			}
			call := hooks.HookCall{Point: c.Point, Kind: hooks.Validate, Actor: redactActor(c.Actor), Data: c.Data}
			if c.ChangeSet != nil {
				if c.Change != nil && !g.InScope(c.Change.Scope) {
					return nil
				}
				call.Data = nil
				call.ChangeSet = g.FilterChangeSet(c.ChangeSet)
				for i := range c.ChangeSet.Changes {
					if &c.ChangeSet.Changes[i] == c.Change {
						idx := i
						call.Change = &idx
					}
				}
			}
			reply, err := in.client.Hook(ctx, call)
			if err != nil {
				return err
			}
			if reply.Deny {
				reason := strings.TrimSpace(reply.Reason)
				if reason == "" {
					reason = in.manifest.Name + " refused the change."
				}
				return ErrRefused.New("reason", reason)
			}
			return nil
		}
	case hooks.Modify:
		h.OnModify = func(ctx context.Context, c *hooks.Context) error {
			if c.ChangeSet == nil {
				return nil
			}
			var mine []int
			for _, i := range c.Changes {
				if g.InScope(c.ChangeSet.Changes[i].Scope) {
					mine = append(mine, i)
				}
			}
			if len(mine) == 0 {
				return nil
			}
			reply, err := in.client.Hook(ctx, hooks.HookCall{Point: c.Point, Kind: hooks.Modify, Actor: redactActor(c.Actor), ChangeSet: g.FilterChangeSet(c.ChangeSet), Changes: mine})
			if err != nil {
				return err
			}
			return g.Merge(ctx, r.opts.Store, c.ChangeSet, reply.ChangeSet, mine)
		}
	case hooks.Observe:
		h.OnObserve = func(ctx context.Context, c *hooks.Context) {
			if g.Account() && c.ChangeSet == nil {
				return
			}
			call := hooks.HookCall{Point: c.Point, Kind: hooks.Observe, Actor: redactActor(c.Actor), Data: c.Data}
			if c.ChangeSet != nil {
				var mine []int
				for _, i := range c.Changes {
					if g.InScope(c.ChangeSet.Changes[i].Scope) {
						mine = append(mine, i)
					}
				}
				if len(mine) == 0 && (len(c.Changes) > 0 || g.Account()) {
					return
				}
				call.Data = nil
				call.ChangeSet, call.Changes = g.FilterChangeSet(c.ChangeSet), mine
			}
			if _, err := in.client.Hook(ctx, call); err != nil {
				r.opts.Log.Warn("an add-on's observe hook failed", "addon", in.instance, "point", c.Point, "error", err)
			}
		}
	case hooks.System:
		h.OnConfig = func(ctx context.Context, p *hooks.ConfigPatch) error {
			reply, err := in.client.Hook(ctx, hooks.HookCall{Point: h.Point, Kind: hooks.System, Config: p})
			if err != nil {
				return err
			}
			if reply.Config == nil {
				return errors.New("the add-on returned no configuration")
			}
			p.Config = reply.Config
			return nil
		}
	}
}

// detach takes an add-on's hooks out of the pipeline and closes the frames
// of its screens, and with forget forgets its token too.
func (r *Runtime) detach(ctx context.Context, inst string, forget bool) {
	r.opts.Registry.Unregister(hooks.OwnerAddon + ":" + inst)
	r.closeFrames(inst)
	r.mu.Lock()
	in := r.running[inst]
	delete(r.running, inst)
	r.mu.Unlock()
	if in != nil {
		in.client.Close()
	}
	if !forget {
		return
	}
	if err := r.opts.Tokens.Delete(ctx, inst); err != nil {
		r.opts.Log.Warn("forgetting an add-on's token", "addon", inst, "error", err)
	}
	if err := removeToken(r.opts.TokenDir, inst); err != nil {
		r.opts.Log.Warn("removing an add-on's token", "addon", inst, "error", err)
	}
}

// Running returns the add-on running as instance.
func (r *Runtime) Running(inst string) (resource store.ID, man *hooks.AddonManifest, g Grants, client *Client, ok bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	in, ok := r.running[inst]
	if !ok {
		return "", nil, Grants{}, nil, false
	}
	return in.resource, in.manifest, in.grants, in.client, true
}

// Instances lists the add-ons that run, sorted.
func (r *Runtime) Instances() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.running))
	for inst := range r.running {
		out = append(out, inst)
	}
	sort.Strings(out)
	return out
}

// Load reconciles every installed add-on as the panel starts, so their
// hooks are attached before the first change, or, in safe mode, none of
// them is.
func (r *Runtime) Load(ctx context.Context) {
	for _, k := range Kinds() {
		cursor := ""
		for {
			page, next, err := k.List(ctx, r.opts.Store, store.Query{Cursor: cursor, Limit: store.MaxLimit})
			if err != nil {
				r.opts.Log.Error("listing add-ons", "error", err)
				return
			}
			for _, obj := range page {
				if r.opts.Enqueue != nil {
					r.opts.Enqueue(obj.ID, "the panel started")
				}
			}
			if next == "" {
				break
			}
			cursor = next
		}
	}
}

// Run watches the add-ons until ctx ends: safe mode turned on or off from
// the console, and add-ons that keep crashing as they start, which turn
// safe mode on.
func (r *Runtime) Run(ctx context.Context) {
	if r.opts.SafeMode != nil && r.opts.SafeMode.Active() {
		r.enterSafeMode(ctx, r.opts.SafeMode.Status())
	}
	ticker := time.NewTicker(watchInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.Watch(ctx)
		}
	}
}

// Watch checks once what Run checks periodically.
func (r *Runtime) Watch(ctx context.Context) {
	if sm := r.opts.SafeMode; sm != nil {
		st := sm.Status()
		r.mu.Lock()
		was := r.safe
		r.mu.Unlock()
		switch {
		case st.Active && !was:
			r.enterSafeMode(ctx, st)
			return
		case !st.Active && was:
			r.leftSafeMode(ctx)
			return
		case st.Active:
			return
		}
	}
	r.mu.Lock()
	list := make([]*installed, 0, len(r.running))
	for _, in := range r.running {
		list = append(list, in)
	}
	r.mu.Unlock()
	now := r.opts.Now()
	for _, in := range list {
		us, err := agent.UnitStatus.Call(ctx, r.opts.Agent, agent.UnitStatusInput{Name: Service(in.instance)}, agent.CallOptions{})
		if err != nil {
			continue
		}
		crashed := us.Restarts - in.restarts
		if crashed >= CrashLoopRestarts && now.Sub(in.startedAt) <= CrashLoopWindow {
			reason := fmt.Sprintf("%s crashed %d times as it started.", in.manifest.Name, crashed)
			if r.opts.SafeMode != nil {
				if err := r.opts.SafeMode.Enter(SafeModeAutomatic, reason, now); err != nil {
					r.opts.Log.Error("turning safe mode on", "error", err)
				}
				r.enterSafeMode(ctx, r.opts.SafeMode.Status())
			}
			return
		}
	}
}

// EnterSafeMode turns safe mode on from the panel, as the console does.
func (r *Runtime) EnterSafeMode(ctx context.Context, actor *auth.Actor, reason string) error {
	if r.opts.SafeMode == nil {
		return errors.New("addons: there is no safe mode to turn on")
	}
	if err := r.opts.SafeMode.Enter(SafeModeConsole, reason, r.opts.Now()); err != nil {
		return err
	}
	r.record(ctx, actor, "core.safe_mode.enter", "Turned safe mode on: "+reason)
	r.enterSafeMode(ctx, r.opts.SafeMode.Status())
	return nil
}

// LeaveSafeMode turns off the console's and the panel's own safe mode,
// and starts the add-ons again unless the boot option keeps it on.
func (r *Runtime) LeaveSafeMode(ctx context.Context, actor *auth.Actor) (SafeModeStatus, error) {
	if r.opts.SafeMode == nil {
		return SafeModeStatus{}, nil
	}
	if err := r.opts.SafeMode.Leave(); err != nil {
		return SafeModeStatus{}, err
	}
	r.record(ctx, actor, "core.safe_mode.leave", "Turned safe mode off")
	st := r.opts.SafeMode.Status()
	if !st.Active {
		r.leftSafeMode(ctx)
	}
	return st, nil
}

// enterSafeMode takes every add-on out of the pipeline and stops it.
func (r *Runtime) enterSafeMode(ctx context.Context, st SafeModeStatus) {
	r.mu.Lock()
	already := r.safe
	r.safe = true
	insts := make([]string, 0, len(r.running))
	for inst := range r.running {
		insts = append(insts, inst)
	}
	r.mu.Unlock()
	r.opts.Registry.UnregisterFunc(func(reg hooks.Registration) bool { return hooks.OwnerType(reg.Owner) == hooks.OwnerAddon })
	for _, inst := range insts {
		r.detach(ctx, inst, false)
		for _, unit := range []string{SocketUnit(inst), Service(inst)} {
			if _, err := agent.StopUnit.Call(ctx, r.opts.Agent, agent.UnitInput{Name: unit}, agent.CallOptions{}); err != nil {
				r.opts.Log.Warn("stopping an add-on for safe mode", "unit", unit, "error", err)
			}
		}
	}
	r.markAll(ctx)
	if already {
		return
	}
	var src SafeModeSource
	if len(st.Sources) > 0 {
		src = st.Sources[len(st.Sources)-1]
	}
	r.opts.Log.Warn("safe mode is on; no add-on runs", "source", src.Kind, "reason", src.Reason)
	if r.opts.Events != nil {
		data, _ := json.Marshal(src)
		if err := r.opts.Events.Publish(context.WithoutCancel(ctx), events.Event{
			Type: SafeModeEntered.Name, Severity: SafeModeEntered.Severity, Scope: store.ServerScope(), Actor: auth.System("addons"),
			Message: "Safe mode is on, so no add-on runs. " + src.Reason, Data: data,
		}); err != nil {
			r.opts.Log.Warn("publishing an event", "error", err)
		}
	}
}

// leftSafeMode has every add-on reconciled again, which starts them.
func (r *Runtime) leftSafeMode(ctx context.Context) {
	r.mu.Lock()
	r.safe = false
	r.mu.Unlock()
	r.Load(ctx)
	if r.opts.Events != nil {
		if err := r.opts.Events.Publish(context.WithoutCancel(ctx), events.Event{
			Type: SafeModeLeft.Name, Severity: SafeModeLeft.Severity, Scope: store.ServerScope(), Actor: auth.System("addons"),
			Message: "Safe mode is off; add-ons start again.",
		}); err != nil {
			r.opts.Log.Warn("publishing an event", "error", err)
		}
	}
}

// markAll records in every installed add-on's status that safe mode keeps
// it from running.
func (r *Runtime) markAll(ctx context.Context) {
	for _, k := range Kinds() {
		page, _, err := k.List(ctx, r.opts.Store, store.Query{Limit: store.MaxLimit})
		if err != nil {
			return
		}
		for _, obj := range page {
			if !obj.Spec.Enabled || obj.Status.State == StateSafeMode {
				continue
			}
			st := obj.Status
			st.State, st.Health, st.Hooks = StateSafeMode, "", 0
			st.Message = "Safe mode is on, so the add-on doesn't run."
			if err := store.SetStatus(context.WithoutCancel(ctx), r.opts.Store, obj.ID, st); err != nil {
				r.opts.Log.Warn("recording safe mode in an add-on's status", "addon", obj.Spec.ID, "error", err)
			}
		}
	}
}

func (r *Runtime) record(ctx context.Context, actor *auth.Actor, action, summary string) {
	if r.opts.Audit == nil {
		return
	}
	rec := events.AuditRecord{Action: action, Scope: store.ServerScope(), Outcome: events.OutcomeSuccess, Summary: summary}
	if actor != nil {
		rec.Actor = *actor
	}
	if err := r.opts.Audit.Record(context.WithoutCancel(ctx), rec); err != nil {
		r.opts.Log.Error("writing an audit record", "action", action, "error", err)
	}
}

// Hooks are the runtime's own hooks: an add-on that changes something
// through the API, or adds a change through its modify hook, may only make
// the changes its permissions allow, the fields they name and deleting,
// actions and labels only as far as they say; only a server admin approves
// what only a server admin may grant; and an add-on from a catalog, as
// every account's add-on is, must be what the catalog offers.
func (r *Runtime) Hooks() []hooks.Registration {
	const owner = hooks.OwnerCore + ":addons"
	var out []hooks.Registration
	for _, op := range []string{"create", "update"} {
		out = append(out, hooks.Registration{
			Point: hooks.KindPoint(Addon.Name(), op, changeset.StageValidate), Kind: hooks.Validate, Owner: owner, Security: true, Priority: -1000,
			OnValidate: r.checkApproval,
		})
		for _, k := range Kinds() {
			// Checking against a catalog may fetch the item's files.
			out = append(out, hooks.Registration{
				Point: hooks.KindPoint(k.Name(), op, changeset.StageValidate), Kind: hooks.Validate, Owner: owner, Security: true, Priority: -1000,
				Deadline: hooks.MaxDeadline, OnValidate: r.checkCatalog,
			})
		}
	}
	out = append(out, r.kindHooks()...)
	return append(out, hooks.Registration{
		Point: "*.*.*.validate", Kind: hooks.Validate, Owner: owner, Security: true, Priority: -900,
		OnValidate: func(ctx context.Context, c *hooks.Context) error {
			if c.Change == nil {
				return nil
			}
			// What a modify hook added is checked again as the pipeline
			// planned it, with the resource's own scope and state, which
			// the person's grants alone don't hold the add-on to.
			if src := c.Change.Source; src.Type == hooks.OwnerAddon {
				_, man, g, _, ok := r.Running(src.Name)
				if !ok {
					return ErrUnknown.New("addon", src.Name)
				}
				if err := g.CheckAdded(man.Name, *c.Change); err != nil {
					return err
				}
			}
			if c.Actor == nil || c.Actor.Type != auth.ActorAddon {
				return nil
			}
			inst := r.instanceOf(c.Actor.ID)
			_, man, g, _, ok := r.Running(inst)
			if !ok {
				return ErrUnknown.New("addon", c.Actor.Name)
			}
			return g.CheckWrite(man.Name, *c.Change)
		},
	})
}

// instanceOf finds the instance of the add-on whose resource id acts.
func (r *Runtime) instanceOf(resource store.ID) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	for inst, in := range r.running {
		if in.resource == resource {
			return inst
		}
	}
	return ""
}

// remote is a capability an add-on provides, called over its socket.
type remote struct {
	in         *installed
	capability string
	settings   json.RawMessage
}

func newRemote(in *installed, capability string, settings json.RawMessage) *remote {
	return &remote{in: in, capability: capability, settings: settings}
}

// Call calls a method of the capability with in and decodes what it
// returns into out. Capabilities whose consumers want a Go interface wrap
// it, as the notifier does.
func (p *remote) Call(ctx context.Context, method string, in, out any) error {
	return p.in.client.Provide(ctx, p.capability, method, map[string]any{"settings": p.settings, "input": in}, out)
}

// Type and Send make an add-on that provides notifier a notification
// channel.
func (p *remote) Type() string { return p.in.instance }

// Send implements notify.Channel for add-ons that provide notifier.
func (p *remote) Send(ctx context.Context, msg notify.Message) error {
	return p.Call(ctx, "send", msg, nil)
}

var _ notify.Channel = (*remote)(nil)
