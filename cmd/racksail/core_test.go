package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/auth/identity"
	"github.com/vilanobeachflorida/racksail/internal/config"
	"github.com/vilanobeachflorida/racksail/internal/modules"
	"github.com/vilanobeachflorida/racksail/internal/modules/modulestest"
	"github.com/vilanobeachflorida/racksail/internal/notify"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

// knot stands in for a module that runs a service and checks on it.
type knot struct {
	modules.Base
	down bool
}

func (k *knot) HealthChecks() []modules.HealthCheck {
	return []modules.HealthCheck{{Name: "knot", Check: func(ctx context.Context, svc *modules.Services) error {
		if svc == nil {
			return errors.New("the check got no services")
		}
		if k.down {
			return errors.New("knot isn't answering")
		}
		return nil
	}}}
}

func TestModuleHealthChecks(t *testing.T) {
	mod := &knot{Base: modules.NewBase([]byte(`
id: dns
name: DNS
description: Zones and records.
version: 0.1.0
license: AGPL-3.0-or-later
category: hosting
dependencies: {}
profiles: [dns-server]
hardware: {arch: [amd64, arm64]}
cost: {idleMemoryMB: 1, activeMemoryMB: 2, diskMB: 3}
upstream: []
`))}
	e := modulestest.New(t, modulestest.Options{}, mod)
	checks := moduleHealthChecks(e.Registry, e.Manager, e.Services)
	if len(checks) != 1 || checks[0].Name != "dns.knot" {
		t.Fatalf("checks = %+v", checks)
	}
	if checks[0].Active() {
		t.Error("a module that is off isn't checked")
	}
	ctx := context.Background()
	e.TurnOn(t, "dns")
	if !checks[0].Active() || checks[0].Check(ctx) != nil {
		t.Error("once it's on, its check runs and passes")
	}
	mod.down = true
	if err := checks[0].Check(ctx); err == nil {
		t.Error("a failing service fails the check")
	}
}

// freeAddr returns a loopback address with a port nothing listens on.
func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

// Stopping the panel ends the live streams clients hold open, so it stops
// at once and cleanly, and its database closes after the work that writes to
// it has stopped.
func TestStoppingWithStreamsOpen(t *testing.T) {
	addr := freeAddr(t)
	root := newRootCommand()
	root.SetArgs([]string{"dev", "--addr", addr, "--edge-http", "127.0.0.1:0", "--edge-https", "127.0.0.1:0", "--data", t.TempDir()})
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- root.ExecuteContext(ctx) }()

	var resp *http.Response
	deadline := time.Now().Add(10 * time.Second)
	for {
		req, _ := http.NewRequest("GET", "http://"+addr+"/api/v1/events/stream", nil)
		var err error
		if resp, err = http.DefaultClient.Do(req); err == nil && resp.StatusCode == http.StatusOK {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the panel never answered: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	defer resp.Body.Close()
	// The stream says it is connected before anything else.
	line, err := bufio.NewReader(resp.Body).ReadString('\n')
	if err != nil || !strings.HasPrefix(line, ": connected") {
		t.Fatalf("stream = %q, %v", line, err)
	}
	var metrics string
	if m, err := http.Get("http://" + addr + "/api/v1/metrics"); err == nil {
		body, _ := io.ReadAll(m.Body)
		m.Body.Close()
		metrics = string(body)
	}
	for _, name := range []string{"racksail_jobs_queued", "racksail_reconcile_queued", "racksail_webhook_deliveries_pending", "racksail_events_total", "racksail_api_streams 1"} {
		if !strings.Contains(metrics, name) {
			t.Errorf("the metrics have no %s:\n%s", name, metrics)
		}
	}

	start := time.Now()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("stopping = %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the panel didn't stop")
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("stopping took %s", took)
	}
	if _, err := io.ReadAll(resp.Body); err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("the stream ended badly: %v", err)
	}
}

// A person's own notifications reach their user's email address: the
// panel's users are who notifications find people among.
func TestNotificationsReachThePanelsUsers(t *testing.T) {
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
	id := store.NewID(identity.PrefixUser)
	spec, _ := json.Marshal(identity.UserSpec{Username: "ada", Name: "Ada", Email: "ada@example.com"})
	if err := d.core.services.Store.Write(ctx, store.WriteMeta{Actor: "system:tests"}, func(tx store.Tx) error {
		return tx.Create(ctx, &store.Resource{Meta: store.Meta{ID: id, Kind: identity.UserKind.Name(), Scope: store.ServerScope()}, Spec: spec})
	}); err != nil {
		t.Fatal(err)
	}
	sent, err := d.core.services.Notify.Send(ctx, notify.Recipient{Person: id}, notify.Message{Title: "Hello", Body: "Hello, Ada."})
	if err != nil {
		t.Fatal(err)
	}
	// The panel has no mail relay, so the email can't go out yet; what
	// matters is that it was for Ada's address.
	deadline := time.Now().Add(5 * time.Second)
	for {
		n, err := d.core.services.Notify.Notification(ctx, sent.ID)
		if err != nil {
			t.Fatal(err)
		}
		if n.AttemptCount > 0 {
			if !strings.Contains(n.LastError, "mail relay") {
				t.Errorf("the notification failed with %q, not for want of a mail relay", n.LastError)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the notification was never attempted")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
