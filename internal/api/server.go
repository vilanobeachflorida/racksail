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
	"strings"
	"sync"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"

	"github.com/vilanobeachflorida/racksail/internal/api/httpapi"
	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
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
const SessionCookie = "racksail_session"

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
	Auth    auth.Authenticator
	Modules *modules.Registry
	// Services are passed to modules that add API operations.
	Services *modules.Services
	Health   []HealthCheck
	// AllowFrom limits who may reach the panel to these networks when it
	// isn't empty: panel.allow_from in racksail.toml. Requests from anywhere
	// else are refused before anything else happens.
	AllowFrom []netip.Prefix
	// UI serves everything outside /api.
	UI  http.Handler
	Log *slog.Logger
}

// ErrAddressNotAllowed refuses requests from outside panel.allow_from.
var ErrAddressNotAllowed = i18n.Define("api.address_not_allowed", http.StatusForbidden, "The panel doesn't accept requests from {address}.")

// Server is the panel API.
type Server struct {
	opts Options
	mux  *http.ServeMux
	api  huma.API
}

// New builds the server.
func New(opts Options) *Server {
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	if opts.Auth == nil {
		opts.Auth = auth.NoAuthenticator{}
	}
	mux := http.NewServeMux()
	cfg := httpapi.Config("Racksail API", Version)
	cfg.Info.Description = "The API of the Racksail panel. Every change goes through the change pipeline: " +
		"mutating calls accept dryRun=true to preview the change set without applying it, and an Idempotency-Key header. " +
		"Errors are RFC 9457 problem details with a stable code."
	cfg.Info.License = &huma.License{Name: "AGPL-3.0-or-later", Identifier: "AGPL-3.0-or-later"}
	cfg.Components.SecuritySchemes = map[string]*huma.SecurityScheme{
		SessionScheme: {Type: "apiKey", In: "cookie", Name: SessionCookie, Description: "The session cookie set when signing in to the panel."},
		TokenScheme:   {Type: "http", Scheme: "bearer", Description: "An API token, scoped to permissions and accounts."},
	}
	s := &Server{opts: opts, mux: mux, api: humago.New(mux, cfg)}
	s.api.UseMiddleware(s.requireActor)
	s.registerSystem()
	if opts.Modules != nil {
		for _, m := range opts.Modules.All() {
			if p, ok := m.(modules.APIProvider); ok {
				p.RegisterAPI(s.api, opts.Services)
			}
		}
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
	huma.Register(s.api, huma.Operation{
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
	}, func(ctx context.Context, _ *struct{}) (*healthOutput, error) {
		out := &healthOutput{Status: http.StatusOK, Body: HealthReport{Status: "ok", Checks: s.runChecks(ctx)}}
		for _, c := range out.Body.Checks {
			if c.Status != "ok" {
				out.Status, out.Body.Status = http.StatusServiceUnavailable, "degraded"
			}
		}
		return out, nil
	})
	huma.Register(s.api, huma.Operation{
		OperationID: "getVersion",
		Method:      http.MethodGet,
		Path:        Prefix + "/version",
		Summary:     "Show the Racksail version",
		Description: "Reports the version, release channel and build of the running panel. It needs no sign-in.",
		Tags:        []string{"System"},
	}, func(ctx context.Context, _ *struct{}) (*versionOutput, error) {
		return &versionOutput{Body: updates.Build()}, nil
	})
}

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

// Handler returns the server wrapped in its middleware.
func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		id := r.Header.Get("X-Request-Id")
		if !requestIDPattern.MatchString(id) {
			id = string(store.NewID(store.PrefixRequest))
		}
		w.Header().Set("X-Request-Id", id)
		rec := &statusRecorder{ResponseWriter: w}
		defer func() {
			if p := recover(); p != nil {
				s.opts.Log.Error("request panicked", "request", id, "method", r.Method, "path", r.URL.Path, "panic", p, "stack", string(debug.Stack()))
				if rec.status == 0 {
					writeProblem(rec, i18n.ErrInternal.New())
				}
			}
			level := slog.LevelInfo
			if r.URL.Path == Prefix+"/health" {
				level = slog.LevelDebug
			}
			s.opts.Log.Log(r.Context(), level, "request", "request", id, "method", r.Method, "path", r.URL.Path,
				"status", rec.status, "bytes", rec.bytes, "duration", time.Since(start).Round(time.Microsecond))
		}()
		if len(s.opts.AllowFrom) > 0 && !s.allowed(r.RemoteAddr) {
			writeProblem(rec, ErrAddressNotAllowed.New("address", r.RemoteAddr))
			return
		}
		ctx := r.Context()
		actor, err := s.opts.Auth.Authenticate(ctx, r)
		switch {
		case err != nil:
			// Credentials that aren't valid, such as an expired session
			// cookie, only matter to operations that need a signed-in actor.
			// Everything else, the UI and its sign-in screen included,
			// treats the request as anonymous.
			ctx = context.WithValue(ctx, authErrorKey{}, err)
		case actor != nil:
			ctx = auth.WithActor(ctx, actor)
		}
		if r.URL.Path == apiRoot || strings.HasPrefix(r.URL.Path, apiRoot+"/") {
			rec.Header().Set("Cache-Control", "no-store")
			rec.Header().Set("X-Content-Type-Options", "nosniff")
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
