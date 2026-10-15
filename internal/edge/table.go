package edge

import (
	"context"
	"fmt"
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
}

// Handler says how a route answers.
type Handler struct {
	Type HandlerType `json:"type" enum:"static,proxy,fastcgi,redirect,respond"`
	// Root is the document root for static files and FastCGI scripts.
	Root      string     `json:"root,omitempty"`
	Upstreams []Upstream `json:"upstreams,omitempty"`
	// Index is the front controller for FastCGI, such as index.php.
	Index string `json:"index,omitempty"`
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
}

// Header is a response header a route adds.
type Header struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Route sends requests for some hosts and a path prefix to a handler.
type Route struct {
	// ID names the route within its owner, such as "main" or "webmail".
	ID       string   `json:"id"`
	Owner    store.ID `json:"owner"`
	Hosts    []string `json:"hosts"`
	Path     string   `json:"path,omitempty"`
	Priority int      `json:"priority,omitempty"`
	Handler  Handler  `json:"handler"`
	TLS      TLS      `json:"tls"`
	Headers  []Header `json:"headers,omitempty"`
}

var (
	hostPattern    = regexp.MustCompile(`^(\*\.)?([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)*[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	routeIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	headerPattern  = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)
)

// Validate checks a route.
func (r Route) Validate() error {
	if !routeIDPattern.MatchString(r.ID) {
		return fmt.Errorf("route id %q", r.ID)
	}
	if !r.Owner.Valid() {
		return fmt.Errorf("route %s: owner %q isn't an id", r.ID, r.Owner)
	}
	if len(r.Hosts) == 0 {
		return fmt.Errorf("route %s has no hosts", r.ID)
	}
	for _, h := range r.Hosts {
		if !hostPattern.MatchString(h) && net.ParseIP(h) == nil {
			return fmt.Errorf("route %s: host %q", r.ID, h)
		}
	}
	if r.Path != "" && (!strings.HasPrefix(r.Path, "/") || path.Clean(r.Path) != r.Path) {
		return fmt.Errorf("route %s: path %q must be a clean prefix starting with /", r.ID, r.Path)
	}
	for _, h := range r.Headers {
		if !headerPattern.MatchString(h.Name) || strings.ContainsAny(h.Value, "\r\n") {
			return fmt.Errorf("route %s: header %q", r.ID, h.Name)
		}
	}
	switch r.TLS.Mode {
	case TLSAuto, TLSOnDemand, TLSOff, "":
	case TLSCertificate:
		if r.TLS.Certificate == "" {
			return fmt.Errorf("route %s: the certificate mode needs a certificate name", r.ID)
		}
	default:
		return fmt.Errorf("route %s: TLS mode %q", r.ID, r.TLS.Mode)
	}
	h := r.Handler
	switch h.Type {
	case HandleStatic:
		if !path.IsAbs(h.Root) {
			return fmt.Errorf("route %s: static files need an absolute root", r.ID)
		}
	case HandleFastCGI:
		if !path.IsAbs(h.Root) || len(h.Upstreams) == 0 {
			return fmt.Errorf("route %s: FastCGI needs an absolute root and an upstream", r.ID)
		}
	case HandleProxy:
		if len(h.Upstreams) == 0 {
			return fmt.Errorf("route %s: a proxy needs upstreams", r.ID)
		}
	case HandleRedirect:
		if h.To == "" || (h.Code != 0 && (h.Code < 300 || h.Code > 308)) {
			return fmt.Errorf("route %s: a redirect needs a target and a 3xx code", r.ID)
		}
	case HandleRespond:
		if h.Code != 0 && (h.Code < 200 || h.Code > 599) {
			return fmt.Errorf("route %s: status %d", r.ID, h.Code)
		}
	default:
		return fmt.Errorf("route %s: handler type %q", r.ID, h.Type)
	}
	for _, u := range h.Upstreams {
		switch u.Network {
		case "unix":
			if !path.IsAbs(u.Address) {
				return fmt.Errorf("route %s: socket %q must be absolute", r.ID, u.Address)
			}
		case "tcp":
			if _, _, err := net.SplitHostPort(u.Address); err != nil {
				return fmt.Errorf("route %s: upstream %q: %v", r.ID, u.Address, err)
			}
		default:
			return fmt.Errorf("route %s: upstream network %q", r.ID, u.Network)
		}
	}
	return nil
}

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
	version uint64
	watch   []chan struct{}
}

// NewTable returns an empty table.
func NewTable() *Table { return &Table{byOwner: map[store.ID][]Route{}} }

// SetRoutes implements Router.
func (t *Table) SetRoutes(ctx context.Context, owner store.ID, routes []Route) error {
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
		t.byOwner[owner] = append([]Route(nil), routes...)
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
	for _, rs := range t.byOwner {
		all = append(all, rs...)
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
	t.sorted = all
	t.version++
	for _, ch := range t.watch {
		close(ch)
	}
	t.watch = nil
}

// Match finds the route for a host and path. Exact host names win over
// wildcards.
func (t *Table) Match(host, reqPath string) (Route, bool) {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	for _, exact := range []bool{true, false} {
		for _, r := range t.sorted {
			if !pathMatches(r.Path, reqPath) {
				continue
			}
			for _, h := range r.Hosts {
				if exact && h == host {
					return r, true
				}
				if !exact && strings.HasPrefix(h, "*.") && strings.HasSuffix(host, h[1:]) && !strings.Contains(strings.TrimSuffix(host, h[1:]), ".") {
					return r, true
				}
			}
		}
	}
	return Route{}, false
}

func pathMatches(prefix, p string) bool {
	if prefix == "" || prefix == "/" {
		return true
	}
	return p == prefix || strings.HasPrefix(p, prefix+"/")
}
