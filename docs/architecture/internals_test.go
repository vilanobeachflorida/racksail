package architecture

import (
	"bytes"
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/vilanobeachflorida/racksail/internal/agent"
	"github.com/vilanobeachflorida/racksail/internal/changeset"
	"github.com/vilanobeachflorida/racksail/internal/modules"
	"github.com/vilanobeachflorida/racksail/internal/modules/modulestest"
	"github.com/vilanobeachflorida/racksail/internal/reconcile"
	"github.com/vilanobeachflorida/racksail/internal/store"
	"github.com/vilanobeachflorida/racksail/internal/system"
	"github.com/vilanobeachflorida/racksail/internal/system/sim"
)

func init() {
	// What a DNS module allows from an init function of its own, as
	// internals.md says modules extend the agent's roots.
	agent.AllowWrite("/etc/knot")
}

// renderZone stands in for the zone file renderer the example's module
// would have.
func renderZone(spec ZoneSpec) []byte {
	return []byte("$ORIGIN " + spec.Name + ".\n@ 3600 IN SOA ns1." + spec.Name + ". hostmaster." + spec.Name + ". 1 7200 3600 1209600 3600\n")
}

// zoneModule is the module the example's kind and reconciler belong to.
type zoneModule struct{ modules.Base }

func (zoneModule) Kinds() []store.Kind { return []store.Kind{Zone} }

func (zoneModule) Reconcilers() []reconcile.Reconciler {
	return []reconcile.Reconciler{reconcile.Typed(Zone, zoneReconciler{})}
}

var zoneManifest = []byte(`id: dns
name: DNS
description: Zones served by Knot DNS, as internals.md's example keeps them.
version: 0.1.0
license: AGPL-3.0-or-later
category: hosting
dependencies: {}
profiles: [dns-server]
hardware: {arch: [amd64, arm64]}
cost: {idleMemoryMB: 5, activeMemoryMB: 40, diskMB: 15}
upstream: []
`)

// TestZoneExampleRuns runs internals.md's kind, reconciler and agent
// operation the way a module's run: a zone is previewed, created and
// deleted through the change pipeline, against a simulated server.
func TestZoneExampleRuns(t *testing.T) {
	ctx := context.Background()
	machine := sim.New(sim.Options{})
	var reloads atomic.Int32
	machine.SetProgram("/usr/sbin/knotc", func(ctx context.Context, m *sim.Machine, cmd system.Command) (system.Output, error) {
		if !slices.Equal(cmd.Args, []string{"zone-reload"}) {
			return system.Output{ExitCode: 1, Stderr: []byte("unexpected arguments")}, nil
		}
		reloads.Add(1)
		return system.Output{}, nil
	})
	e := modulestest.New(t, modulestest.Options{Machine: machine}, &zoneModule{Base: modules.NewBase(zoneManifest)})
	e.TurnOn(t, "dns")

	const file = "/etc/knot/zones/example.com.zone"
	create := changeset.Request{Title: "Create example.com", Changes: []changeset.Change{{
		Op: changeset.OpCreate, Kind: "dns.zone", Scope: store.AccountScope(store.NewID("acct")),
		Spec: modulestest.JSON(t, ZoneSpec{Name: "example.com"}),
	}}}

	// The preview runs the plan in the agent's check mode: it says what
	// applying would do and changes nothing.
	preview := create
	preview.DryRun = true
	cs, err := e.Submit(t, preview)
	if err != nil {
		t.Fatal(err)
	}
	var effects []string
	for _, eff := range cs.Effects {
		effects = append(effects, eff.Action+" "+eff.Target)
	}
	for _, want := range []string{"mkdir /etc/knot", "mkdir /etc/knot/zones", "write " + file, "reload knot.service"} {
		if !slices.Contains(effects, want) {
			t.Errorf("the preview doesn't say it will %s: %v", want, effects)
		}
	}
	if _, err := machine.Backend().Files.Stat(ctx, "/etc/knot"); !errors.Is(err, system.ErrNotFound) {
		t.Errorf("the preview changed the server: %v", err)
	}

	applied := e.MustApply(t, create)
	data, err := machine.Backend().Files.ReadFile(ctx, file, 0)
	if err != nil {
		t.Fatalf("applying didn't write the zone file: %v", err)
	}
	if !bytes.Equal(data, renderZone(ZoneSpec{Name: "example.com"})) {
		t.Errorf("zone file = %q", data)
	}
	if n := reloads.Load(); n != 1 {
		t.Errorf("knotc zone-reload ran %d times, want once", n)
	}
	id := applied.Changes[0].ID
	zone, err := Zone.Get(ctx, e.Store, id)
	if err != nil {
		t.Fatal(err)
	}
	if zone.Status.Phase != store.PhaseReady || !zone.Status.IsTrue("Ready") || zone.Status.ObservedGeneration != zone.Generation {
		t.Errorf("status = %+v", zone.Status)
	}

	// Planned again, every step finds its file in place and writes nothing.
	machine.ResetCalls()
	if err := e.Runner.Reconcile(ctx, id, nil); err != nil {
		t.Fatal(err)
	}
	for _, c := range machine.Calls() {
		if c.Area == "files" && strings.HasPrefix(c.Target, "/etc/knot") {
			t.Errorf("reconciling again changed the zone's files: %s", c)
		}
	}

	e.MustApply(t, changeset.Request{Title: "Delete example.com", Changes: []changeset.Change{{Op: changeset.OpDelete, Kind: "dns.zone", ID: id}}})
	if _, err := machine.Backend().Files.Stat(ctx, file); !errors.Is(err, system.ErrNotFound) {
		t.Errorf("deleting the zone left its file: %v", err)
	}
	if _, err := e.Store.Get(ctx, id); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("the deleted zone wasn't purged: %v", err)
	}
}

var goBlock = regexp.MustCompile("(?ms)^```go\n(.*?)\n```$")

// declarations lists the top-level names Go source declares, methods as
// Type.Method, or reports that it isn't a list of declarations.
func declarations(src string) ([]string, bool) {
	f, err := parser.ParseFile(token.NewFileSet(), "", src, parser.SkipObjectResolution)
	if err != nil {
		return nil, false
	}
	var names []string
	for _, d := range f.Decls {
		switch d := d.(type) {
		case *ast.FuncDecl:
			name := d.Name.Name
			if d.Recv != nil && len(d.Recv.List) > 0 {
				recv := d.Recv.List[0].Type
				if star, ok := recv.(*ast.StarExpr); ok {
					recv = star.X
				}
				if id, ok := recv.(*ast.Ident); ok {
					name = id.Name + "." + name
				}
			}
			names = append(names, name)
		case *ast.GenDecl:
			for _, s := range d.Specs {
				switch s := s.(type) {
				case *ast.TypeSpec:
					names = append(names, s.Name.Name)
				case *ast.ValueSpec:
					for _, n := range s.Names {
						names = append(names, n.Name)
					}
				}
			}
		}
	}
	return names, true
}

// TestExamplesAreShownAsTheyAre keeps examples_test.go and internals.md the
// same: every block of Go in internals.md that declares something the file
// declares shows it exactly as the file has it, and the file declares
// nothing internals.md doesn't show, so the code people copy is the code
// TestZoneExampleRuns runs.
func TestExamplesAreShownAsTheyAre(t *testing.T) {
	doc, err := os.ReadFile("internals.md")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("examples_test.go")
	if err != nil {
		t.Fatal(err)
	}
	src := strings.ReplaceAll(string(raw), "\r\n", "\n")
	declared, ok := declarations(src)
	if !ok || len(declared) == 0 {
		t.Fatal("examples_test.go declares nothing")
	}
	shown := map[string]bool{}
	for _, m := range goBlock.FindAllStringSubmatch(strings.ReplaceAll(string(doc), "\r\n", "\n"), -1) {
		block := m[1]
		names, ok := declarations("package architecture\n\n" + block)
		if !ok {
			// Statements and fragments, which only make sense in context.
			continue
		}
		var overlap []string
		for _, n := range names {
			if slices.Contains(declared, n) {
				overlap = append(overlap, n)
			}
		}
		if len(overlap) == 0 {
			continue
		}
		if !strings.Contains(src, block) {
			t.Errorf("internals.md shows %s differently from examples_test.go, which runs it; change both:\n%s", strings.Join(overlap, ", "), block)
		}
		for _, n := range names {
			shown[n] = true
		}
	}
	var missing []string
	for _, n := range declared {
		if !shown[n] {
			missing = append(missing, n)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("examples_test.go declares %s, which internals.md doesn't show", strings.Join(missing, ", "))
	}
}
