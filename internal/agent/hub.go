package agent

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sort"
	"sync"
	"time"
)

// A server joins a cluster without opening a management port: its agent
// connects out to the panel's hub over mutual TLS, keeps a few connections
// open, and serves its API on them. The panel calls the agent through those
// connections with an ordinary HTTPClient.

// ALPNAgent is the protocol agents ask for when they connect to the hub.
const ALPNAgent = "racksail-agent/1"

// DefaultHubPort is where the panel's hub listens unless configured
// otherwise.
const DefaultHubPort = 8444

// Connector keeps connections open from an agent to the panel's hub and
// serves the agent's API on them.
type Connector struct {
	// Addr is the hub's host:port.
	Addr string
	// Credentials identify the agent and pin the panel's CA.
	Credentials Credentials
	Server      *Server
	// Conns is how many connections are kept open; 4 by default.
	Conns int
	// OnRenew keeps renewed credentials, so the next start uses them.
	OnRenew func(Credentials) error
	Log     *slog.Logger

	// mu guards Credentials once Run has started, since renewal replaces
	// them while connections are dialed.
	mu sync.Mutex
}

func (c *Connector) credentials() Credentials {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.Credentials
}

// Run connects and serves until ctx is done. It renews the agent's
// certificate before it expires.
func (c *Connector) Run(ctx context.Context) error {
	if c.Log == nil {
		c.Log = slog.Default()
	}
	n := c.Conns
	if n <= 0 {
		n = 4
	}
	ln := &dialListener{slots: make(chan struct{}, n), closed: make(chan struct{}), log: c.Log, dial: c.dial}
	srv := &http.Server{
		Handler: c.Server.Handler(),
		// The panel decides when connections end; the agent keeps them open
		// for as long as the panel wants.
		ConnContext: func(ctx context.Context, conn net.Conn) context.Context {
			if sc, ok := conn.(*slotConn); ok {
				return context.WithValue(ctx, callerKey{}, sc.peer)
			}
			return ctx
		},
		ErrorLog: slog.NewLogLogger(c.Log.Handler(), slog.LevelWarn),
	}
	go c.renewLoop(ctx)
	go func() {
		<-ctx.Done()
		ln.Close()
		srv.Close()
	}()
	if c.Server.runtime != nil {
		if err := c.Server.runtime.Start(ctx); err != nil {
			return err
		}
	}
	err := srv.Serve(ln)
	if ctx.Err() != nil {
		return nil
	}
	return err
}

func (c *Connector) tlsConfig() (*tls.Config, error) {
	creds := c.credentials()
	cert, err := creds.TLSCertificate()
	if err != nil {
		return nil, err
	}
	pool, err := creds.Pool()
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		MinVersion:         tls.VersionTLS13,
		Certificates:       []tls.Certificate{cert},
		InsecureSkipVerify: true,
		VerifyConnection:   verifyPeer(pool, RolePanel, ""),
		NextProtos:         []string{ALPNAgent},
	}, nil
}

func (c *Connector) dial(ctx context.Context) (net.Conn, string, error) {
	cfg, err := c.tlsConfig()
	if err != nil {
		return nil, "", err
	}
	d := &tls.Dialer{NetDialer: &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}, Config: cfg}
	conn, err := d.DialContext(ctx, "tcp", c.Addr)
	if err != nil {
		return nil, "", err
	}
	tc := conn.(*tls.Conn)
	st := tc.ConnectionState()
	if st.NegotiatedProtocol != ALPNAgent {
		conn.Close()
		return nil, "", fmt.Errorf("%s doesn't speak the agent protocol", c.Addr)
	}
	id, err := IdentityOf(st.PeerCertificates[0])
	if err != nil {
		conn.Close()
		return nil, "", err
	}
	return conn, id.String(), nil
}

// renewLoop renews the certificate when it's due, checking twice a day.
func (c *Connector) renewLoop(ctx context.Context) {
	t := time.NewTicker(12 * time.Hour)
	defer t.Stop()
	for {
		if creds := c.credentials(); creds.NeedsRenewal(time.Now()) {
			renewed, err := Renew(ctx, c.Addr, creds)
			if err != nil {
				c.Log.Warn("renewing the agent's certificate", "error", err)
			} else {
				c.mu.Lock()
				c.Credentials = renewed
				c.mu.Unlock()
				if c.OnRenew != nil {
					if err := c.OnRenew(renewed); err != nil {
						c.Log.Error("keeping the renewed certificate", "error", err)
					}
				}
				c.Log.Info("renewed the agent's certificate")
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// dialListener is a net.Listener whose connections are dialed out, at most
// as many at once as it has slots.
type dialListener struct {
	slots  chan struct{}
	closed chan struct{}
	once   sync.Once
	log    *slog.Logger
	dial   func(context.Context) (net.Conn, string, error)
}

// slotConn frees its slot when it closes, so another connection is dialed.
type slotConn struct {
	net.Conn
	peer    string
	release func()
}

func (s *slotConn) Close() error {
	s.release()
	return s.Conn.Close()
}

func (l *dialListener) Accept() (net.Conn, error) {
	select {
	case l.slots <- struct{}{}:
	case <-l.closed:
		return nil, net.ErrClosed
	}
	backoff := time.Second
	for {
		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			select {
			case <-l.closed:
				cancel()
			case <-ctx.Done():
			}
		}()
		conn, peer, err := l.dial(ctx)
		cancel()
		if err == nil {
			var once sync.Once
			return &slotConn{Conn: conn, peer: peer, release: func() { once.Do(func() { <-l.slots }) }}, nil
		}
		l.log.Warn("connecting to the panel", "error", err, "retry", backoff)
		select {
		case <-l.closed:
			<-l.slots
			return nil, net.ErrClosed
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, 30*time.Second)
	}
}

func (l *dialListener) Close() error {
	l.once.Do(func() { close(l.closed) })
	return nil
}

func (l *dialListener) Addr() net.Addr { return dialAddr{} }

type dialAddr struct{}

func (dialAddr) Network() string { return "racksail-hub" }
func (dialAddr) String() string  { return "panel" }

// JoinRequest is what a server sends to join: the one-time token it was
// given and a certificate request for its key.
type JoinRequest struct {
	Token    string `json:"token"`
	CSR      []byte `json:"csr" doc:"PEM certificate request"`
	Hostname string `json:"hostname"`
}

// JoinResponse is what the panel answers a server that may join.
type JoinResponse struct {
	Certificate []byte `json:"certificate" doc:"PEM certificate for the server's key"`
	CA          []byte `json:"ca" doc:"PEM certificate of the panel's CA"`
	Server      string `json:"server" doc:"The name the server is known by"`
}

// RenewRequest asks for a new certificate for the caller's identity.
type RenewRequest struct {
	CSR []byte `json:"csr"`
}

// TokenChecker redeems a join token for the name of the server joining, or
// refuses it. Tokens are used once.
type TokenChecker func(ctx context.Context, token, hostname string) (server string, err error)

// Hub accepts connections agents open to the panel, serves joining and
// renewal, and hands the connections to clients calling those agents.
type Hub struct {
	ca    *CA
	creds Credentials
	check TokenChecker
	log   *slog.Logger
	now   func() time.Time

	mu      sync.Mutex
	idle    map[string][]*hubConn
	waiters map[string][]chan *hubConn
	seen    map[string]time.Time
}

type hubConn struct {
	net.Conn
	since time.Time
}

// NewHub returns a hub for a panel that keeps ca. creds are the panel's own
// credentials, issued by ca; check decides which join tokens are good.
func NewHub(ca *CA, creds Credentials, check TokenChecker, log *slog.Logger) *Hub {
	if log == nil {
		log = slog.Default()
	}
	return &Hub{ca: ca, creds: creds, check: check, log: log, now: time.Now,
		idle: map[string][]*hubConn{}, waiters: map[string][]chan *hubConn{}, seen: map[string]time.Time{}}
}

// TLSConfig is the hub listener's configuration. Agents ask for the agent
// protocol and must present their certificate; joining servers have none
// yet and use HTTP with their token. The chain includes the CA, so a
// joining server can check it against the fingerprint it was given.
func (h *Hub) TLSConfig() (*tls.Config, error) {
	cert, err := h.creds.TLSCertificate()
	if err != nil {
		return nil, err
	}
	if block, _ := pem.Decode(h.ca.CertPEM()); block != nil {
		cert.Certificate = append(cert.Certificate, block.Bytes)
	}
	pool := h.ca.Pool()
	base := &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{cert}}
	agents := base.Clone()
	agents.NextProtos = []string{ALPNAgent}
	agents.ClientAuth = tls.RequireAnyClientCert
	agents.VerifyConnection = verifyPeer(pool, RoleAgent, "")
	others := base.Clone()
	others.NextProtos = []string{"http/1.1"}
	others.ClientAuth = tls.RequestClientCert
	return &tls.Config{
		MinVersion: tls.VersionTLS13,
		GetConfigForClient: func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
			for _, p := range hello.SupportedProtos {
				if p == ALPNAgent {
					return agents, nil
				}
			}
			return others, nil
		},
	}, nil
}

// Serve accepts connections on ln until ctx is done.
func (h *Hub) Serve(ctx context.Context, ln net.Listener) error {
	cfg, err := h.TLSConfig()
	if err != nil {
		return err
	}
	httpConns := &chanListener{conns: make(chan net.Conn), addr: ln.Addr(), closed: make(chan struct{})}
	srv := &http.Server{Handler: h.Handler(), ReadHeaderTimeout: 10 * time.Second, ErrorLog: slog.NewLogLogger(h.log.Handler(), slog.LevelWarn)}
	go srv.Serve(httpConns)
	go func() {
		<-ctx.Done()
		ln.Close()
		httpConns.Close()
		srv.Close()
		h.closeAll()
	}()
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		go h.handshake(ctx, tls.Server(conn, cfg), httpConns)
	}
}

func (h *Hub) handshake(ctx context.Context, conn *tls.Conn, httpConns *chanListener) {
	hctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := conn.HandshakeContext(hctx); err != nil {
		h.log.Debug("hub handshake failed", "remote", conn.RemoteAddr().String(), "error", err)
		conn.Close()
		return
	}
	st := conn.ConnectionState()
	if st.NegotiatedProtocol != ALPNAgent {
		select {
		case httpConns.conns <- conn:
		case <-ctx.Done():
			conn.Close()
		}
		return
	}
	id, err := IdentityOf(st.PeerCertificates[0])
	if err != nil {
		conn.Close()
		return
	}
	h.add(id.Server, conn)
}

// add makes a connection available, to a waiting caller first.
func (h *Hub) add(server string, conn net.Conn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	hc := &hubConn{Conn: conn, since: h.now()}
	h.seen[server] = hc.since
	if w := h.waiters[server]; len(w) > 0 {
		h.waiters[server] = w[1:]
		w[0] <- hc
		return
	}
	h.idle[server] = append(h.idle[server], hc)
}

// take hands out a connection to the agent of server, waiting for one
// until ctx is done.
func (h *Hub) take(ctx context.Context, server string) (net.Conn, error) {
	h.mu.Lock()
	if conns := h.idle[server]; len(conns) > 0 {
		// The newest connection is the most likely to be alive.
		hc := conns[len(conns)-1]
		h.idle[server] = conns[:len(conns)-1]
		h.mu.Unlock()
		return hc, nil
	}
	ch := make(chan *hubConn, 1)
	h.waiters[server] = append(h.waiters[server], ch)
	h.mu.Unlock()
	select {
	case hc := <-ch:
		return hc, nil
	case <-ctx.Done():
		h.mu.Lock()
		w := h.waiters[server]
		for i, c := range w {
			if c == ch {
				h.waiters[server] = append(w[:i], w[i+1:]...)
				break
			}
		}
		h.mu.Unlock()
		// A connection may have arrived in the meantime.
		select {
		case hc := <-ch:
			h.add(server, hc.Conn)
		default:
		}
		return nil, fmt.Errorf("the agent of %s isn't connected: %w", server, ctx.Err())
	}
}

func (h *Hub) closeAll() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for server, conns := range h.idle {
		for _, c := range conns {
			c.Close()
		}
		delete(h.idle, server)
	}
}

// Client returns a client for the agent of server, calling it over the
// connections it opened. Calls that never reached the agent are retried.
func (h *Hub) Client(server string) *HTTPClient {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			wait, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			return h.take(wait, server)
		},
		MaxIdleConnsPerHost: 4,
		IdleConnTimeout:     5 * time.Minute,
	}
	return &HTTPClient{base: "http://" + server, http: &http.Client{Transport: transport}, retries: 2}
}

// ConnectedServer describes an agent connected to the hub.
type ConnectedServer struct {
	Server   string    `json:"server"`
	Idle     int       `json:"idle" doc:"Connections waiting for calls"`
	LastSeen time.Time `json:"lastSeen" doc:"When the agent last opened a connection"`
}

// Connected lists the agents that have connected, most recent first.
func (h *Hub) Connected() []ConnectedServer {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]ConnectedServer, 0, len(h.seen))
	for server, at := range h.seen {
		out = append(out, ConnectedServer{Server: server, Idle: len(h.idle[server]), LastSeen: at})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastSeen.After(out[j].LastSeen) })
	return out
}

// Handler serves joining and renewal on the hub's HTTP side.
func (h *Hub) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/join", func(w http.ResponseWriter, r *http.Request) {
		var req JoinRequest
		if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&req); err != nil || req.Token == "" {
			writeProblem(w, ErrInvalidInput.New("op", "join", "reason", "a token and a certificate request are needed"))
			return
		}
		server, err := h.check(r.Context(), req.Token, req.Hostname)
		if err != nil {
			h.log.Warn("refused a server joining", "remote", r.RemoteAddr, "hostname", req.Hostname, "error", err)
			writeProblem(w, ErrNotAllowed.New("op", "join", "target", "this panel"))
			return
		}
		cert, err := h.ca.SignCSR(req.CSR, Identity{Role: RoleAgent, Server: server}, h.now())
		if err != nil {
			writeProblem(w, ErrInvalidInput.Wrap(err, "op", "join", "reason", err.Error()))
			return
		}
		h.log.Info("a server joined", "server", server, "hostname", req.Hostname, "remote", r.RemoteAddr)
		writeJSONResponse(w, JoinResponse{Certificate: cert, CA: h.ca.CertPEM(), Server: server})
	})
	mux.HandleFunc("POST /v1/renew", func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
			writeProblem(w, ErrNotAllowed.New("op", "renew", "target", "a certificate"))
			return
		}
		id, err := h.ca.Verify(r.TLS.PeerCertificates[0], h.now())
		if err != nil || id.Role != RoleAgent {
			writeProblem(w, ErrNotAllowed.New("op", "renew", "target", "a certificate"))
			return
		}
		var req RenewRequest
		if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&req); err != nil {
			writeProblem(w, ErrInvalidInput.Wrap(err, "op", "renew", "reason", err.Error()))
			return
		}
		cert, err := h.ca.SignCSR(req.CSR, id, h.now())
		if err != nil {
			writeProblem(w, ErrInvalidInput.Wrap(err, "op", "renew", "reason", err.Error()))
			return
		}
		writeJSONResponse(w, JoinResponse{Certificate: cert, CA: h.ca.CertPEM(), Server: id.Server})
	})
	return mux
}

func writeJSONResponse(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

// chanListener is a net.Listener fed from a channel.
type chanListener struct {
	conns  chan net.Conn
	addr   net.Addr
	closed chan struct{}
	once   sync.Once
}

func (l *chanListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}

func (l *chanListener) Close() error {
	l.once.Do(func() { close(l.closed) })
	return nil
}

func (l *chanListener) Addr() net.Addr { return l.addr }

// pinnedTLS trusts a hub whose chain ends in the CA with the given
// fingerprint, for a server that has no CA yet.
func pinnedTLS(fingerprint string, cert *tls.Certificate) *tls.Config {
	return &tls.Config{
		MinVersion:         tls.VersionTLS13,
		InsecureSkipVerify: true,
		NextProtos:         []string{"http/1.1"},
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) < 2 {
				return errors.New("the hub didn't present its CA")
			}
			ca := cs.PeerCertificates[len(cs.PeerCertificates)-1]
			if Fingerprint(ca) != fingerprint {
				return errors.New("the hub's CA doesn't match the fingerprint in the token")
			}
			pool := x509.NewCertPool()
			pool.AddCert(ca)
			id, err := verifyIdentity(pool, cs.PeerCertificates[0], nil, time.Now())
			if err != nil {
				return err
			}
			if id.Role != RolePanel {
				return fmt.Errorf("the hub is %s, not the panel", id)
			}
			return nil
		},
		GetClientCertificate: func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
			if cert == nil {
				return &tls.Certificate{}, nil
			}
			return cert, nil
		},
	}
}

// Join enrolls this server with the panel's hub at addr. The token comes
// from the panel together with the fingerprint of its CA, which is pinned,
// so a server pretending to be the panel can't take the token.
func Join(ctx context.Context, addr, token, caFingerprint, hostname string) (Credentials, error) {
	keyPEM, csrPEM, err := NewKeyAndCSR(hostname)
	if err != nil {
		return Credentials{}, err
	}
	body, err := json.Marshal(JoinRequest{Token: token, CSR: csrPEM, Hostname: hostname})
	if err != nil {
		return Credentials{}, err
	}
	resp, err := hubPost(ctx, addr, "/v1/join", body, pinnedTLS(caFingerprint, nil))
	if err != nil {
		return Credentials{}, err
	}
	return resp.credentials(keyPEM, caFingerprint)
}

// Renew asks the hub for a new certificate for the same identity and key
// type, authenticating with the current one.
func Renew(ctx context.Context, addr string, creds Credentials) (Credentials, error) {
	cert, err := creds.TLSCertificate()
	if err != nil {
		return Credentials{}, err
	}
	block, _ := pem.Decode(creds.CAPEM)
	if block == nil {
		return Credentials{}, errors.New("no CA in the credentials")
	}
	ca, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return Credentials{}, err
	}
	cfg := pinnedTLS(Fingerprint(ca), &cert)
	keyPEM, csrPEM, err := NewKeyAndCSR(cert.Leaf.Subject.CommonName)
	if err != nil {
		return Credentials{}, err
	}
	body, err := json.Marshal(RenewRequest{CSR: csrPEM})
	if err != nil {
		return Credentials{}, err
	}
	resp, err := hubPost(ctx, addr, "/v1/renew", body, cfg)
	if err != nil {
		return Credentials{}, err
	}
	return resp.credentials(keyPEM, Fingerprint(ca))
}

func (r JoinResponse) credentials(keyPEM []byte, fingerprint string) (Credentials, error) {
	block, _ := pem.Decode(r.CA)
	if block == nil {
		return Credentials{}, errors.New("the panel sent no CA")
	}
	ca, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return Credentials{}, err
	}
	if Fingerprint(ca) != fingerprint {
		return Credentials{}, errors.New("the panel sent another CA than the one pinned")
	}
	creds := Credentials{CertPEM: r.Certificate, KeyPEM: keyPEM, CAPEM: r.CA}
	if _, err := creds.TLSCertificate(); err != nil {
		return Credentials{}, err
	}
	pool, _ := creds.Pool()
	cert, _ := creds.TLSCertificate()
	if _, err := verifyIdentity(pool, cert.Leaf, nil, time.Now()); err != nil {
		return Credentials{}, err
	}
	return creds, nil
}

func hubPost(ctx context.Context, addr, path string, body []byte, cfg *tls.Config) (JoinResponse, error) {
	client := &http.Client{Timeout: time.Minute, Transport: &http.Transport{TLSClientConfig: cfg, ForceAttemptHTTP2: false}}
	defer client.CloseIdleConnections()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://"+addr+path, bytes.NewReader(body))
	if err != nil {
		return JoinResponse{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return JoinResponse{}, ErrUnreachable.Wrap(err, "reason", err.Error())
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return JoinResponse{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return JoinResponse{}, problemError(path, resp.Status, data)
	}
	var out JoinResponse
	if err := json.Unmarshal(data, &out); err != nil {
		return JoinResponse{}, err
	}
	return out, nil
}
