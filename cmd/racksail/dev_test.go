package main

import (
	"context"
	"crypto/tls"
	"io"
	"log/slog"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/agent"
	"github.com/vilanobeachflorida/racksail/internal/config"
	"github.com/vilanobeachflorida/racksail/internal/edge"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

// The panel, the simulated agent and the edge run in one process, as
// racksail dev runs them: a module's route goes through the panel's edge
// router, its files through the agent, and the page comes out of the edge.
func TestPanelAgentAndEdgeInOneProcess(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dir := shortTemp(t)
	cfg, err := config.Dev(dir, config.DevAddresses{Panel: "127.0.0.1:0", EdgeHTTP: "127.0.0.1:0", EdgeHTTPS: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	d, err := newDevServer(ctx, cfg, dir, false, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	defer d.close(context.Background())

	// A site's files arrive through the agent, as a module's reconciler
	// writes them.
	root := "/srv/racksail/accounts/demo/sites/www"
	steps := []agent.Step{
		agent.EnsureDir.Step(agent.EnsureDirInput{Path: root, Mode: 0o755, Parents: true}),
		agent.WriteFile.Step(agent.WriteFileInput{Path: root + "/index.html", Content: []byte("<h1>Hello from the simulated server</h1>"), Mode: 0o644}),
	}
	if _, err := agent.Execute(ctx, d.core.agent, steps, agent.CallOptions{}, nil); err != nil {
		t.Fatal(err)
	}
	if err := d.machine.Flush(); err != nil {
		t.Fatal(err)
	}
	// The route goes through the edge router modules get from the panel.
	site := store.NewID("site")
	if err := d.core.services.Edge.SetRoutes(ctx, site, []edge.Route{{
		ID: "main", Hosts: []string{"demo.localhost"},
		Handler: edge.Handler{Type: edge.HandleStatic, Root: root},
	}}); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(15 * time.Second)
	for !d.edge.Running() {
		if time.Now().After(deadline) {
			t.Fatalf("the edge never started: %+v", d.edge.Health())
		}
		time.Sleep(20 * time.Millisecond)
	}
	_, httpsAddr := d.edge.Addrs()
	client := &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, network, httpsAddr)
		},
		// The local CA signed the certificate; the handshake still has to
		// produce one for the route's name.
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}}
	resp, err := client.Get("https://demo.localhost/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(body) != "<h1>Hello from the simulated server</h1>" {
		t.Fatalf("GET = %d %q", resp.StatusCode, body)
	}
	if names := resp.TLS.PeerCertificates[0].DNSNames; len(names) != 1 || names[0] != "demo.localhost" {
		t.Errorf("certificate names = %v", names)
	}
	// The panel's own route waits passively for sites; with one, it serves.
	if routes, _ := d.core.services.Edge.Routes(ctx); len(routes) != 2 {
		t.Errorf("routes = %+v", routes)
	}
}
