package edge

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddytls"
	"github.com/caddyserver/certmagic"
	"github.com/coreos/go-systemd/v22/daemon"
)

// Settings are the choices the panel makes for the edge. They are kept in
// the edge's state, so a restarted edge uses them before the panel calls.
type Settings struct {
	// Email registers the ACME accounts; certificates are obtained without
	// one too.
	Email string `json:"email,omitempty" doc:"Contact address for the ACME accounts"`
	// CA is the ACME directory, Let's Encrypt's when empty.
	CA string `json:"ca,omitempty" doc:"ACME directory URL; Let's Encrypt when empty"`
	// CARoots are PEM certificates trusted for the CA's own HTTPS, for a
	// private CA.
	CARoots string `json:"caRoots,omitempty" doc:"PEM certificates to trust for the CA's HTTPS"`
	// ZeroSSL falls back to ZeroSSL when the CA fails. It needs Email.
	ZeroSSL bool `json:"zeroSSL,omitempty" doc:"Fall back to ZeroSSL when the CA fails"`
	// IPProfile is the ACME profile for IP address certificates, which
	// Let's Encrypt only issues short-lived.
	IPProfile string `json:"ipProfile,omitempty" doc:"ACME profile for IP address certificates; shortlived when empty"`
	// DisableHTTP3 serves HTTP/1.1 and HTTP/2 only.
	DisableHTTP3 bool `json:"disableHTTP3,omitempty"`
	// DisableHTTPChallenge and DisableTLSALPNChallenge stop validating
	// names over port 80 or port 443, for networks that block one.
	DisableHTTPChallenge    bool `json:"disableHTTPChallenge,omitempty" doc:"Don't validate names over port 80"`
	DisableTLSALPNChallenge bool `json:"disableTLSALPNChallenge,omitempty" doc:"Don't validate names over port 443"`
	// DNSResolvers check that DNS-01 challenge records are published.
	DNSResolvers []string `json:"dnsResolvers,omitempty" doc:"Resolvers (host:port) that check DNS-01 records are published"`
	// DNSPropagationTimeout bounds that check; negative skips it.
	DNSPropagationTimeout time.Duration `json:"dnsPropagationTimeout,omitempty"`
}

// LetsEncrypt is the default ACME directory.
const LetsEncrypt = "https://acme-v02.api.letsencrypt.org/directory"

// zeroSSL is ZeroSSL's ACME directory.
const zeroSSL = "https://acme.zerossl.com/v2/DV90"

// Validate checks settings.
func (s Settings) Validate() error {
	if s.Email != "" && (strings.ContainsAny(s.Email, " \t\r\n<>") || !strings.Contains(s.Email, "@") || len(s.Email) > 254) {
		return fmt.Errorf("email %q", s.Email)
	}
	if s.CA != "" {
		u, err := url.Parse(s.CA)
		if err != nil || u.Scheme != "https" || u.Host == "" {
			return fmt.Errorf("the ACME directory %q must be an https URL", s.CA)
		}
	}
	if s.CARoots != "" {
		if !x509PoolFromPEM([]byte(s.CARoots)) {
			return errors.New("caRoots holds no PEM certificate")
		}
	}
	if s.ZeroSSL && s.Email == "" {
		return errors.New("ZeroSSL needs an email address")
	}
	if s.DisableHTTPChallenge && s.DisableTLSALPNChallenge {
		return errors.New("names need port 80 or port 443 to be validated; keep one of the challenges")
	}
	if s.IPProfile != "" && !routeIDPattern.MatchString(s.IPProfile) {
		return fmt.Errorf("ACME profile %q", s.IPProfile)
	}
	for _, r := range s.DNSResolvers {
		if _, _, err := net.SplitHostPort(r); err != nil {
			return fmt.Errorf("resolver %q must be host:port", r)
		}
	}
	return nil
}

// Options configure an edge.
type Options struct {
	// Table is the routing table; a new one when nil.
	Table *Table
	// HTTP and HTTPS are the listen addresses. A port of 0 picks a free
	// one, which Addrs reports once the edge serves.
	HTTP, HTTPS string
	// StateDir keeps certificates, ACME accounts, settings and the last
	// routing table.
	StateDir string
	// Certs provides uploaded certificates by name.
	Certs CertSource
	// Dev issues every certificate from a local CA, for development where
	// no public CA can reach the edge.
	Dev bool
	// MapPath maps a path on the server to where the edge reads it, for
	// the simulated server in development. Nil leaves paths alone.
	MapPath func(string) string
	// Responder answers the edge's questions in the same process. Without
	// one, the panel answers them over the admin socket.
	Responder Responder
	// Settings are used until the panel sets others.
	Settings Settings
	Log      *slog.Logger
}

// Edge serves the routing table with an embedded Caddy. Caddy starts with
// the first route that isn't passive and stops when the last one goes;
// routes change without reloading it.
type Edge struct {
	opts      Options
	table     *Table
	log       *slog.Logger
	id        string
	questions *questionBroker
	asked     *askLimiter
	certs     *cachedCerts
	activity  *activity
	keeper    *certKeeper

	mu        sync.Mutex
	settings  Settings
	settingsC chan struct{}
	running   bool
	httpAddr  string
	httpsAddr string
	loads     int
	lastErr   error
	tlsApp    *caddytls.TLS
	magic     *certmagic.Config

	// public decides which subjects public CAs certify; certmagic's rules
	// when nil. Tests treat loopback addresses as public.
	public func(string) bool
}

// New returns an edge.
func New(opts Options) (*Edge, error) {
	if opts.Table == nil {
		opts.Table = NewTable()
	}
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	if opts.StateDir == "" {
		return nil, errors.New("edge: no state directory")
	}
	if opts.HTTP == "" || opts.HTTPS == "" {
		return nil, errors.New("edge: no listen addresses")
	}
	if opts.Certs == nil {
		opts.Certs = FileCerts{}
	}
	if err := opts.Settings.Validate(); err != nil {
		return nil, fmt.Errorf("edge settings: %w", err)
	}
	var id [8]byte
	rand.Read(id[:])
	e := &Edge{
		opts: opts, table: opts.Table, log: opts.Log, id: hex.EncodeToString(id[:]),
		questions: newBroker(), asked: newAskLimiter(), certs: newCachedCerts(opts.Certs), activity: newActivity(),
		settings: opts.Settings, settingsC: make(chan struct{}, 1),
	}
	e.keeper = newKeeper(e)
	opts.Table.checkWith(e.checkRoute)
	return e, nil
}

// protectedDirs are the edge's own files, which no route serves: its state,
// where Caddy keeps certificates, keys and ACME accounts, and the uploaded
// certificates it reads. Each is given as written and as it resolves.
func (e *Edge) protectedDirs() []string {
	var dirs []string
	add := func(p string) {
		if p == "" {
			return
		}
		abs, err := filepath.Abs(p)
		if err != nil {
			return
		}
		dirs = append(dirs, filepath.ToSlash(abs))
		if real, err := filepath.EvalSymlinks(abs); err == nil && real != abs {
			dirs = append(dirs, filepath.ToSlash(real))
		}
	}
	add(e.opts.StateDir)
	if fc, ok := e.opts.Certs.(FileCerts); ok {
		add(fc.Dir)
	}
	return dirs
}

// checkRoute refuses static and FastCGI roots that are, hold or lie in the
// edge's own files: serving them would hand out every site's keys.
func (e *Edge) checkRoute(r Route) error {
	if r.Handler.Type != HandleStatic && r.Handler.Type != HandleFastCGI {
		return nil
	}
	root, err := filepath.Abs(e.mapPath(r.Handler.Root))
	if err != nil {
		return fmt.Errorf("route %s: root %s: %w", r.ID, r.Handler.Root, err)
	}
	root = filepath.ToSlash(root)
	for _, d := range e.protectedDirs() {
		if within(root, d) || within(d, root) {
			return fmt.Errorf("route %s: the root %s would serve the edge's own files", r.ID, r.Handler.Root)
		}
	}
	return nil
}

// Table returns the routing table the edge serves.
func (e *Edge) Table() *Table { return e.table }

// Addrs returns the addresses the edge serves HTTP and HTTPS on, once it
// does.
func (e *Edge) Addrs() (httpAddr, httpsAddr string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.httpAddr, e.httpsAddr
}

// Running reports whether the edge serves.
func (e *Edge) Running() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.running
}

// Settings returns the edge's settings.
func (e *Edge) Settings() Settings {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.settings
}

// SetSettings changes the settings. Caddy reloads only when what it
// serves with changes; the routes never cause a reload.
func (e *Edge) SetSettings(s Settings) error {
	if err := s.Validate(); err != nil {
		return err
	}
	e.mu.Lock()
	same := settingsEqual(e.settings, s)
	e.settings = s
	e.mu.Unlock()
	if same {
		return nil
	}
	if err := e.saveJSON(settingsFile, s); err != nil {
		return err
	}
	select {
	case e.settingsC <- struct{}{}:
	default:
	}
	return nil
}

func settingsEqual(a, b Settings) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}

// mapPath maps a server path to where the edge reads it.
func (e *Edge) mapPath(p string) string {
	if e.opts.MapPath == nil {
		return p
	}
	return e.opts.MapPath(p)
}

// ask answers a question, in this process or through the panel's poll.
func (e *Edge) ask(ctx context.Context, q Question) error {
	if e.opts.Responder != nil {
		return ask(ctx, e.opts.Responder, q)
	}
	return e.questions.ask(ctx, q)
}

// Files in the state directory.
const (
	settingsFile = "settings.json"
	routesFile   = "routes.json"
	caddyDir     = "caddy"
)

func (e *Edge) saveJSON(name string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(e.opts.StateDir, name), data)
}

func writeFileAtomic(p string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o640); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// restore loads the settings and routes a previous run kept, so the edge
// serves its sites again before the panel gets in touch.
func (e *Edge) restore() error {
	if data, err := os.ReadFile(filepath.Join(e.opts.StateDir, settingsFile)); err == nil {
		var s Settings
		if err := json.Unmarshal(data, &s); err != nil {
			return fmt.Errorf("reading the saved settings: %w", err)
		}
		if err := s.Validate(); err != nil {
			return fmt.Errorf("the saved settings: %w", err)
		}
		e.mu.Lock()
		e.settings = s
		e.mu.Unlock()
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	f, err := os.Open(filepath.Join(e.opts.StateDir, routesFile))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	if e.table.Len() > 0 {
		// Someone set routes before the edge ran; they win.
		return nil
	}
	dropped, err := e.table.load(f)
	if err != nil {
		return fmt.Errorf("reading the saved routes: %w", err)
	}
	for _, d := range dropped {
		e.log.Warn("dropped a saved route that no longer validates", "error", d)
	}
	return nil
}

func (e *Edge) saveRoutes() error {
	var b bytes.Buffer
	if err := e.table.save(&b); err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(e.opts.StateDir, routesFile), b.Bytes())
}

// One edge at a time runs Caddy, which is global to the process.
var caddyOwner struct {
	sync.Mutex
	id string
}

// instances finds the edge a Caddy module belongs to.
var instances sync.Map

func lookup(id string) (*Edge, error) {
	if v, ok := instances.Load(id); ok {
		return v.(*Edge), nil
	}
	return nil, fmt.Errorf("edge %q isn't running", id)
}

// Run serves the admin API on admin, which may be nil when the table is
// changed in the same process, and serves the routing table from the first
// route that isn't passive until ctx is done.
func (e *Edge) Run(ctx context.Context, admin net.Listener) error {
	if err := os.MkdirAll(e.opts.StateDir, 0o750); err != nil {
		return err
	}
	if err := e.restore(); err != nil {
		e.log.Error("restoring the edge's state", "error", err)
	}
	instances.Store(e.id, e)
	defer instances.Delete(e.id)

	errs := make(chan error, 1)
	var adminSrv *http.Server
	if admin != nil {
		adminSrv = &http.Server{Handler: e.AdminHandler(), ReadHeaderTimeout: 10 * time.Second, ErrorLog: slog.NewLogLogger(e.log.Handler(), slog.LevelWarn)}
		go func() {
			if err := adminSrv.Serve(admin); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errs <- err
			}
		}()
	}
	// Under systemd the unit is of type notify; anywhere else this does
	// nothing.
	if _, err := daemon.SdNotify(false, daemon.SdNotifyReady); err != nil {
		e.log.Warn("telling systemd the edge is ready", "error", err)
	}
	defer func() {
		e.stopCaddy()
		if adminSrv != nil {
			shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			adminSrv.Shutdown(shutdown)
		}
	}()

	// The keeper stops with the edge, whichever way Run returns.
	keeperCtx, stopKeeper := context.WithCancel(ctx)
	keeperDone := make(chan struct{})
	go func() {
		defer close(keeperDone)
		e.keeper.run(keeperCtx)
	}()
	defer func() {
		stopKeeper()
		<-keeperDone
	}()

	var retry, persist, purge *time.Timer
	var retryC, persistC, purgeC <-chan time.Time
	lastVersion := e.table.Version()
	for {
		changed := e.table.Changed()
		active := e.table.Active() > 0
		switch {
		case active && !e.Running():
			if err := e.startCaddy(); err != nil {
				e.log.Error("starting the edge", "error", err)
				if retry == nil {
					retry = time.NewTimer(10 * time.Second)
				} else {
					retry.Reset(10 * time.Second)
				}
				retryC = retry.C
			} else {
				retryC = nil
			}
		case !active && e.Running():
			e.log.Info("the edge stops: no routes are left")
			e.stopCaddy()
		}
		// The keeper also stops working on certificates while the edge
		// doesn't serve.
		if stale := e.keeper.sync(); stale && e.Running() && purgeC == nil {
			// Certificates of names no longer served linger in Caddy's cache
			// until a reload drops them; gather a few changes first.
			purge = time.NewTimer(2 * time.Minute)
			purgeC = purge.C
		}
		select {
		case <-ctx.Done():
			if persistC != nil {
				persist.Stop()
				if err := e.saveRoutes(); err != nil {
					e.log.Error("keeping the routing table", "error", err)
				}
			}
			return nil
		case err := <-errs:
			return err
		case <-changed:
			if v := e.table.Version(); v != lastVersion && persistC == nil {
				lastVersion = v
				persist = time.NewTimer(time.Second)
				persistC = persist.C
			}
		case <-persistC:
			persistC = nil
			lastVersion = e.table.Version()
			if err := e.saveRoutes(); err != nil {
				e.log.Error("keeping the routing table", "error", err)
			}
		case <-e.settingsC:
			if e.Running() {
				if err := e.reloadCaddy(); err != nil {
					e.log.Error("applying the edge settings", "error", err)
				}
			}
		case <-purgeC:
			purgeC = nil
			if e.Running() {
				if err := e.reloadCaddy(); err != nil {
					e.log.Error("dropping certificates of names no longer served", "error", err)
				}
			}
		case <-retryC:
			retryC = nil
		}
	}
}

// resolveAddr turns a port of 0 into a free port, since Caddy's config
// must name the ports it serves on.
func resolveAddr(addr string) (string, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", err
	}
	if port != "0" {
		return addr, nil
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
	if err != nil {
		return "", err
	}
	defer ln.Close()
	_, port, _ = net.SplitHostPort(ln.Addr().String())
	return net.JoinHostPort(host, port), nil
}

func portOf(addr string) int {
	_, port, _ := net.SplitHostPort(addr)
	n, _ := strconv.Atoi(port)
	return n
}

func (e *Edge) startCaddy() error {
	caddyOwner.Lock()
	defer caddyOwner.Unlock()
	if caddyOwner.id != "" && caddyOwner.id != e.id {
		return errors.New("another edge runs in this process")
	}
	e.mu.Lock()
	var err error
	if e.httpAddr == "" {
		if e.httpAddr, err = resolveAddr(e.opts.HTTP); err == nil {
			e.httpsAddr, err = resolveAddr(e.opts.HTTPS)
		}
		if err != nil {
			e.httpAddr, e.httpsAddr = "", ""
			e.mu.Unlock()
			return err
		}
	}
	e.mu.Unlock()
	if err := e.loadCaddy(); err != nil {
		return err
	}
	caddyOwner.id = e.id
	e.mu.Lock()
	e.running = true
	e.mu.Unlock()
	httpAddr, httpsAddr := e.Addrs()
	e.log.Info("edge serving", "http", httpAddr, "https", httpsAddr, "routes", e.table.Len())
	return nil
}

func (e *Edge) reloadCaddy() error {
	caddyOwner.Lock()
	defer caddyOwner.Unlock()
	return e.loadCaddy()
}

// loadCaddy (re)loads Caddy's configuration. The caller holds caddyOwner.
func (e *Edge) loadCaddy() error {
	cfg, err := e.caddyConfig()
	if err != nil {
		return err
	}
	if err := caddy.Load(cfg, true); err != nil {
		e.mu.Lock()
		e.lastErr = err
		e.mu.Unlock()
		return err
	}
	e.mu.Lock()
	e.loads++
	e.lastErr = nil
	e.mu.Unlock()
	return nil
}

func (e *Edge) stopCaddy() {
	caddyOwner.Lock()
	defer caddyOwner.Unlock()
	if caddyOwner.id != e.id {
		return
	}
	if err := caddy.Stop(); err != nil {
		e.log.Warn("stopping the edge", "error", err)
	}
	caddyOwner.id = ""
	e.mu.Lock()
	e.running, e.tlsApp, e.magic = false, nil, nil
	e.mu.Unlock()
}

// Loads returns how many times Caddy's configuration was loaded, which
// route changes must never add to.
func (e *Edge) Loads() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.loads
}

// setTLS records the TLS app and certificate configuration Caddy runs
// with, as the modules see them.
func (e *Edge) setTLS(app *caddytls.TLS, magic *certmagic.Config) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if app != nil {
		e.tlsApp = app
	}
	if magic != nil {
		e.magic = magic
	}
}

func (e *Edge) tls() (*caddytls.TLS, *certmagic.Config) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.tlsApp, e.magic
}

// Health describes the edge.
type Health struct {
	Status   string `json:"status" example:"ok"`
	Running  bool   `json:"running" doc:"Whether the edge serves"`
	Routes   int    `json:"routes"`
	Version  uint64 `json:"version" doc:"Changes with every change to the routing table"`
	HTTP     string `json:"http,omitempty" doc:"Address HTTP is served on"`
	HTTPS    string `json:"https,omitempty" doc:"Address HTTPS is served on"`
	Error    string `json:"error,omitempty" doc:"Why the edge couldn't start or reload"`
	Answered bool   `json:"answered" doc:"Whether the panel answers the edge's questions"`
}

// Health reports the edge's state.
func (e *Edge) Health() Health {
	e.mu.Lock()
	h := Health{Status: "ok", Running: e.running, HTTP: e.httpAddr, HTTPS: e.httpsAddr}
	if e.lastErr != nil {
		h.Status, h.Error = "degraded", e.lastErr.Error()
	}
	e.mu.Unlock()
	h.Routes, h.Version = e.table.Len(), e.table.Version()
	if e.opts.Responder != nil {
		h.Answered = true
	} else {
		e.questions.mu.Lock()
		h.Answered = !e.questions.lastPoll.IsZero() && time.Since(e.questions.lastPoll) < pollGrace
		e.questions.mu.Unlock()
	}
	return h
}

// keptSubjects lists the names whose certificates the edge obtains ahead
// of the first visitor: hosts of routes in the auto mode.
func keptSubjects(routes []Route) []string {
	seen := map[string]bool{}
	var out []string
	for _, r := range routes {
		if r.tlsMode() != TLSAuto {
			continue
		}
		for _, h := range r.Hosts {
			if h != CatchAll && !seen[h] {
				seen[h] = true
				out = append(out, h)
			}
		}
	}
	slices.Sort(out)
	return out
}
