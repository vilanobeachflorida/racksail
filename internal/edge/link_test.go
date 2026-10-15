package edge

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/store"
)

// countingAdmin serves an edge's admin API on a socket and counts the
// requests, which on a server would each start the edge.
func countingAdmin(t *testing.T, e *Edge) (*AdminClient, *atomic.Int64) {
	t.Helper()
	sock := filepath.Join(shortTemp(t), "edge.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	var n atomic.Int64
	admin := e.AdminHandler()
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		admin.ServeHTTP(w, r)
	})}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return DialAdmin(sock), &n
}

func TestLinkReachesTheEdgeOnlyWhenItIsNeeded(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e, err := New(Options{StateDir: t.TempDir(), HTTP: "127.0.0.1:0", HTTPS: "127.0.0.1:0", Log: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	client, requests := countingAdmin(t, e)
	var mu sync.Mutex
	running := false
	var boots []bool
	panel, err := PanelRoute("panel.example.test", "127.0.0.1:8443", true)
	if err != nil {
		t.Fatal(err)
	}
	link := NewLink(ctx, LinkOptions{
		Client: client, Responder: &fakePanel{allowed: map[string]bool{}}, Panel: &panel, Log: slog.New(slog.DiscardHandler),
		Running: func(context.Context) (bool, error) {
			mu.Lock()
			defer mu.Unlock()
			return running, nil
		},
		Boot: func(_ context.Context, start bool) error {
			mu.Lock()
			defer mu.Unlock()
			boots = append(boots, start)
			return nil
		},
	})
	defer link.Close()

	// Nothing the edge serves: changes wait and the edge isn't touched.
	passive, gone, site := store.NewID("site"), store.NewID("site"), store.NewID("site")
	if err := link.SetRoutes(ctx, passive, []Route{{ID: "p", Hosts: []string{"p.test"}, Passive: true, Handler: Handler{Type: HandleRespond}}}); err != nil {
		t.Fatal(err)
	}
	if err := link.DeleteRoutes(ctx, gone); err != nil {
		t.Fatal(err)
	}
	if routes, err := link.Routes(ctx); err != nil || len(routes) != 0 {
		t.Errorf("routes of a stopped edge = %+v, %v", routes, err)
	}
	if err := link.SetRoutes(ctx, site, []Route{{ID: "bad", Hosts: []string{"bad host"}}}); err == nil {
		t.Error("a bad route is refused before it waits")
	}
	if n := requests.Load(); n != 0 || link.Answering() || link.Active() {
		t.Fatalf("the edge was reached %d times before anything needed it", n)
	}

	// The first route the edge serves sends everything and starts answering.
	if err := link.SetRoutes(ctx, site, []Route{{ID: "main", Hosts: []string{"site.test"}, Handler: Handler{Type: HandleRespond}}}); err != nil {
		t.Fatal(err)
	}
	if !link.Active() || !link.Answering() || requests.Load() == 0 {
		t.Fatal("the first route should reach the edge and start answering")
	}
	served, _ := e.table.Routes(ctx)
	owners := map[store.ID]bool{}
	for _, r := range served {
		owners[r.Owner] = true
	}
	if !owners[passive] || !owners[site] || !owners[PanelOwner] || e.table.Active() != 1 {
		t.Errorf("the edge's table = %+v", served)
	}
	deadline := time.Now().Add(5 * time.Second)
	for !e.Health().Answered {
		if time.Now().After(deadline) {
			t.Fatal("the panel never polled for questions")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// The last one stops it all again.
	if err := link.DeleteRoutes(ctx, site); err != nil {
		t.Fatal(err)
	}
	if link.Active() || link.Answering() {
		t.Error("without routes the link stops answering")
	}
	served, _ = e.table.Routes(ctx)
	for _, r := range served {
		if r.Owner == PanelOwner {
			t.Error("the panel's route stays only while sites are routed")
		}
	}
	mu.Lock()
	if len(boots) != 2 || !boots[0] || boots[1] {
		t.Errorf("boot = %v", boots)
	}
	mu.Unlock()
	link.Close()
	before := requests.Load()
	if err := link.SetRoutes(ctx, passive, nil); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != before {
		t.Error("a stopped edge isn't reached for a change it doesn't need")
	}

	// A running edge gets changes at once.
	mu.Lock()
	running = true
	mu.Unlock()
	if err := link.SetRoutes(ctx, passive, []Route{{ID: "p", Hosts: []string{"p.test"}, Passive: true, Handler: Handler{Type: HandleRespond}}}); err != nil {
		t.Fatal(err)
	}
	if requests.Load() == before {
		t.Error("a running edge should get changes at once")
	}
}

func TestIdleEdgeExits(t *testing.T) {
	dir := shortTemp(t)
	t.Setenv("XDG_DATA_HOME", dir)
	e, err := New(Options{StateDir: filepath.Join(dir, "state"), HTTP: "127.0.0.1:0", HTTPS: "127.0.0.1:0", Dev: true,
		IdleExit: 300 * time.Millisecond, Log: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", filepath.Join(dir, "edge.sock"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- e.Run(ctx, ln) }()
	client := DialAdmin(filepath.Join(dir, "edge.sock"))
	// A route keeps the edge running past its idle time.
	owner := store.NewID("site")
	if err := client.SetRoutes(ctx, owner, []Route{{ID: "main", Hosts: []string{"site.test"}, Handler: Handler{Type: HandleRespond}}}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		t.Fatalf("an edge with routes exited: %v", err)
	case <-time.After(time.Second):
	}
	if err := client.DeleteRoutes(ctx, owner); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("run = %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("an edge with nothing to serve kept running")
	}
	if e.Running() {
		t.Error("Caddy should have stopped")
	}
}
