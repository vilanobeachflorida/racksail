package main

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/modules"
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
	reg := modules.NewRegistry()
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
	if err := reg.Add(mod, nil); err != nil {
		t.Fatal(err)
	}
	svc := &modules.Services{}
	manager := modules.NewManager(reg, modules.ManagerOptions{Services: svc})
	checks := moduleHealthChecks(reg, manager, svc)
	if len(checks) != 1 || checks[0].Name != "dns.knot" {
		t.Fatalf("checks = %+v", checks)
	}
	if checks[0].Active() {
		t.Error("a module that is off isn't checked")
	}
	ctx := context.Background()
	if err := manager.TurnOn(ctx, "dns"); err != nil {
		t.Fatal(err)
	}
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
