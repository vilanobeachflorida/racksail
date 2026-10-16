package main

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/agent"
)

func TestAgentJoinKeepsTheCertificate(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ca, _, err := agent.NewCA("Racksail test", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	keyPEM, csrPEM, _ := agent.NewKeyAndCSR("panel")
	certPEM, err := ca.SignCSR(csrPEM, agent.Identity{Role: agent.RolePanel}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	panel := agent.Credentials{CertPEM: certPEM, KeyPEM: keyPEM, CAPEM: ca.CertPEM()}
	hub, err := agent.NewHub(agent.HubOptions{
		CA: ca, Credentials: panel,
		Tokens: func(ctx context.Context, token, hostname string) (string, error) {
			if token != "good-token" {
				return "", errors.New("unknown token")
			}
			return "web-1", nil
		},
		Members: func(ctx context.Context, server string) (bool, error) { return server == "web-1", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go hub.Serve(ctx, ln)

	file := filepath.Join(t.TempDir(), "cluster.json")
	out, err := run(t, "agent", "join", ln.Addr().String(), "good-token", "--fingerprint", ca.Fingerprint(), "--cluster-file", file, "--hostname", "web-1.example.net")
	if err != nil {
		t.Fatalf("join: %v\n%s", err, out)
	}
	if !strings.Contains(out, "web-1") {
		t.Errorf("output = %q", out)
	}
	state, err := readCluster(file)
	if err != nil {
		t.Fatal(err)
	}
	if id, _ := state.Credentials.Identity(); state.Hub != ln.Addr().String() || id.Server != "web-1" {
		t.Errorf("state = %+v, %+v", state.Hub, id)
	}
	if info, _ := os.Stat(file); info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v", info.Mode())
	}
	if _, err := run(t, "agent", "join", ln.Addr().String(), "bad-token", "--fingerprint", ca.Fingerprint(), "--cluster-file", file+".2"); err == nil {
		t.Error("a bad token must not join")
	}
	if _, err := os.Stat(file + ".2"); !errors.Is(err, os.ErrNotExist) {
		t.Error("a failed join must leave nothing behind")
	}
}

func TestRunAllStopsTheRestWhenOneEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	boom := errors.New("boom")
	err := runAll(cancel,
		func() error { <-ctx.Done(); return nil },
		func() error { return boom },
		func() error { <-ctx.Done(); return nil },
	)
	if !errors.Is(err, boom) {
		t.Errorf("runAll = %v", err)
	}
}
