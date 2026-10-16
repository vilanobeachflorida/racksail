package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/agent"
	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/auth/identity"
	"github.com/vilanobeachflorida/racksail/internal/changeset"
	"github.com/vilanobeachflorida/racksail/internal/cli"
	"github.com/vilanobeachflorida/racksail/internal/config"
	"github.com/vilanobeachflorida/racksail/internal/edge"
	"github.com/vilanobeachflorida/racksail/internal/events"
	"github.com/vilanobeachflorida/racksail/internal/inbox"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

// The panel, the simulated agent and the edge run in one process, as
// racksail dev runs them: a module's route goes through the panel's edge
// router, its files through the agent, and the page comes out of the edge.
func TestPanelAgentAndEdgeInOneProcess(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dir := shortTemp(t)
	t.Setenv("XDG_DATA_HOME", dir)
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

	// The attention inbox asks the edge in the same process about its
	// certificates, and suggests Storage for the simulated server's empty
	// disks.
	lister, ok := d.core.services.Edge.(certificateLister)
	if !ok {
		t.Fatal("the dev edge router lists no certificates")
	}
	certs, err := lister.Certificates(ctx)
	if err != nil || !slices.ContainsFunc(certs, func(c edge.Certificate) bool { return c.Subject == "demo.localhost" }) {
		t.Errorf("certificates = %+v, %v", certs, err)
	}
	res, err := d.core.inbox.List(ctx, inbox.Query{All: true}, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(res.Items, func(it inbox.Item) bool { return it.ID == "suggestion:storage" }) {
		t.Errorf("inbox = %+v", res)
	}
}

// What racksail dev's developer does is recorded with the address the
// request came from, without its ephemeral port, and with the surface it
// came through: the panel's pages, the racksail command or another program.
func TestDevRecordsWhereRequestsComeFrom(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dir := shortTemp(t)
	t.Setenv("XDG_DATA_HOME", dir)
	cfg, err := config.Dev(dir, config.DevAddresses{Panel: "127.0.0.1:0", EdgeHTTP: "127.0.0.1:0", EdgeHTTPS: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	d, err := newDevServer(ctx, cfg, dir, false, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	defer d.close(context.Background())
	dev, err := d.core.identity.Developer(ctx)
	if err != nil {
		t.Fatal(err)
	}
	apiServer, err := d.core.apiServer(identity.DevAuthenticator{Service: d.core.identity, User: dev}, true)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(apiServer.Handler())
	defer srv.Close()
	defer apiServer.Close()
	for i, tc := range []struct {
		header map[string]string
		want   auth.Source
	}{
		{map[string]string{"Sec-Fetch-Site": "same-origin", "User-Agent": "Mozilla/5.0"}, auth.SourceUI},
		{map[string]string{"User-Agent": cli.UserAgent}, auth.SourceCLI},
		{map[string]string{"User-Agent": "curl/8.14.1"}, auth.SourceAPI},
	} {
		req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/v1/me", strings.NewReader(`{"name":"Developer `+strconv.Itoa(i)+`"}`))
		req.Header.Set("Content-Type", "application/json")
		for k, v := range tc.header {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		var cs changeset.ChangeSet
		err = json.NewDecoder(resp.Body).Decode(&cs)
		resp.Body.Close()
		if err != nil || resp.StatusCode != http.StatusAccepted {
			t.Fatalf("%s: %d, %v", tc.want, resp.StatusCode, err)
		}
		if cs.Actor.IP != "127.0.0.1" || cs.Actor.Source != tc.want || cs.Actor.ID != dev {
			t.Errorf("the change set's actor = %+v, want %s from 127.0.0.1", cs.Actor, tc.want)
		}
	}
	records, _, err := d.core.timeline.QueryAudit(ctx, events.Query{Types: []string{"core.user.update_profile"}, Limit: 10})
	if err != nil || len(records) != 3 {
		t.Fatalf("audit = %+v, %v", records, err)
	}
	for _, r := range records {
		if r.Actor.IP != "127.0.0.1" {
			t.Errorf("an audit record says the developer came from %q", r.Actor.IP)
		}
	}
}
