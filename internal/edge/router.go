package edge

import (
	"context"
	"errors"
	"fmt"
	"html"
	"net"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"github.com/caddyserver/caddy/v2/modules/caddytls"
	"go.uber.org/zap"

	"github.com/vilanobeachflorida/racksail/internal/store"
)

// caddyRouter serves the routing table. The HTTPS server's router serves
// routes with TLS and the HTTP server's serves those without, redirecting
// the others to HTTPS. Each compiles its routes as the table changes.
type caddyRouter struct {
	Instance string `json:"instance"`
	// Plain marks the router of the HTTP server.
	Plain bool `json:"plain,omitempty"`

	edge   *Edge
	ctx    caddy.Context
	logger *zap.Logger
	stop   chan struct{}
	done   chan struct{}

	mu     sync.Mutex
	routes map[string]*compiledRoute
	failed map[string]failedCompile
}

// failedCompile remembers a route that didn't compile, so requests to it
// don't each try again.
type failedCompile struct {
	hash string
	err  error
	at   time.Time
}

// retryCompile is how long a route that didn't compile waits before it is
// tried again.
const retryCompile = 30 * time.Second

// CaddyModule implements caddy.Module.
func (*caddyRouter) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{ID: "http.handlers.racksail", New: func() caddy.Module { return new(caddyRouter) }}
}

// retireAfter lets requests that started on a replaced route finish
// before its handlers are cleaned up.
const retireAfter = time.Minute

// Provision implements caddy.Provisioner.
func (h *caddyRouter) Provision(ctx caddy.Context) error {
	e, err := lookup(h.Instance)
	if err != nil {
		return err
	}
	h.edge, h.ctx, h.logger = e, ctx, ctx.Logger()
	if app, err := ctx.App("tls"); err == nil {
		e.setTLS(app.(*caddytls.TLS), nil)
	}
	h.routes, h.failed = map[string]*compiledRoute{}, map[string]failedCompile{}
	h.stop, h.done = make(chan struct{}), make(chan struct{})
	changed := e.table.Changed()
	h.sync()
	go h.watch(changed)
	return nil
}

// Cleanup implements caddy.CleanerUpper.
func (h *caddyRouter) Cleanup() error {
	close(h.stop)
	<-h.done
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, c := range h.routes {
		c.cancel()
	}
	h.routes = nil
	return nil
}

func (h *caddyRouter) watch(changed <-chan struct{}) {
	defer close(h.done)
	for {
		select {
		case <-h.stop:
			return
		case <-changed:
			changed = h.edge.table.Changed()
			h.sync()
		}
	}
}

// serves reports whether this router serves a route.
func (h *caddyRouter) serves(r Route) bool { return (r.tlsMode() == TLSOff) == h.Plain }

// sync compiles the routes this router serves that are new or changed
// and retires the others.
func (h *caddyRouter) sync() {
	routes, _ := h.edge.table.Routes(h.ctx)
	want := map[string]Route{}
	for _, r := range routes {
		if h.serves(r) {
			want[r.key()] = r
		}
	}
	h.mu.Lock()
	for key := range h.failed {
		if _, ok := want[key]; !ok {
			delete(h.failed, key)
		}
	}
	var retire []*compiledRoute
	for key, c := range h.routes {
		if r, ok := want[key]; !ok || routeHash(r) != c.hash {
			retire = append(retire, c)
			delete(h.routes, key)
		}
	}
	var todo []Route
	for key, r := range want {
		if _, ok := h.routes[key]; !ok {
			todo = append(todo, r)
		}
	}
	h.mu.Unlock()
	for _, r := range todo {
		if _, err := h.compiled(r); err != nil {
			h.edge.log.Error("compiling a route", "owner", r.Owner, "route", r.ID, "error", err)
		}
	}
	if len(retire) > 0 {
		time.AfterFunc(retireAfter, func() {
			for _, c := range retire {
				c.cancel()
			}
		})
	}
}

// compiled returns the route's handlers, compiling them when needed.
func (h *caddyRouter) compiled(r Route) (*compiledRoute, error) {
	key, hash := r.key(), routeHash(r)
	h.mu.Lock()
	if c, ok := h.routes[key]; ok && c.hash == hash {
		h.mu.Unlock()
		return c, nil
	}
	if f, ok := h.failed[key]; ok && f.hash == hash && time.Since(f.at) < retryCompile {
		h.mu.Unlock()
		return nil, f.err
	}
	h.mu.Unlock()

	ctx, cancel := caddy.NewContext(h.ctx)
	fsName := ""
	if root := h.edge.staticRoot(r); root != "" {
		fsName = fsKey(r, hash)
		ctx.FileSystems().Register(fsName, rootFS{dir: root, protected: h.edge.protectedDirs()})
	}
	cleanup := func() {
		cancel()
		if fsName != "" {
			ctx.FileSystems().Unregister(fsName)
		}
	}
	list, err := h.edge.compile(r, fsName)
	if err == nil {
		err = list.Provision(ctx)
	}
	if err != nil {
		cleanup()
		err = fmt.Errorf("route %s of %s: %w", r.ID, r.Owner, err)
		h.mu.Lock()
		if h.failed != nil {
			h.failed[key] = failedCompile{hash: hash, err: err, at: time.Now()}
		}
		h.mu.Unlock()
		return nil, err
	}
	c := &compiledRoute{route: r, hash: hash, cancel: cleanup, handler: list.Compile(caddyhttp.HandlerFunc(func(w http.ResponseWriter, r *http.Request) error {
		return caddyhttp.Error(http.StatusNotFound, errors.New("no handler answered"))
	}))}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.routes == nil {
		// The router was cleaned up meanwhile.
		cleanup()
		return nil, errors.New("the edge is reloading")
	}
	if old, ok := h.routes[key]; ok {
		if old.hash == hash {
			cleanup()
			return old, nil
		}
		time.AfterFunc(retireAfter, old.cancel)
	}
	h.routes[key] = c
	return c, nil
}

// ServeHTTP implements caddyhttp.MiddlewareHandler.
func (h *caddyRouter) ServeHTTP(w http.ResponseWriter, r *http.Request, next caddyhttp.Handler) error {
	e := h.edge
	route, ok := e.table.Match(r.Host, r.URL.Path)
	if !ok || (route.Passive && e.table.Active() == 0) {
		page(w, http.StatusNotFound, "No site here", "This address isn't set up on this server.")
		return nil
	}
	if !h.serves(route) {
		h.redirect(w, r, route.tlsMode() != TLSOff)
		return nil
	}
	c, err := h.compiled(route)
	if err != nil {
		h.logger.Error("serving a route", zap.String("route", route.key()), zap.Error(err))
		page(w, http.StatusBadGateway, "The site isn't set up right", "Its configuration couldn't be loaded. The administrator can see why in the panel.")
		return nil
	}
	e.activity.seen(route)
	tw := &trackingWriter{ResponseWriterWrapper: &caddyhttp.ResponseWriterWrapper{ResponseWriter: w}}
	err = c.handler.ServeHTTP(tw, r)
	if err == nil || tw.wrote {
		return err
	}
	status := http.StatusInternalServerError
	var he caddyhttp.HandlerError
	if errors.As(err, &he) && he.StatusCode != 0 {
		status = he.StatusCode
	}
	if status >= 500 {
		h.logger.Warn("route failed", zap.String("route", route.key()), zap.Int("status", status), zap.Error(err))
	}
	switch {
	case status == http.StatusNotFound:
		page(w, status, "Not found", "There's nothing at this address.")
	case status == http.StatusForbidden:
		page(w, status, "Not allowed", "This address can't be shown.")
	case status == http.StatusBadGateway, status == http.StatusServiceUnavailable, status == http.StatusGatewayTimeout:
		page(w, status, "The site isn't answering", "The site's application didn't respond. Try again in a moment.")
	default:
		page(w, status, http.StatusText(status), "The request couldn't be completed.")
	}
	return nil
}

// redirect sends a request to the other server: HTTPS for routes with
// TLS, plain HTTP for those without.
func (h *caddyRouter) redirect(w http.ResponseWriter, r *http.Request, toHTTPS bool) {
	httpAddr, httpsAddr := h.edge.Addrs()
	scheme, port, standard := "http", portOf(httpAddr), 80
	if toHTTPS {
		scheme, port, standard = "https", portOf(httpsAddr), 443
	}
	host := NormalizeHost(r.Host)
	if ip := net.ParseIP(host); ip != nil && ip.To4() == nil {
		host = "[" + host + "]"
	}
	if port != standard && port != 0 {
		host += ":" + strconv.Itoa(port)
	}
	http.Redirect(w, r, scheme+"://"+host+r.URL.RequestURI(), http.StatusPermanentRedirect)
}

// trackingWriter notes whether a response was started, so an error after
// that isn't answered twice.
type trackingWriter struct {
	*caddyhttp.ResponseWriterWrapper
	wrote bool
}

func (t *trackingWriter) WriteHeader(code int) {
	if code >= 200 || code == http.StatusSwitchingProtocols {
		t.wrote = true
	}
	t.ResponseWriterWrapper.WriteHeader(code)
}

func (t *trackingWriter) Write(b []byte) (int, error) {
	t.wrote = true
	return t.ResponseWriterWrapper.Write(b)
}

func page(w http.ResponseWriter, code int, title, message string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(code)
	fmt.Fprintf(w, "<!doctype html><html lang=\"en\"><meta charset=\"utf-8\"><meta name=\"viewport\" content=\"width=device-width\"><title>%s</title>"+
		"<body style=\"font-family:system-ui,sans-serif;margin:4rem auto;max-width:32rem;padding:0 1rem\"><h1>%s</h1><p>%s</p></body></html>",
		html.EscapeString(title), html.EscapeString(title), html.EscapeString(message))
}

// activity counts requests per route, for the panel to see which sites
// are used.
type activity struct {
	mu     sync.Mutex
	routes map[string]*routeActivity
}

type routeActivity struct {
	owner    store.ID
	id       string
	requests atomic.Int64
	last     atomic.Int64
}

func newActivity() *activity { return &activity{routes: map[string]*routeActivity{}} }

func (a *activity) seen(r Route) {
	key := r.key()
	a.mu.Lock()
	ra, ok := a.routes[key]
	if !ok {
		ra = &routeActivity{owner: r.Owner, id: r.ID}
		a.routes[key] = ra
	}
	a.mu.Unlock()
	ra.requests.Add(1)
	ra.last.Store(time.Now().UnixNano())
}

// RouteActivity is how much a route was used since the edge started.
type RouteActivity struct {
	Owner       store.ID  `json:"owner"`
	Route       string    `json:"route"`
	Requests    int64     `json:"requests"`
	LastRequest time.Time `json:"lastRequest"`
}

// Activity lists how much each route in the table was used, most recently
// used first.
func (e *Edge) Activity() []RouteActivity {
	routes, _ := e.table.Routes(context.Background())
	current := make(map[string]bool, len(routes))
	for _, r := range routes {
		current[r.key()] = true
	}
	e.activity.mu.Lock()
	out := make([]RouteActivity, 0, len(e.activity.routes))
	for key, ra := range e.activity.routes {
		if !current[key] {
			// Routes that went away are forgotten.
			delete(e.activity.routes, key)
			continue
		}
		out = append(out, RouteActivity{Owner: ra.owner, Route: ra.id, Requests: ra.requests.Load(), LastRequest: time.Unix(0, ra.last.Load()).UTC()})
	}
	e.activity.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].LastRequest.After(out[j].LastRequest) })
	return out
}
