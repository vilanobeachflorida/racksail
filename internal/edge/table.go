package edge

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"path"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/store"
)

// HandlerType is what a route does with a request.
type HandlerType string

// Handler types.
const (
	HandleStatic   HandlerType = "static"
	HandleProxy    HandlerType = "proxy"
	HandleFastCGI  HandlerType = "fastcgi"
	HandleRedirect HandlerType = "redirect"
	HandleRespond  HandlerType = "respond"
)

// Upstream is a backend a route sends requests to.
type Upstream struct {
	// Network is tcp or unix.
	Network string `json:"network" enum:"tcp,unix"`
	// Address is host:port for tcp and a socket path for unix.
	Address string `json:"address"`
	Weight  int    `json:"weight,omitempty"`
	// TLS speaks HTTPS to an upstream on this machine, such as the panel,
	// trusting its certificate since nothing else can answer on loopback.
	TLS bool `json:"tls,omitempty" doc:"Speak HTTPS to an upstream on a loopback address"`
}

func loopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Handler says how a route answers.
type Handler struct {
	Type HandlerType `json:"type" enum:"static,proxy,fastcgi,redirect,respond"`
	// Root is the document root for static files and FastCGI scripts.
	Root      string     `json:"root,omitempty"`
	Upstreams []Upstream `json:"upstreams,omitempty"`
	// Index is the front controller for FastCGI, such as index.php.
	Index string `json:"index,omitempty"`
	// SplitPath ends FastCGI script names, .php by default.
	SplitPath []string `json:"splitPath,omitempty" doc:"Extensions that end a FastCGI script name; .php by default"`
	// Env adds FastCGI parameters, such as APP_ENV.
	Env map[string]string `json:"env,omitempty" doc:"Extra FastCGI parameters"`
	// Fallback is served for static paths that don't exist, such as
	// /index.html for an application that routes in the browser.
	Fallback string `json:"fallback,omitempty" doc:"File served when a static path doesn't exist"`
	// StripPath removes the route's path prefix before proxying.
	StripPath bool `json:"stripPath,omitempty" doc:"Remove the route's path prefix before proxying"`
	// HealthPath turns on active health checks of the upstreams.
	HealthPath string `json:"healthPath,omitempty" doc:"Path upstreams answer health checks on"`
	// To is where a redirect points; it may end with {uri}.
	To   string `json:"to,omitempty"`
	Code int    `json:"code,omitempty"`
	Body string `json:"body,omitempty"`
	// WakeTimeout allows a sleeping, socket-activated upstream time to start
	// on the first request.
	WakeTimeout time.Duration `json:"wakeTimeout,omitempty"`
}

// TLSMode says how a route gets its certificate.
type TLSMode string

// TLS modes.
const (
	TLSAuto        TLSMode = "auto"
	TLSOnDemand    TLSMode = "on-demand"
	TLSCertificate TLSMode = "certificate"
	TLSOff         TLSMode = "off"
)

// TLS configures HTTPS for a route.
type TLS struct {
	Mode TLSMode `json:"mode" enum:"auto,on-demand,certificate,off"`
	// Certificate names an installed certificate for the certificate mode.
	Certificate string `json:"certificate,omitempty"`
	// Ask has the panel approve every name before an on-demand certificate
	// is obtained for it, for routes that match names nobody listed, such
	// as customers' own domains on a catch-all route.
	Ask bool `json:"ask,omitempty" doc:"Ask the panel before obtaining an on-demand certificate for a name"`
}

// Header is a response header a route adds.
type Header struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Route sends requests for some hosts and a path prefix to a handler.
type Route struct {
	// ID names the route within its owner, such as "main" or "webmail".
	ID    string   `json:"id"`
	Owner store.ID `json:"owner"`
	// Hosts are names, wildcards such as *.example.com, IP addresses, or
	// "*" for any name no other route has.
	Hosts    []string `json:"hosts"`
	Path     string   `json:"path,omitempty"`
	Priority int      `json:"priority,omitempty"`
	Handler  Handler  `json:"handler"`
	TLS      TLS      `json:"tls"`
	Headers  []Header `json:"headers,omitempty"`
	// Passive routes are served only while the table has other routes, so
	// they never start the edge on their own. The panel's own hostname is
	// one.
	Passive bool `json:"passive,omitempty" doc:"Serve only while other routes exist"`
}

// CatchAll is the host of a route that answers names no other route has.
const CatchAll = "*"

// Limits of a route.
const (
	maxHosts       = 100
	maxUpstreams   = 64
	maxHeaders     = 50
	maxWakeTimeout = 10 * time.Minute
)

var (
	hostPattern    = regexp.MustCompile(`^(\*\.)?([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)*[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	routeIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	headerPattern  = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)
	envPattern     = regexp.MustCompile(`^[A-Z_][A-Z0-9_]{0,63}$`)
	splitPattern   = regexp.MustCompile(`^\.[a-z0-9]{1,15}$`)
	certPattern    = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)
)

func cleanURLPath(p string) bool {
	return strings.HasPrefix(p, "/") && path.Clean(p) == p && !strings.ContainsAny(p, "\x00\r\n")
}

// Validate checks a route.
func (r Route) Validate() error {
	if !routeIDPattern.MatchString(r.ID) {
		return fmt.Errorf("route id %q", r.ID)
	}
	if !r.Owner.Valid() {
		return fmt.Errorf("route %s: owner %q isn't an id", r.ID, r.Owner)
	}
	if len(r.Hosts) == 0 || len(r.Hosts) > maxHosts {
		return fmt.Errorf("route %s needs between 1 and %d hosts", r.ID, maxHosts)
	}
	for _, h := range r.Hosts {
		switch {
		case h == CatchAll:
			// A catch-all route can only get certificates the panel
			// approves name by name.
			if r.TLS.Mode != TLSOff && !(r.TLS.Mode == TLSOnDemand && r.TLS.Ask) {
				return fmt.Errorf("route %s: a catch-all route needs TLS off, or on-demand with ask", r.ID)
			}
		case hostPattern.MatchString(h), net.ParseIP(h) != nil:
		default:
			return fmt.Errorf("route %s: host %q", r.ID, h)
		}
	}
	if r.Path != "" && !cleanURLPath(r.Path) {
		return fmt.Errorf("route %s: path %q must be a clean prefix starting with /", r.ID, r.Path)
	}
	if len(r.Headers) > maxHeaders {
		return fmt.Errorf("route %s has more than %d headers", r.ID, maxHeaders)
	}
	for _, h := range r.Headers {
		if !headerPattern.MatchString(h.Name) || strings.ContainsAny(h.Value, "\r\n\x00") || len(h.Value) > 4096 {
			return fmt.Errorf("route %s: header %q", r.ID, h.Name)
		}
	}
	switch r.TLS.Mode {
	case TLSAuto, TLSOnDemand, TLSOff, "":
	case TLSCertificate:
		if !certPattern.MatchString(r.TLS.Certificate) {
			return fmt.Errorf("route %s: the certificate mode needs a certificate name", r.ID)
		}
	default:
		return fmt.Errorf("route %s: TLS mode %q", r.ID, r.TLS.Mode)
	}
	if r.TLS.Ask && r.TLS.Mode != TLSOnDemand {
		return fmt.Errorf("route %s: only on-demand certificates are asked for", r.ID)
	}
	h := r.Handler
	switch h.Type {
	case HandleStatic:
		if !path.IsAbs(h.Root) || path.Clean(h.Root) != h.Root {
			return fmt.Errorf("route %s: static files need an absolute root", r.ID)
		}
		if h.Fallback != "" && !cleanURLPath(h.Fallback) {
			return fmt.Errorf("route %s: fallback %q must be a path starting with /", r.ID, h.Fallback)
		}
	case HandleFastCGI:
		if !path.IsAbs(h.Root) || path.Clean(h.Root) != h.Root || len(h.Upstreams) == 0 {
			return fmt.Errorf("route %s: FastCGI needs an absolute root and an upstream", r.ID)
		}
		if h.Index != "" && (strings.ContainsAny(h.Index, "/\x00") || h.Index == "." || h.Index == "..") {
			return fmt.Errorf("route %s: index %q must be a file name", r.ID, h.Index)
		}
		for _, s := range h.SplitPath {
			if !splitPattern.MatchString(s) {
				return fmt.Errorf("route %s: split path %q", r.ID, s)
			}
		}
		for k, v := range h.Env {
			if !envPattern.MatchString(k) || strings.ContainsAny(v, "\x00\r\n") || len(v) > 4096 {
				return fmt.Errorf("route %s: FastCGI parameter %q", r.ID, k)
			}
		}
	case HandleProxy:
		if len(h.Upstreams) == 0 {
			return fmt.Errorf("route %s: a proxy needs upstreams", r.ID)
		}
	case HandleRedirect:
		if h.To == "" || strings.ContainsAny(h.To, "\r\n\x00") || (h.Code != 0 && (h.Code < 300 || h.Code > 308)) {
			return fmt.Errorf("route %s: a redirect needs a target and a 3xx code", r.ID)
		}
	case HandleRespond:
		if h.Code != 0 && (h.Code < 200 || h.Code > 599) {
			return fmt.Errorf("route %s: status %d", r.ID, h.Code)
		}
		if len(h.Body) > 64<<10 {
			return fmt.Errorf("route %s: the body is larger than 64 KiB", r.ID)
		}
	default:
		return fmt.Errorf("route %s: handler type %q", r.ID, h.Type)
	}
	if h.HealthPath != "" && !cleanURLPath(h.HealthPath) {
		return fmt.Errorf("route %s: health path %q must start with /", r.ID, h.HealthPath)
	}
	if h.WakeTimeout < 0 || h.WakeTimeout > maxWakeTimeout {
		return fmt.Errorf("route %s: wake timeout must be at most %s", r.ID, maxWakeTimeout)
	}
	if len(h.Upstreams) > maxUpstreams {
		return fmt.Errorf("route %s has more than %d upstreams", r.ID, maxUpstreams)
	}
	for _, u := range h.Upstreams {
		if u.TLS != h.Upstreams[0].TLS || (u.TLS && h.Type != HandleProxy) {
			return fmt.Errorf("route %s: every upstream of a proxy speaks HTTPS or none does", r.ID)
		}
	}
	for _, u := range h.Upstreams {
		if u.Weight < 0 || u.Weight > 100 {
			return fmt.Errorf("route %s: upstream weight %d must be between 0 and 100", r.ID, u.Weight)
		}
		switch u.Network {
		case "unix":
			if !path.IsAbs(u.Address) || path.Clean(u.Address) != u.Address {
				return fmt.Errorf("route %s: socket %q must be absolute", r.ID, u.Address)
			}
		case "tcp":
			host, port, err := net.SplitHostPort(u.Address)
			if err != nil || host == "" || port == "" || strings.ContainsAny(u.Address, "/\x00 ") {
				return fmt.Errorf("route %s: upstream %q isn't host:port", r.ID, u.Address)
			}
			if u.TLS && !loopbackHost(host) {
				return fmt.Errorf("route %s: HTTPS upstreams must be on a loopback address, not %s", r.ID, host)
			}
		default:
			return fmt.Errorf("route %s: upstream network %q", r.ID, u.Network)
		}
	}
	return nil
}

// tlsMode is the route's TLS mode with the default filled in.
func (r Route) tlsMode() TLSMode {
	if r.TLS.Mode == "" {
		return TLSAuto
	}
	return r.TLS.Mode
}

// key identifies a route in the table.
func (r Route) key() string { return string(r.Owner) + "/" + r.ID }

// Router manages the routing table.
type Router interface {
	// SetRoutes replaces every route of an owner.
	SetRoutes(ctx context.Context, owner store.ID, routes []Route) error
	DeleteRoutes(ctx context.Context, owner store.ID) error
	Routes(ctx context.Context) ([]Route, error)
}

// Table is the routing table. It is safe for concurrent use.
type Table struct {
	mu      sync.RWMutex
	byOwner map[store.ID][]Route
	sorted  []Route
	active  int
	version uint64
	watch   []chan struct{}
}

// NewTable returns an empty table.
func NewTable() *Table { return &Table{byOwner: map[store.ID][]Route{}} }

// SetRoutes implements Router.
func (t *Table) SetRoutes(ctx context.Context, owner store.ID, routes []Route) error {
	if !owner.Valid() {
		return fmt.Errorf("owner %q isn't an id", owner)
	}
	routes = append([]Route(nil), routes...)
	seen := map[string]bool{}
	for i := range routes {
		if routes[i].Owner == "" {
			routes[i].Owner = owner
		}
		if routes[i].Owner != owner {
			return fmt.Errorf("route %s belongs to %s, not %s", routes[i].ID, routes[i].Owner, owner)
		}
		if err := routes[i].Validate(); err != nil {
			return err
		}
		if seen[routes[i].ID] {
			return fmt.Errorf("route %s is listed twice for %s", routes[i].ID, owner)
		}
		seen[routes[i].ID] = true
	}
	t.mu.Lock()
	if len(routes) == 0 {
		delete(t.byOwner, owner)
	} else {
		t.byOwner[owner] = routes
	}
	t.rebuild()
	t.mu.Unlock()
	return nil
}

// DeleteRoutes implements Router.
func (t *Table) DeleteRoutes(ctx context.Context, owner store.ID) error {
	return t.SetRoutes(ctx, owner, nil)
}

// Routes implements Router, in matching order.
func (t *Table) Routes(ctx context.Context) ([]Route, error) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return append([]Route(nil), t.sorted...), nil
}

// Len returns how many routes the table has.
func (t *Table) Len() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return len(t.sorted)
}

// Active returns how many routes aren't passive. The edge serves only
// while there is one.
func (t *Table) Active() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.active
}

// Version increases with every change.
func (t *Table) Version() uint64 {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.version
}

// Changed returns a channel that is closed at the next change.
func (t *Table) Changed() <-chan struct{} {
	t.mu.Lock()
	defer t.mu.Unlock()
	ch := make(chan struct{})
	t.watch = append(t.watch, ch)
	return ch
}

// rebuild orders routes for matching: higher priority, then longer paths,
// then exact hosts before wildcards. The caller holds t.mu.
func (t *Table) rebuild() {
	var all []Route
	active := 0
	for _, rs := range t.byOwner {
		for _, r := range rs {
			all = append(all, r)
			if !r.Passive {
				active++
			}
		}
	}
	sort.SliceStable(all, func(i, j int) bool {
		a, b := all[i], all[j]
		if a.Priority != b.Priority {
			return a.Priority > b.Priority
		}
		if len(a.Path) != len(b.Path) {
			return len(a.Path) > len(b.Path)
		}
		if a.Owner != b.Owner {
			return a.Owner < b.Owner
		}
		return a.ID < b.ID
	})
	t.sorted, t.active = all, active
	t.version++
	for _, ch := range t.watch {
		close(ch)
	}
	t.watch = nil
}

// NormalizeHost lowercases a request's host and drops its port and the
// brackets of an IPv6 address.
func NormalizeHost(host string) string {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
}

// Match finds the route for a host and path. Exact host names win over
// wildcards, and wildcards over a catch-all route.
func (t *Table) Match(host, reqPath string) (Route, bool) {
	host = NormalizeHost(host)
	t.mu.RLock()
	defer t.mu.RUnlock()
	for _, pass := range []int{0, 1, 2} {
		for _, r := range t.sorted {
			if !pathMatches(r.Path, reqPath) {
				continue
			}
			for _, h := range r.Hosts {
				switch {
				case pass == 0 && h == host:
					return r, true
				case pass == 1 && wildcardMatches(h, host):
					return r, true
				case pass == 2 && h == CatchAll:
					return r, true
				}
			}
		}
	}
	return Route{}, false
}

// wildcardMatches reports whether a pattern such as *.example.com covers
// host, one label deep.
func wildcardMatches(pattern, host string) bool {
	if !strings.HasPrefix(pattern, "*.") {
		return false
	}
	suffix := pattern[1:]
	return strings.HasSuffix(host, suffix) && len(host) > len(suffix) && !strings.Contains(strings.TrimSuffix(host, suffix), ".")
}

func pathMatches(prefix, p string) bool {
	if prefix == "" || prefix == "/" {
		return true
	}
	return p == prefix || strings.HasPrefix(p, prefix+"/")
}

// save writes the table as JSON, every owner's routes in matching order.
func (t *Table) save(w io.Writer) error {
	routes, _ := t.Routes(context.Background())
	if routes == nil {
		routes = []Route{}
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(routes)
}

// load replaces the table with routes saved by save. Routes that no
// longer validate are dropped and reported.
func (t *Table) load(r io.Reader) ([]error, error) {
	var routes []Route
	if err := json.NewDecoder(r).Decode(&routes); err != nil {
		return nil, err
	}
	byOwner := map[store.ID][]Route{}
	var dropped []error
	for _, rt := range routes {
		if err := rt.Validate(); err != nil {
			dropped = append(dropped, err)
			continue
		}
		byOwner[rt.Owner] = append(byOwner[rt.Owner], rt)
	}
	t.mu.Lock()
	t.byOwner = byOwner
	t.rebuild()
	t.mu.Unlock()
	return dropped, nil
}
