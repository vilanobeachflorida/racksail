package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"reflect"
	"regexp"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"

	"github.com/vilanobeachflorida/racksail/internal/api/httpapi"
	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/auth/account"
	"github.com/vilanobeachflorida/racksail/internal/auth/identity"
	"github.com/vilanobeachflorida/racksail/internal/changeset"
	"github.com/vilanobeachflorida/racksail/internal/events"
	"github.com/vilanobeachflorida/racksail/internal/events/webhook"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/inbox"
	"github.com/vilanobeachflorida/racksail/internal/jobs"
	"github.com/vilanobeachflorida/racksail/internal/modules"
	"github.com/vilanobeachflorida/racksail/internal/store"
	"github.com/vilanobeachflorida/racksail/internal/updates"
)

// Version is the version of the API document.
const Version = "1.0.0"

// Prefix is where the API lives.
const Prefix = "/api/v1"

// Security scheme names in the OpenAPI document.
const (
	SessionScheme = "session"
	TokenScheme   = "token"
)

// SessionCookie is the name of the session cookie.
const SessionCookie = auth.SessionCookie

// Protected is the security requirement of operations that need a signed-in
// actor: a session cookie or an API token.
var Protected = []map[string][]string{{SessionScheme: {}}, {TokenScheme: {}}}

// Check is one health check.
type Check struct {
	Name   string `json:"name" example:"agent"`
	Status string `json:"status" enum:"ok,failing" example:"ok"`
	// Message explains a failing check.
	Message string `json:"message,omitempty"`
}

// HealthReport is the answer of the health endpoint.
type HealthReport struct {
	Status string  `json:"status" enum:"ok,degraded" example:"ok" doc:"ok when every check passes"`
	Checks []Check `json:"checks"`
}

// HealthCheck runs one check; a nil error means it passed.
type HealthCheck struct {
	Name  string
	Check func(ctx context.Context) error
	// Active reports whether the check applies right now, such as the checks
	// of a module only while it is on. Nil means always.
	Active func() bool
}

// healthTimeout bounds each health check. The checks run side by side, so
// it also bounds the whole report.
const healthTimeout = 5 * time.Second

// Options are the dependencies of the server.
type Options struct {
	// Auth identifies callers. Without one, operations that need a
	// signed-in actor are refused, except in development mode.
	Auth auth.Authenticator
	// Dev is racksail dev: without Auth, every request is signed in as the
	// developer, an administrator, which the log says when the server is
	// built.
	Dev bool
	// Identity is who signs in and how: the sign-in operations, sessions,
	// tokens and users are its. Without it they answer api.no_sign_in.
	Identity *identity.Service
	// ModuleStates reports each module's state for the session document.
	ModuleStates func() map[string]modules.State
	// Accounts says which reseller each account belongs to, and learns
	// which accounts are being opened for whom.
	Accounts *account.Ownership
	Modules  *modules.Registry
	// Services are what operations work with, and what modules that add
	// operations are given. Services.Kinds, when nil, is the process-wide
	// kind registry, so the document lists every kind.
	Services *modules.Services
	// Timeline serves the event timeline and its live stream.
	Timeline *events.Log
	// Webhooks serves the delivery logs of webhooks.
	Webhooks *webhook.Dispatcher
	// Inbox serves the attention inbox.
	Inbox  *inbox.Service
	Health []HealthCheck
	// AllowFrom limits who may reach the panel to these networks when it
	// isn't empty: panel.allow_from in racksail.toml. Requests from anywhere
	// else are refused before anything else happens.
	AllowFrom []netip.Prefix
	// RateLimit bounds how often callers may call the API.
	RateLimit RateLimit
	// Metrics returns the panel's metrics for GET /api/v1/metrics, besides
	// those of the API itself; nil means those alone.
	Metrics func(ctx context.Context) ([]Metric, error)
	// UI serves everything outside /api.
	UI  http.Handler
	Log *slog.Logger
	Now func() time.Time
}

// Errors of the API itself.
var (
	// ErrAddressNotAllowed refuses requests from outside panel.allow_from.
	ErrAddressNotAllowed = i18n.Define("api.address_not_allowed", http.StatusForbidden, "The panel doesn't accept requests from {address}.")
	// ErrNoSignIn refuses operations that need a signed-in actor on a panel
	// that has no way to sign anyone in.
	ErrNoSignIn = i18n.Define("api.no_sign_in", http.StatusServiceUnavailable, "This panel has no way to sign anyone in yet, so it refuses requests that need a signed-in user.")
)

// Server is the panel API.
type Server struct {
	opts    Options
	mux     *http.ServeMux
	api     huma.API
	kinds   *store.KindRegistry
	docs    map[string]kindDoc
	limiter *limiter
	// catalog is the answer of GET /events/types.
	catalog eventCatalog
	// crossOrigin keeps other sites' pages from changing anything with the
	// session, and credentials are the patterns of paths that carry a
	// token of their own instead, as segments.
	crossOrigin *http.CrossOriginProtection
	credentials [][]string
	// noSignIn is set when no authenticator is configured outside
	// development mode.
	noSignIn bool
	// closing ends when Close is called: live streams and waits for jobs
	// end with it, so shutting down isn't held up by them.
	closing  context.Context
	close    context.CancelFunc
	streams  atomic.Int64
	requests atomic.Int64
}

// Close ends the live streams and the waits for actions to finish, at once
// and for requests that come later, so the HTTP server's Shutdown, which
// waits for every request to finish, isn't held up by clients that would
// otherwise stay connected. Clients reconnect to streams by themselves. The
// panel calls it from http.Server.RegisterOnShutdown.
func (s *Server) Close() { s.close() }

// New builds the server.
func New(opts Options) *Server {
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Services == nil {
		opts.Services = &modules.Services{}
	}
	var noSignIn bool
	if opts.Auth == nil {
		if opts.Dev {
			opts.Auth = auth.DevAuthenticator{}
			opts.Log.Warn("development mode: every request is signed in as the developer, an administrator; the panel only listens on loopback addresses")
		} else {
			opts.Auth = auth.NoAuthenticator{}
			noSignIn = true
		}
	}
	kinds := opts.Services.Kinds
	if kinds == nil {
		kinds = store.Kinds()
	}
	mux := http.NewServeMux()
	cfg := httpapi.Config("Racksail API", Version)
	cfg.Transformers = append(cfg.Transformers, retryAfterHeader)
	cfg.Info.Description = "The API of the Racksail panel. Every change goes through the change pipeline: " +
		"mutating calls accept dryRun=true to preview the change set without applying it, and an Idempotency-Key header. " +
		"Errors are RFC 9457 problem details with a stable code."
	cfg.Info.License = &huma.License{Name: "AGPL-3.0-or-later", Identifier: "AGPL-3.0-or-later"}
	cfg.Components.SecuritySchemes = map[string]*huma.SecurityScheme{
		SessionScheme: {Type: "apiKey", In: "cookie", Name: SessionCookie, Description: "The session cookie set when signing in to the panel."},
		TokenScheme:   {Type: "http", Scheme: "bearer", Description: "An API token, scoped to permissions and accounts."},
	}
	closing, close := context.WithCancel(context.Background())
	s := &Server{
		opts: opts, mux: mux, api: humago.New(mux, cfg), kinds: kinds, docs: map[string]kindDoc{},
		limiter: newLimiter(opts.RateLimit, opts.Now), noSignIn: noSignIn, closing: closing, close: close,
		crossOrigin: http.NewCrossOriginProtection(),
	}
	if noSignIn && opts.Services.Store != nil {
		opts.Log.Error("the panel has no way to sign anyone in, so it refuses every request that needs a signed-in user")
	}
	s.api.UseMiddleware(s.requireActor, s.gate)
	s.registerSystem()
	s.registerAuth()
	s.registerSession()
	s.registerMe()
	s.registerUsers()
	s.registerCatalog()
	s.registerKindDocs()
	s.registerSchemaKinds()
	s.registerResources()
	s.registerChangeSets()
	s.registerJobs()
	s.registerTimeline()
	s.registerSearch()
	s.registerInbox()
	s.registerMetrics()
	if opts.Modules != nil {
		for _, m := range opts.Modules.All() {
			if p, ok := m.(modules.APIProvider); ok {
				p.RegisterAPI(s.api, opts.Services)
			}
		}
	}
	s.addExtensions()
	// The operations of kinds come last, so the schemas they build take
	// names no Go type has.
	s.registerKinds()
	if _, err := s.eventTypeList(); err != nil {
		// Like a schema Huma can't build, this is a mistake in the code.
		panic(fmt.Sprintf("api: describing event types: %v", err))
	}
	mux.HandleFunc("GET "+Prefix+"/openapi.json", s.serveOpenAPI)
	// Everything under /api is the API's, so paths no operation matches get
	// a problem rather than the UI's page.
	mux.HandleFunc(apiRoot, s.unmatched)
	mux.HandleFunc(apiRoot+"/", s.unmatched)
	if opts.UI != nil {
		mux.Handle("/", opts.UI)
	}
	return s
}

const apiRoot = "/api"

// methods are the methods operations use, in the order Allow lists them.
var methods = []string{http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete}

// unmatched answers requests under /api that no operation matches: 405 with
// an Allow header when the path has operations for other methods, and 404
// otherwise.
func (s *Server) unmatched(w http.ResponseWriter, r *http.Request) {
	var allowed []string
	for _, m := range methods {
		if m == r.Method {
			continue
		}
		probe := &http.Request{Method: m, URL: r.URL, Host: r.Host, Header: http.Header{}}
		if _, pattern := s.mux.Handler(probe); pattern != "" && pattern != apiRoot && pattern != apiRoot+"/" {
			allowed = append(allowed, m)
		}
	}
	if len(allowed) == 0 {
		writeProblem(w, i18n.ErrNotFound.New("what", "The API path "+r.URL.Path))
		return
	}
	w.Header().Set("Allow", strings.Join(allowed, ", "))
	choices := strings.Join(allowed[:len(allowed)-1], ", ")
	if choices != "" {
		choices += " or "
	}
	choices += allowed[len(allowed)-1]
	writeProblem(w, i18n.ErrMethodNotAllowed.New("path", r.URL.Path, "method", r.Method, "allowed", choices))
}

// API returns the Huma API, for registering operations.
func (s *Server) API() huma.API { return s.api }

// OpenAPI returns the OpenAPI document.
func (s *Server) OpenAPI() ([]byte, error) {
	return json.MarshalIndent(s.api.OpenAPI(), "", "  ")
}

func (s *Server) serveOpenAPI(w http.ResponseWriter, r *http.Request) {
	doc, err := s.OpenAPI()
	if err != nil {
		writeProblem(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/vnd.oai.openapi+json")
	w.Write(doc)
}

type healthOutput struct {
	Status int
	Body   HealthReport
}

type versionOutput struct {
	Body updates.BuildInfo
}

func (s *Server) registerSystem() {
	report := s.api.OpenAPI().Components.Schemas.Schema(reflect.TypeFor[HealthReport](), true, "HealthReport")
	op := huma.Operation{
		OperationID: "getHealth",
		Method:      http.MethodGet,
		Path:        Prefix + "/health",
		Summary:     "Check that the panel is working",
		Description: "Runs the panel's health checks, such as whether the agent answers, and those of every module that is on. " +
			"Answers 200 when every check passes and 503 when one fails, so load balancers and uptime monitors can use it. It needs no sign-in.",
		Tags: []string{"System"},
		Responses: map[string]*huma.Response{
			"503": {Description: "A check failed", Content: map[string]*huma.MediaType{"application/json": {Schema: report}}},
		},
	}
	Describe(&op, Info{Risk: store.RiskRead})
	huma.Register(s.api, op, func(ctx context.Context, _ *struct{}) (*healthOutput, error) {
		out := &healthOutput{Status: http.StatusOK, Body: HealthReport{Status: "ok", Checks: s.runChecks(ctx)}}
		for _, c := range out.Body.Checks {
			if c.Status != "ok" {
				out.Status, out.Body.Status = http.StatusServiceUnavailable, "degraded"
			}
		}
		return out, nil
	})
	op = huma.Operation{
		OperationID: "getVersion",
		Method:      http.MethodGet,
		Path:        Prefix + "/version",
		Summary:     "Show the Racksail version",
		Description: "Reports the version, release channel and build of the running panel. It needs no sign-in.",
		Tags:        []string{"System"},
	}
	Describe(&op, Info{Risk: store.RiskRead})
	huma.Register(s.api, op, func(ctx context.Context, _ *struct{}) (*versionOutput, error) {
		return &versionOutput{Body: updates.Build()}, nil
	})
}

// services returns what operations work with; parts may be missing, as
// when the document is exported without a running panel.
func (s *Server) services() *modules.Services { return s.opts.Services }

func (s *Server) store() store.Store           { return s.opts.Services.Store }
func (s *Server) pipeline() changeset.Pipeline { return s.opts.Services.Pipeline }
func (s *Server) jobs() jobs.Queue             { return s.opts.Services.Jobs }
func (s *Server) authorizer() auth.Authorizer  { return s.opts.Services.Authorizer }

// runChecks runs the health checks that apply, side by side, and reports
// them in their order.
func (s *Server) runChecks(ctx context.Context) []Check {
	var active []HealthCheck
	for _, hc := range s.opts.Health {
		if hc.Active == nil || hc.Active() {
			active = append(active, hc)
		}
	}
	checks := make([]Check, len(active))
	var wg sync.WaitGroup
	for i, hc := range active {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c := Check{Name: hc.Name, Status: "ok"}
			checkCtx, cancel := context.WithTimeout(ctx, healthTimeout)
			defer cancel()
			if err := hc.Check(checkCtx); err != nil {
				c.Status, c.Message = "failing", i18n.From(err).Message
			}
			checks[i] = c
		}()
	}
	wg.Wait()
	return checks
}

// authErrorKey carries the error of a request whose credentials weren't
// valid, for the operations that need them.
type authErrorKey struct{}

// requireActor refuses protected operations without a signed-in actor. A
// request whose credentials weren't valid gets the authenticator's error, so
// the person learns their session expired rather than that they never signed
// in.
func (s *Server) requireActor(ctx huma.Context, next func(huma.Context)) {
	if len(ctx.Operation().Security) == 0 {
		next(ctx)
		return
	}
	if s.noSignIn {
		err := ErrNoSignIn.New()
		huma.WriteErr(s.api, ctx, http.StatusServiceUnavailable, err.Message, err)
		return
	}
	if err, ok := ctx.Context().Value(authErrorKey{}).(error); ok {
		p := httpapi.FromError(err)
		huma.WriteErr(s.api, ctx, p.Status, p.Detail, err)
		return
	}
	if _, ok := auth.ActorFrom(ctx.Context()); !ok {
		huma.WriteErr(s.api, ctx, http.StatusUnauthorized, i18n.ErrUnauthenticated.New().Message, i18n.ErrUnauthenticated.New())
		return
	}
	next(ctx)
}

var requestIDPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{8,128}$`)

type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	n, err := r.ResponseWriter.Write(b)
	r.bytes += n
	return n, err
}

// Flush lets streaming responses such as Server-Sent Events through.
func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap gives http.ResponseController access to the original writer.
func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// Handler returns the server wrapped in its middleware: request IDs,
// recovery, access logs, panel.allow_from, authentication and rate limits.
func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		s.requests.Add(1)
		id := r.Header.Get("X-Request-Id")
		if !requestIDPattern.MatchString(id) {
			id = string(store.NewID(store.PrefixRequest))
		}
		w.Header().Set("X-Request-Id", id)
		rec := &statusRecorder{ResponseWriter: w}
		var actor *auth.Actor
		defer func() {
			if p := recover(); p != nil {
				s.opts.Log.Error("request panicked", "request", id, "method", r.Method, "path", s.loggedPath(r.URL.Path), "panic", p, "stack", string(debug.Stack()))
				if rec.status == 0 {
					writeProblem(rec, i18n.ErrInternal.New())
				}
			}
			level := slog.LevelInfo
			if r.URL.Path == Prefix+"/health" {
				level = slog.LevelDebug
			}
			s.opts.Log.Log(r.Context(), level, "request", "request", id, "method", r.Method, "path", s.loggedPath(r.URL.Path),
				"status", rec.status, "bytes", rec.bytes, "duration", time.Since(start).Round(time.Microsecond),
				"actor", actor.String(), "remote", r.RemoteAddr, "client", clientOf(r).IP)
		}()
		client := clientOf(r)
		isAPI := r.URL.Path == apiRoot || strings.HasPrefix(r.URL.Path, apiRoot+"/")
		setSecurityHeaders(rec.Header(), isAPI, client.Secure)
		if len(s.opts.AllowFrom) > 0 && !s.allowed(addrPort(client.IP)) {
			writeProblem(rec, ErrAddressNotAllowed.New("address", client.IP))
			return
		}
		if isAPI && r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
			if err := s.crossSite(r); err != nil {
				writeProblem(rec, err)
				return
			}
		}
		ctx := withAudit(events.WithRequestID(r.Context(), id), s.services().Audit, s.opts.Log)
		ctx = withOrigin(auth.WithClient(ctx, client), r, client)
		if strings.HasPrefix(r.URL.Path, providerPath+"/") {
			ctx = context.WithValue(ctx, queryKey{}, r.URL.Query())
		}
		ctx, reissued := identity.WithReissue(ctx)
		found, err := s.opts.Auth.Authenticate(ctx, r)
		if reissued.Issued != nil {
			// The session got a new secret because what its person may do
			// changed; the browser keeps the new one from this answer on.
			cookie := sessionCookie(reissued.Issued, client.Secure)
			http.SetCookie(rec, &cookie)
		}
		switch {
		case err != nil:
			// Credentials that aren't valid, such as an expired session
			// cookie, only matter to operations that need a signed-in actor.
			// Everything else, the UI and its sign-in screen included,
			// treats the request as anonymous.
			ctx = context.WithValue(ctx, authErrorKey{}, err)
		case found != nil:
			actor = found
			ctx = auth.WithActor(ctx, actor)
		}
		if isAPI {
			rec.Header().Set("Cache-Control", "no-store")
			// Load balancers ask for the health often; it is never limited.
			if r.URL.Path != Prefix+"/health" {
				// The headers describe the caller's bucket: how many requests
				// it holds when full, how many are left, and the seconds
				// until it is full again.
				ok, q, wait := s.limiter.allow(actor, addrPort(client.IP))
				if q.limit > 0 {
					rec.Header().Set("RateLimit-Limit", strconv.Itoa(q.limit))
					rec.Header().Set("RateLimit-Remaining", strconv.Itoa(q.remaining))
					rec.Header().Set("RateLimit-Reset", retryAfter(q.reset))
				}
				if !ok {
					after := retryAfter(wait)
					rec.Header().Set("Retry-After", after)
					writeProblem(rec, i18n.ErrRateLimited.New("retryAfter", after))
					return
				}
			}
		}
		s.mux.ServeHTTP(rec, r.WithContext(ctx))
	})
}

// allowed reports whether a client address is inside panel.allow_from.
func (s *Server) allowed(remote string) bool {
	ap, err := netip.ParseAddrPort(remote)
	if err != nil {
		return false
	}
	// A client on IPv4 reaches a dual-stack listener as an IPv4-mapped IPv6
	// address.
	addr := ap.Addr().Unmap()
	for _, p := range s.opts.AllowFrom {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

func writeProblem(w http.ResponseWriter, err error) {
	p := httpapi.FromError(err)
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(p.Status)
	if encErr := json.NewEncoder(w).Encode(p); encErr != nil {
		slog.Error("writing a problem response", "error", fmt.Sprint(encErr))
	}
}
