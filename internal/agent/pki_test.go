package agent

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/system/sim"
)

// testCluster is a panel CA with credentials for the panel and agents.
type testCluster struct {
	ca    *CA
	panel Credentials
}

func newTestCluster(t *testing.T) testCluster {
	t.Helper()
	ca, _, err := NewCA("Racksail test", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return testCluster{ca: ca, panel: issue(t, ca, Identity{Role: RolePanel})}
}

func issue(t *testing.T, ca *CA, id Identity) Credentials {
	t.Helper()
	keyPEM, csrPEM, err := NewKeyAndCSR(id.String())
	if err != nil {
		t.Fatal(err)
	}
	cert, err := ca.SignCSR(csrPEM, id, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return Credentials{CertPEM: cert, KeyPEM: keyPEM, CAPEM: ca.CertPEM()}
}

func TestCertificateAuthority(t *testing.T) {
	c := newTestCluster(t)
	agentCreds := issue(t, c.ca, Identity{Role: RoleAgent, Server: "web-1"})
	id, err := agentCreds.Identity()
	if err != nil || id != (Identity{Role: RoleAgent, Server: "web-1"}) || id.String() != "racksail://agent/web-1" {
		t.Fatalf("identity = %+v, %v", id, err)
	}
	cert, _ := agentCreds.TLSCertificate()
	if got, err := c.ca.Verify(cert.Leaf, time.Now()); err != nil || got != id {
		t.Errorf("verify = %+v, %v", got, err)
	}
	if _, err := c.ca.Verify(cert.Leaf, time.Now().Add(2*CertValidity)); err == nil {
		t.Error("an expired certificate must not verify")
	}
	other, _, _ := NewCA("Someone else", time.Now())
	if _, err := other.Verify(cert.Leaf, time.Now()); err == nil {
		t.Error("a certificate from another CA must not verify")
	}
	if agentCreds.NeedsRenewal(time.Now()) || !agentCreds.NeedsRenewal(time.Now().Add(CertValidity-RenewBefore+time.Hour)) {
		t.Error("renewal is due in the last month only")
	}
	for _, bad := range []Identity{{Role: RoleAgent}, {Role: RoleAgent, Server: "Web 1"}, {Role: RolePanel, Server: "x"}, {Role: "root"}} {
		if _, err := c.ca.Issue(bad, cert.Leaf.PublicKey, time.Now()); err == nil {
			t.Errorf("identity %+v must be refused", bad)
		}
	}
	// The panel decides the identity, whatever the request asks for.
	_, csrPEM, _ := NewKeyAndCSR("racksail://panel")
	issued, err := c.ca.SignCSR(csrPEM, Identity{Role: RoleAgent, Server: "web-2"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(issued)
	parsed, _ := x509.ParseCertificate(block.Bytes)
	if id, _ := IdentityOf(parsed); id.Server != "web-2" {
		t.Errorf("issued identity = %+v", id)
	}
	if _, err := c.ca.SignCSR([]byte("not a request"), id, time.Now()); err == nil {
		t.Error("garbage must not be signed")
	}
	// A CA survives being written out and read back.
	ca2, key2, _ := NewCA("Round trip", time.Now())
	if _, err := LoadCA(ca2.CertPEM(), key2); err != nil {
		t.Errorf("LoadCA: %v", err)
	}
	if _, err := LoadCA(c.ca.CertPEM(), key2); err == nil {
		t.Error("a certificate with another CA's key must not load")
	}
}

// serveTLS runs an agent with mutual TLS on a loopback port.
func serveTLS(t *testing.T, creds Credentials) (addr string, srv *Server) {
	t.Helper()
	m := sim.New(sim.Options{Hostname: "remote"})
	srv = NewServer(m.Backend(), "test", nil)
	cfg, err := ServerTLSConfig(creds)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.ServeTLS(ctx, ln, cfg) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("serve: %v", err)
		}
	})
	return ln.Addr().String(), srv
}

func tlsClient(t *testing.T, creds Credentials, server, addr string) *HTTPClient {
	t.Helper()
	cfg, err := ClientTLSConfig(creds, server)
	if err != nil {
		t.Fatal(err)
	}
	return NewHTTPClient("https://"+addr, &http.Client{Transport: &http.Transport{TLSClientConfig: cfg}, Timeout: 10 * time.Second})
}

func TestMutualTLS(t *testing.T) {
	ctx := context.Background()
	c := newTestCluster(t)
	agentCreds := issue(t, c.ca, Identity{Role: RoleAgent, Server: "web-1"})
	addr, srv := serveTLS(t, agentCreds)

	panel := tlsClient(t, c.panel, "web-1", addr)
	facts, err := GetFacts.Call(ctx, panel, Empty{}, CallOptions{})
	if err != nil || facts.Hostname != "remote" {
		t.Fatalf("facts = %+v, %v", facts, err)
	}
	records, _ := srv.Runtime().Audit().Recent(AuditFilter{Op: "GetFacts"})
	if len(records) != 1 || records[0].Caller != "racksail://panel" {
		t.Errorf("the audit trail should name the remote panel: %+v", records)
	}

	// The panel refuses an agent with another server's certificate.
	wrongServer := tlsClient(t, c.panel, "web-2", addr)
	if _, err := wrongServer.Health(ctx); !errors.Is(err, ErrUnreachable) {
		t.Errorf("calling web-2 at web-1's address: %v", err)
	}
	// The agent refuses other agents, and certificates of other CAs.
	peer := issue(t, c.ca, Identity{Role: RoleAgent, Server: "web-3"})
	if _, err := tlsClient(t, peer, "web-1", addr).Health(ctx); err == nil {
		t.Error("an agent's certificate must not call another agent")
	}
	foreignCA, _, _ := NewCA("Foreign", time.Now())
	foreign := issue(t, foreignCA, Identity{Role: RolePanel})
	foreign.CAPEM = c.ca.CertPEM()
	if _, err := tlsClient(t, foreign, "web-1", addr).Health(ctx); err == nil {
		t.Error("a panel certificate from another CA must be refused")
	}
	// No certificate at all.
	plain := NewHTTPClient("https://"+addr, &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}})
	if _, err := plain.Health(ctx); err == nil {
		t.Error("a client without a certificate must be refused")
	}
}

// joinTokens is a token checker that redeems each token once.
type joinTokens struct {
	mu     sync.Mutex
	tokens map[string]string
}

func (j *joinTokens) check(ctx context.Context, token, hostname string) (string, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	server, ok := j.tokens[token]
	if !ok {
		return "", errors.New("unknown token")
	}
	delete(j.tokens, token)
	return server, nil
}

func TestHubJoinRenewAndReverseCalls(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := newTestCluster(t)
	tokens := &joinTokens{tokens: map[string]string{"tok-1": "web-1"}}
	hub := NewHub(c.ca, c.panel, tokens.check, nil)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	hubDone := make(chan error, 1)
	go func() { hubDone <- hub.Serve(ctx, ln) }()
	addr := ln.Addr().String()

	// A server that doesn't pin the panel's CA can't join, and a token
	// works once.
	if _, err := Join(ctx, addr, "tok-1", strings.Repeat("0", 64), "web-1.example.net"); err == nil {
		t.Fatal("joining with the wrong CA fingerprint must fail")
	}
	creds, err := Join(ctx, addr, "tok-1", c.ca.Fingerprint(), "web-1.example.net")
	if err != nil {
		t.Fatalf("join: %v", err)
	}
	if id, _ := creds.Identity(); id != (Identity{Role: RoleAgent, Server: "web-1"}) {
		t.Errorf("joined as %+v", id)
	}
	if _, err := Join(ctx, addr, "tok-1", c.ca.Fingerprint(), "web-1.example.net"); !errors.Is(err, ErrNotAllowed) {
		t.Errorf("a used token: %v", err)
	}

	renewed, err := Renew(ctx, addr, creds)
	if err != nil {
		t.Fatalf("renew: %v", err)
	}
	if id, _ := renewed.Identity(); id.Server != "web-1" || string(renewed.KeyPEM) == string(creds.KeyPEM) {
		t.Errorf("renewed = %+v", id)
	}

	// The agent connects out and serves the panel over its connections.
	m := sim.New(sim.Options{Hostname: "web-1"})
	agentSrv := NewServer(m.Backend(), "test", nil)
	conn := &Connector{Addr: addr, Credentials: renewed, Server: agentSrv, Conns: 2}
	connDone := make(chan error, 1)
	go func() { connDone <- conn.Run(ctx) }()

	client := hub.Client("web-1")
	callCtx, callCancel := context.WithTimeout(ctx, 10*time.Second)
	defer callCancel()
	facts, err := GetFacts.Call(callCtx, client, Empty{}, CallOptions{})
	if err != nil || facts.Hostname != "web-1" {
		t.Fatalf("facts through the hub = %+v, %v", facts, err)
	}
	for range 5 {
		if _, err := GetFacts.Call(callCtx, client, Empty{}, CallOptions{}); err != nil {
			t.Fatalf("repeated call: %v", err)
		}
	}
	records, _ := agentSrv.Runtime().Audit().Recent(AuditFilter{Op: "GetFacts"})
	if len(records) != 6 || records[0].Caller != "racksail://panel" {
		t.Errorf("calls through the hub carry the panel's identity: %+v", records)
	}
	if got := hub.Connected(); len(got) != 1 || got[0].Server != "web-1" {
		t.Errorf("connected = %+v", got)
	}
	short, shortCancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer shortCancel()
	if _, err := hub.Client("web-9").Health(short); err == nil {
		t.Error("a server that never connected can't be called")
	}

	cancel()
	if err := <-connDone; err != nil {
		t.Errorf("connector: %v", err)
	}
	if err := <-hubDone; err != nil {
		t.Errorf("hub: %v", err)
	}
}
