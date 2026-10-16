package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/store"
	"github.com/vilanobeachflorida/racksail/internal/system"
	"github.com/vilanobeachflorida/racksail/internal/system/sim"
)

func TestEveryCallIsAudited(t *testing.T) {
	ctx := context.Background()
	_, c := newLocal(t)
	site := store.NewID("site")
	in := WriteFileInput{Path: "/srv/racksail/accounts/a/index.html", Content: []byte("hello"), Mode: 0o644}
	if _, err := EnsureDir.Call(ctx, c, EnsureDirInput{Path: "/srv/racksail/accounts/a", Mode: 0o755, Parents: true}, CallOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteFile.Call(ctx, c, in, CallOptions{Owner: site}); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteFile.Call(ctx, c, in, CallOptions{Owner: site, Check: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteFile.Call(ctx, c, WriteFileInput{Path: "/etc/shadow", Mode: 0o644}, CallOptions{}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("a path outside the policy: %v", err)
	}
	if _, err := c.Call(ctx, "NoSuchOperation", json.RawMessage(`{}`), CallOptions{}); !errors.Is(err, ErrUnknownOperation) {
		t.Fatal(err)
	}

	records, err := c.Runtime().Audit().Recent(AuditFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 5 {
		t.Fatalf("%d records, want 5: %v", len(records), records)
	}
	// Newest first.
	if records[0].Op != "NoSuchOperation" || records[0].Code != string(ErrUnknownOperation.Code) {
		t.Errorf("unknown operation record = %+v", records[0])
	}
	if records[1].Op != "WriteFile" || records[1].Code != string(ErrInvalidInput.Code) || !slices.Contains(records[1].Targets, "/etc/shadow") {
		t.Errorf("refused record = %+v", records[1])
	}
	if r := records[2]; !r.Check || r.Changed || r.Owner != site {
		t.Errorf("check record = %+v", r)
	}
	if r := records[3]; r.Check || !r.Changed || r.Owner != site || r.Caller != CallerLocal || r.InputSHA256 == "" ||
		!slices.Contains(r.Targets, in.Path) || strings.Contains(r.String(), "hello") {
		t.Errorf("write record = %+v", r)
	}

	failed, _ := c.Runtime().Audit().Recent(AuditFilter{Failed: true})
	if len(failed) != 2 {
		t.Errorf("failed records = %v", failed)
	}
	mine, _ := c.Runtime().Audit().Recent(AuditFilter{Owner: site, Op: "WriteFile", Limit: 1})
	if len(mine) != 1 || !mine[0].Check {
		t.Errorf("filtered records = %v", mine)
	}

	trail, err := ReadAudit.Call(ctx, c, ReadAuditInput{Filter: AuditFilter{Op: "EnsureDir"}}, CallOptions{})
	if err != nil || len(trail.Records) != 1 {
		t.Errorf("ReadAudit = %+v, %v", trail, err)
	}
	if _, err := ReadAudit.Call(ctx, c, ReadAuditInput{Filter: AuditFilter{Limit: -1}}, CallOptions{}); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("a negative limit: %v", err)
	}
}

func TestSensitiveInputsAreNotHashed(t *testing.T) {
	ctx := context.Background()
	_, c := newLocal(t)
	if _, err := SealCredential.Call(ctx, c, SealCredentialInput{Name: "racksail-test", Value: []byte("secret value")}, CallOptions{}); err != nil {
		t.Fatal(err)
	}
	records, _ := c.Runtime().Audit().Recent(AuditFilter{Op: "SealCredential"})
	if len(records) != 1 || records[0].InputSHA256 != "" || !records[0].Changed {
		t.Errorf("record = %+v", records)
	}
}

func TestFileAudit(t *testing.T) {
	p := filepath.Join(t.TempDir(), "audit.jsonl")
	a, err := OpenFileAudit(p, 600)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	for i := range 8 {
		r := AuditRecord{Time: start.Add(time.Duration(i) * time.Minute), Op: "WriteFile", Caller: CallerLocal, Targets: []string{"/srv/racksail/f"}, Changed: i%2 == 0}
		if i == 7 {
			r.Code, r.Error = "agent.operation_failed", "disk full"
		}
		if err := a.Record(r); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(p + ".1"); err != nil {
		t.Errorf("the trail should have been rotated: %v", err)
	}
	all, err := a.Recent(AuditFilter{})
	if err != nil || len(all) == 0 || !all[0].Time.Equal(start.Add(7*time.Minute)) {
		t.Fatalf("recent = %v, %v", all, err)
	}
	for i := 1; i < len(all); i++ {
		if all[i].Time.After(all[i-1].Time) {
			t.Fatal("records must be newest first across files")
		}
	}
	failed, _ := a.Recent(AuditFilter{Failed: true})
	if len(failed) != 1 || failed[0].Error != "disk full" {
		t.Errorf("failed = %v", failed)
	}
	since, _ := a.Recent(AuditFilter{Since: start.Add(6 * time.Minute)})
	if len(since) != 2 {
		t.Errorf("since = %v", since)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	// Records survive a restart.
	b, err := OpenFileAudit(p, 600)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	again, _ := b.Recent(AuditFilter{Limit: 3})
	if len(again) != 3 || !again[0].Time.Equal(all[0].Time) {
		t.Errorf("after reopening = %v", again)
	}
	// A trail removed under the agent starts again where it can be found,
	// rather than going on in a file nobody can open.
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if err := b.Record(AuditRecord{Time: start.Add(time.Hour), Op: "ReadFile"}); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(p); err != nil || !strings.Contains(string(data), `"op":"ReadFile"`) {
		t.Errorf("the trail after it was removed = %q, %v", data, err)
	}
}

func TestFileAuditNeverFollowsALink(t *testing.T) {
	dir := t.TempDir()
	elsewhere := filepath.Join(dir, "elsewhere")
	if err := os.WriteFile(elsewhere, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "audit.jsonl")
	if err := os.Symlink(elsewhere, p); err != nil {
		t.Fatal(err)
	}
	if a, err := OpenFileAudit(p, 0); err == nil {
		a.Close()
		t.Error("opening a trail that is a link must fail")
	}
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	a, err := OpenFileAudit(p, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	// A link swapped in afterwards isn't written through either.
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, p); err != nil {
		t.Fatal(err)
	}
	if err := a.Record(AuditRecord{Op: "WriteFile"}); err == nil {
		t.Error("recording through a link must fail")
	}
	if data, _ := os.ReadFile(elsewhere); len(data) != 0 {
		t.Errorf("the link's target got %q", data)
	}
	if _, err := readAuditFile(p); err == nil {
		t.Error("reading the trail through a link must fail")
	}
}

// A rollback that fails, such as when networkd can't be reached for a
// moment, is tried again until the previous configuration is back.
func TestNetworkRollbackIsRetried(t *testing.T) {
	ctx := context.Background()
	m := sim.New(sim.Options{Hostname: "retry"})
	b := m.Backend()
	before, _ := b.Network.Config(ctx)
	rt := NewRuntime(b, RuntimeOptions{RollbackRetry: 20 * time.Millisecond})
	c := NewLocalRuntime(rt)
	events, stop := rt.Subscribe()
	defer stop()
	in := NetworkConfigInput{Files: map[string]string{"50-racksail-enp1s0.network": "[Match]\nName=enp1s0\n[Network]\nAddress=203.0.113.9/24\n"}, ConfirmWithin: 1}
	res, err := ApplyNetworkConfig.Call(ctx, c, in, CallOptions{})
	if err != nil || res.Pending == nil {
		t.Fatalf("apply = %+v, %v", res, err)
	}
	for range 3 {
		m.FailNext("network.apply_config", "", errors.New("networkctl: connection refused"))
	}
	select {
	case e := <-events:
		if e.Type != EventNetworkRolledBack {
			t.Errorf("event = %+v", e)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the rollback wasn't tried again")
	}
	after, _ := b.Network.Config(ctx)
	if len(after) != len(before) {
		t.Errorf("config after the rollback = %v, before %v", after, before)
	}
	attempts := 0
	for _, call := range m.Calls() {
		if call.Area == "network" && call.Op == "apply_config" {
			attempts++
		}
	}
	if attempts != 5 {
		t.Errorf("apply attempts = %d, want the change and four rollbacks", attempts)
	}
}

func TestLateConfirmationWhileRollingBack(t *testing.T) {
	ctx := context.Background()
	m := sim.New(sim.Options{Hostname: "late"})
	b := m.Backend()
	now := time.Now()
	var mu sync.Mutex
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	rt := NewRuntime(b, RuntimeOptions{Clock: clock, RollbackRetry: time.Hour})
	c := NewLocalRuntime(rt)
	in := NetworkConfigInput{Files: map[string]string{"50-racksail-enp1s0.network": "[Match]\nName=enp1s0\n"}, ConfirmWithin: 60}
	res, err := ApplyNetworkConfig.Call(ctx, c, in, CallOptions{})
	if err != nil || res.Pending == nil {
		t.Fatalf("apply = %+v, %v", res, err)
	}
	// The deadline passes while the rollback can't be done yet.
	mu.Lock()
	now = now.Add(2 * time.Minute)
	mu.Unlock()
	_, err = ConfirmNetworkConfig.Call(ctx, c, ConfirmNetworkInput{Token: res.Pending.Token}, CallOptions{})
	if !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "ran out") {
		t.Errorf("confirming after the deadline: %v", err)
	}
}

func TestDrift(t *testing.T) {
	ctx := context.Background()
	m, c := newLocal(t)
	site := store.NewID("site")
	conf := WriteFileInput{Path: "/etc/racksail/generated/site.conf", Content: []byte("listen 80;\n"), Mode: 0o644}
	if _, err := WriteFile.Call(ctx, c, conf, CallOptions{Owner: site}); err != nil {
		t.Fatal(err)
	}
	newAccount(t, c, "blog", 20001)
	unit := EnsureUnitInput{Name: "racksail-site-blog.service", Content: sandboxed("[Service]\nUser=blog\nExecStart=/bin/true\n")}
	if _, err := EnsureUnit.Call(ctx, c, unit, CallOptions{Owner: site}); err != nil {
		t.Fatal(err)
	}
	gone := WriteFileInput{Path: "/etc/racksail/generated/gone.conf", Content: []byte("x"), Mode: 0o644}
	if _, err := WriteFile.Call(ctx, c, gone, CallOptions{Owner: site}); err != nil {
		t.Fatal(err)
	}
	report, err := CheckDrift.Call(ctx, c, Empty{}, CallOptions{})
	if err != nil || len(report.Drift) != 0 {
		t.Fatalf("no drift yet: %+v, %v", report, err)
	}

	// Someone edits one file by hand and removes another.
	b := m.Backend()
	if err := b.Files.WriteFile(ctx, conf.Path, []byte("listen 8080;\n"), 0o644, system.Owner{}); err != nil {
		t.Fatal(err)
	}
	if err := b.Files.Remove(ctx, gone.Path); err != nil {
		t.Fatal(err)
	}
	report, err = CheckDrift.Call(ctx, c, Empty{}, CallOptions{})
	if err != nil {
		t.Fatal(err)
	}
	want := []Drift{
		{Owner: site, Kind: "file", Target: gone.Path, Reason: "missing"},
		{Owner: site, Kind: "file", Target: conf.Path, Reason: "changed"},
	}
	if !slices.Equal(report.Drift, want) {
		t.Errorf("drift = %+v, want %+v", report.Drift, want)
	}

	// Writing it again through the agent puts it right, and removing what
	// was written drops it from the ledger.
	if _, err := WriteFile.Call(ctx, c, conf, CallOptions{Owner: site}); err != nil {
		t.Fatal(err)
	}
	if _, err := RemovePath.Call(ctx, c, RemovePathInput{Path: gone.Path}, CallOptions{}); err != nil {
		t.Fatal(err)
	}
	if report, _ = CheckDrift.Call(ctx, c, Empty{}, CallOptions{}); len(report.Drift) != 0 {
		t.Errorf("after reconciling: %+v", report.Drift)
	}

	// The ledger lives on the machine, so a new runtime sees the same.
	if err := b.Files.WriteFile(ctx, conf.Path, []byte("changed again\n"), 0o644, system.Owner{}); err != nil {
		t.Fatal(err)
	}
	fresh := NewLocal(b, nil)
	report, err = CheckDrift.Call(ctx, fresh, Empty{}, CallOptions{})
	if err != nil || len(report.Drift) != 1 || report.Drift[0].Target != conf.Path {
		t.Errorf("drift after a restart = %+v, %v", report, err)
	}
}

func TestDriftEvents(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := sim.New(sim.Options{Hostname: "drift"})
	rt := NewRuntime(m.Backend(), RuntimeOptions{DriftInterval: 20 * time.Millisecond})
	c := NewLocalRuntime(rt)
	path := "/etc/racksail/generated/x.conf"
	if _, err := WriteFile.Call(ctx, c, WriteFileInput{Path: path, Content: []byte("a"), Mode: 0o644}, CallOptions{}); err != nil {
		t.Fatal(err)
	}
	events, stop := rt.Subscribe()
	defer stop()
	if err := rt.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if err := m.Backend().Files.WriteFile(ctx, path, []byte("b"), 0o644, system.Owner{}); err != nil {
		t.Fatal(err)
	}
	select {
	case e := <-events:
		if e.Type != EventDrift || e.Drift == nil || e.Drift.Target != path || e.Drift.Reason != "changed" {
			t.Errorf("event = %+v", e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no drift event")
	}
	// The same drift is reported once, however many checks find it.
	select {
	case e := <-events:
		t.Errorf("drift reported again: %+v", e)
	case <-time.After(200 * time.Millisecond):
	}
	// A panel that listens later still learns about it.
	if current := rt.CurrentDrift(); len(current) != 1 || current[0].Target != path {
		t.Errorf("current drift = %+v", current)
	}
	late := make(chan Event, 4)
	lateCtx, stopLate := context.WithCancel(ctx)
	go c.Events(lateCtx, func(e Event) { late <- e })
	select {
	case e := <-late:
		if e.Drift == nil || e.Drift.Target != path {
			t.Errorf("late event = %+v", e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a late listener didn't hear about the drift")
	}
	stopLate()
	// Put right and drifting again, it is reported again.
	if _, err := WriteFile.Call(ctx, c, WriteFileInput{Path: path, Content: []byte("a"), Mode: 0o644}, CallOptions{}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(rt.CurrentDrift()) != 0 {
		if time.Now().After(deadline) {
			t.Fatal("the drift never cleared")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := m.Backend().Files.Remove(ctx, path); err != nil {
		t.Fatal(err)
	}
	select {
	case e := <-events:
		if e.Drift == nil || e.Drift.Reason != "missing" {
			t.Errorf("event = %+v", e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the new drift wasn't reported")
	}
}

// Files are only read when their size or time changed, but a rolling full
// comparison catches edits that kept both, and a file whose time alone
// changed isn't drift.
func TestDriftComparesContentsInTurn(t *testing.T) {
	ctx := context.Background()
	root := linuxRoot(t)
	c := NewLocal(linuxBackend(root), nil)
	rt := c.Runtime()
	conf := "/srv/racksail/accounts/a/www/site.conf"
	if _, err := WriteFile.Call(ctx, c, WriteFileInput{Path: conf, Content: []byte("listen 80;\n"), Mode: 0o644}, CallOptions{Owner: store.NewID("site")}); err != nil {
		t.Fatal(err)
	}
	host := filepath.Join(root, conf)
	info, err := os.Stat(host)
	if err != nil {
		t.Fatal(err)
	}
	// Only the time changes: not drift.
	later := info.ModTime().Add(time.Hour)
	if err := os.Chtimes(host, later, later); err != nil {
		t.Fatal(err)
	}
	if drift, err := rt.CheckDrift(ctx); err != nil || len(drift) != 0 {
		t.Fatalf("a touched file = %+v, %v", drift, err)
	}
	// Same size, same time, other contents.
	if err := os.WriteFile(host, []byte("listen 81;\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(host, later, later); err != nil {
		t.Fatal(err)
	}
	drift, err := rt.CheckDrift(ctx)
	if err != nil || len(drift) != 1 || drift[0].Reason != "changed" {
		t.Errorf("an edit that kept the size and time = %+v, %v", drift, err)
	}
	// The ledger keeps one file per target.
	entries, err := os.ReadDir(filepath.Join(root, system.AgentStateDir, ledgerDir))
	if err != nil || len(entries) != 1 {
		t.Errorf("ledger files = %v, %v", entries, err)
	}
	if _, err := os.Stat(filepath.Join(root, system.AgentStateDir, "ledger.json")); err == nil {
		t.Error("the ledger isn't kept as one file")
	}
}

// eventsOnce is an event source whose stream ends after one event.
type eventsOnce struct{ calls atomic.Int32 }

func (e *eventsOnce) Events(ctx context.Context, fn func(Event)) error {
	e.calls.Add(1)
	fn(Event{Type: EventNetworkConfirmed})
	return errors.New("the agent went away")
}

func TestFollowEventsReconnects(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	src := &eventsOnce{}
	got := make(chan Event, 8)
	done := make(chan struct{})
	go func() {
		defer close(done)
		FollowEvents(ctx, src, func(e Event) { got <- e }, nil)
	}()
	for range 2 {
		select {
		case <-got:
		case <-time.After(10 * time.Second):
			t.Fatal("the stream wasn't followed again")
		}
	}
	cancel()
	<-done
	if src.calls.Load() < 2 {
		t.Errorf("calls = %d", src.calls.Load())
	}
}

func TestNetworkChangesRollBackUnlessConfirmed(t *testing.T) {
	ctx := context.Background()
	m, c := newLocal(t)
	b := m.Backend()
	before, err := b.Network.Config(ctx)
	if err != nil {
		t.Fatal(err)
	}
	events, stop := c.Runtime().Subscribe()
	defer stop()

	bad := NetworkConfigInput{Files: map[string]string{"50-racksail-enp1s0.network": "[Match]\nName=enp1s0\n[Network]\nAddress=203.0.113.9/24\n"}, ConfirmWithin: 1}
	res, err := ApplyNetworkConfig.Call(ctx, c, bad, CallOptions{})
	if err != nil || !res.Changed || res.Pending == nil || len(res.Pending.Token) != 32 {
		t.Fatalf("apply = %+v, %v", res, err)
	}
	status, _ := GetNetworkChange.Call(ctx, c, Empty{}, CallOptions{})
	if status.Pending == nil || status.Pending.Token != res.Pending.Token {
		t.Errorf("status = %+v", status)
	}
	if _, err := ApplyNetworkConfig.Call(ctx, c, NetworkConfigInput{Files: map[string]string{"50-racksail-x.network": "[Match]\nName=x\n"}, ConfirmWithin: 30}, CallOptions{}); !errors.Is(err, ErrConflict) {
		t.Errorf("a second change while one is pending: %v", err)
	}
	select {
	case e := <-events:
		if e.Type != EventNetworkRolledBack {
			t.Errorf("event = %+v", e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the change wasn't rolled back")
	}
	after, _ := b.Network.Config(ctx)
	if len(after) != len(before) {
		t.Errorf("config after rollback = %v, before %v", after, before)
	}
	if status, _ := GetNetworkChange.Call(ctx, c, Empty{}, CallOptions{}); status.Pending != nil {
		t.Errorf("still pending: %+v", status)
	}
	// A confirmation that comes after the rollback isn't taken for one that
	// worked, in check mode either.
	for _, check := range []bool{true, false} {
		if _, err := ConfirmNetworkConfig.Call(ctx, c, ConfirmNetworkInput{Token: res.Pending.Token}, CallOptions{Check: check}); !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "rolled back") {
			t.Errorf("confirming too late (check %v): %v", check, err)
		}
	}
	if _, err := ConfirmNetworkConfig.Call(ctx, c, ConfirmNetworkInput{Token: strings.Repeat("a", 32)}, CallOptions{}); !errors.Is(err, ErrConflict) {
		t.Errorf("confirming with nothing waiting: %v", err)
	}

	good := NetworkConfigInput{Files: map[string]string{"50-racksail-enp1s0.network": "[Match]\nName=enp1s0\n[Network]\nDHCP=yes\n"}, ConfirmWithin: 60}
	res, err = ApplyNetworkConfig.Call(ctx, c, good, CallOptions{})
	if err != nil || res.Pending == nil {
		t.Fatalf("apply = %+v, %v", res, err)
	}
	if _, err := ConfirmNetworkConfig.Call(ctx, c, ConfirmNetworkInput{Token: strings.Repeat("0", 32)}, CallOptions{}); !errors.Is(err, ErrConflict) {
		t.Errorf("a wrong token: %v", err)
	}
	if _, err := ConfirmNetworkConfig.Call(ctx, c, ConfirmNetworkInput{Token: "nope"}, CallOptions{}); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("a malformed token: %v", err)
	}
	conf, err := ConfirmNetworkConfig.Call(ctx, c, ConfirmNetworkInput{Token: res.Pending.Token}, CallOptions{})
	if err != nil || !conf.Changed {
		t.Fatalf("confirm = %+v, %v", conf, err)
	}
	if conf, _ := ConfirmNetworkConfig.Call(ctx, c, ConfirmNetworkInput{Token: res.Pending.Token}, CallOptions{}); conf.Changed {
		t.Error("confirming twice changes nothing")
	}
	after, _ = b.Network.Config(ctx)
	if string(after["50-racksail-enp1s0.network"]) != good.Files["50-racksail-enp1s0.network"] {
		t.Errorf("the confirmed change must stay: %v", after)
	}
}

func TestPendingNetworkChangeSurvivesARestart(t *testing.T) {
	ctx := context.Background()
	m := sim.New(sim.Options{Hostname: "restart"})
	b := m.Backend()
	before, _ := b.Network.Config(ctx)
	first := NewLocal(b, nil)
	in := NetworkConfigInput{Files: map[string]string{"50-racksail-enp1s0.network": "[Match]\nName=enp1s0\n"}, ConfirmWithin: 600}
	if _, err := ApplyNetworkConfig.Call(ctx, first, in, CallOptions{}); err != nil {
		t.Fatal(err)
	}
	// The agent restarts after the deadline passed: the next run rolls back
	// as soon as it starts.
	later := NewRuntime(b, RuntimeOptions{Clock: func() time.Time { return time.Now().Add(time.Hour) }})
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	if err := later.Start(runCtx); err != nil {
		t.Fatal(err)
	}
	after, _ := b.Network.Config(ctx)
	if len(after) != len(before) {
		t.Errorf("config after recovery = %v", after)
	}
	if st, _ := GetNetworkChange.Call(ctx, NewLocalRuntime(later), Empty{}, CallOptions{}); st.Pending != nil {
		t.Errorf("pending after recovery: %+v", st)
	}
	// A change still within its time gets its timer back and can be
	// confirmed by the new run.
	res, err := ApplyNetworkConfig.Call(ctx, NewLocalRuntime(later), in, CallOptions{})
	if err != nil {
		t.Fatal(err)
	}
	third := NewRuntime(b, RuntimeOptions{})
	if err := third.Start(runCtx); err != nil {
		t.Fatal(err)
	}
	if conf, err := ConfirmNetworkConfig.Call(ctx, NewLocalRuntime(third), ConfirmNetworkInput{Token: res.Pending.Token}, CallOptions{}); err != nil || !conf.Changed {
		t.Errorf("confirming after a restart = %+v, %v", conf, err)
	}
}

func TestAccountIDsAreReserved(t *testing.T) {
	ctx := context.Background()
	_, c := newLocal(t)
	a, err := AllocateAccountID.Call(ctx, c, AllocateAccountIDInput{}, CallOptions{})
	if err != nil {
		t.Fatal(err)
	}
	b, err := AllocateAccountID.Call(ctx, c, AllocateAccountIDInput{Exclude: []int{a.UID + 1}}, CallOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if a.UID < system.AccountUIDMin || a.UID > system.AccountUIDMax || b.UID == a.UID || b.UID == a.UID+1 {
		t.Errorf("ids = %d, %d", a.UID, b.UID)
	}
	if a.SubIDs.Start == b.SubIDs.Start || a.SubIDs.Count == 0 {
		t.Errorf("sub ids = %+v, %+v", a.SubIDs, b.SubIDs)
	}
	// Using the first ID releases its reservation; it stays taken because
	// the user exists.
	if _, err := EnsureGroup.Call(ctx, c, EnsureGroupInput{Name: "shop", GID: a.UID}, CallOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureUser.Call(ctx, c, EnsureUserInput{Name: "shop", UID: a.UID, GID: a.UID, Home: "/srv/racksail/accounts/shop"}, CallOptions{}); err != nil {
		t.Fatal(err)
	}
	next, _ := AllocateAccountID.Call(ctx, c, AllocateAccountIDInput{}, CallOptions{})
	if next.UID == a.UID || next.UID == b.UID {
		t.Errorf("an ID in use or reserved was handed out again: %d", next.UID)
	}
	// Reservations outlive the agent: a new run doesn't hand them out.
	again := NewLocal(c.Runtime().backend, nil)
	fresh, err := AllocateAccountID.Call(ctx, again, AllocateAccountIDInput{}, CallOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if fresh.UID == b.UID || fresh.UID == next.UID {
		t.Errorf("a restarted agent handed out a reserved ID again: %d", fresh.UID)
	}
	// They lapse after a while.
	later := NewLocalRuntime(NewRuntime(c.Runtime().backend, RuntimeOptions{Clock: func() time.Time { return time.Now().Add(reservationTime + time.Minute) }}))
	if lapsed, _ := AllocateAccountID.Call(ctx, later, AllocateAccountIDInput{}, CallOptions{}); lapsed.UID != b.UID && lapsed.UID != next.UID && lapsed.UID != fresh.UID {
		t.Errorf("a lapsed reservation should be free again, got %d", lapsed.UID)
	}
}

func TestEventsOverHTTP(t *testing.T) {
	m := sim.New(sim.Options{Hostname: "events"})
	srv := NewServer(m.Backend(), "test", nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	client := NewHTTPClient(ts.URL, ts.Client())

	// The stream ends before the server closes, which waits for it.
	ctx, cancel := context.WithCancel(context.Background())
	got := make(chan Event, 4)
	ended := make(chan error, 1)
	go func() { ended <- client.Events(ctx, func(e Event) { got <- e }) }()
	defer func() {
		cancel()
		<-ended
	}()
	// The stream is open once the server has a subscriber.
	deadline := time.Now().Add(5 * time.Second)
	for {
		srv.Runtime().events.mu.Lock()
		n := len(srv.Runtime().events.subs)
		srv.Runtime().events.mu.Unlock()
		if n > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the event stream never opened")
		}
		time.Sleep(10 * time.Millisecond)
	}
	srv.Runtime().events.publish(Event{Type: EventDrift, Time: time.Now(), Drift: &Drift{Kind: "file", Target: "/etc/racksail/x", Reason: "missing"}})
	select {
	case e := <-got:
		if e.Type != EventDrift || e.Drift.Target != "/etc/racksail/x" {
			t.Errorf("event = %+v", e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no event arrived")
	}
}

func TestRefusedRequestsAreAudited(t *testing.T) {
	m := sim.New(sim.Options{Hostname: "refusals"})
	srv := NewServer(m.Backend(), "test", nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	for _, body := range []string{`{"path": 12}`, `not json`} {
		resp, err := http.Post(ts.URL+"/v1/ops/WriteFile", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode < 400 {
			t.Errorf("%s was accepted", body)
		}
	}
	resp, err := http.Post(ts.URL+"/v1/ops/NoSuchOperation", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	records, _ := srv.Runtime().Audit().Recent(AuditFilter{Failed: true})
	if len(records) != 3 {
		t.Fatalf("records = %v", records)
	}
	if records[0].Op != "NoSuchOperation" || records[1].Op != "WriteFile" || records[2].Op != "WriteFile" {
		t.Errorf("records = %v", records)
	}
}

func TestEventStreamIsDocumented(t *testing.T) {
	doc, err := NewServer(nil, "test", nil).OpenAPI()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"/v1/events"`, `"/v1/streams/SendSnapshot"`, `"/v1/ops/SealCredential"`} {
		if !strings.Contains(string(doc), want) {
			t.Errorf("the protocol document lacks %s", want)
		}
	}
}
