package agent

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/coreos/go-systemd/v22/daemon"
	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"

	"github.com/vilanobeachflorida/racksail/internal/api/httpapi"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/store"
	"github.com/vilanobeachflorida/racksail/internal/system"
)

// ProtocolVersion is the version of the agent protocol.
const ProtocolVersion = "1.1.0"

// maxBody bounds the requests and responses of operations. The largest are
// files written or read whole, whose contents travel base64-encoded in JSON,
// so it leaves room for the largest file in that encoding plus the rest of
// the request. Streams move what is larger and bound it themselves.
const maxBody = (maxFileContent+2)/3*4 + 1<<20

// bodyReadTimeout bounds reading one request. Panels on other servers reach
// the agent over mutual TLS, where a request close to maxBody takes longer
// than Huma's default of five seconds.
const bodyReadTimeout = 2 * time.Minute

// Server serves the operation allowlist over HTTP+JSON.
type Server struct {
	backend *system.Backend
	runtime *Runtime
	log     *slog.Logger
	version string
	mux     *http.ServeMux
	api     huma.API
}

// ServerOptions configure a Server.
type ServerOptions struct {
	Backend *system.Backend
	// Runtime is shared with in-process clients of the same machine; the
	// server makes its own when it's nil.
	Runtime *Runtime
	Version string
	Log     *slog.Logger
}

// NewServer builds the agent's HTTP API. backend may be nil when the server
// is only built to export its OpenAPI document.
func NewServer(backend *system.Backend, version string, log *slog.Logger) *Server {
	return NewServerWith(ServerOptions{Backend: backend, Version: version, Log: log})
}

// NewServerWith builds the agent's HTTP API from options.
func NewServerWith(opts ServerOptions) *Server {
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	if opts.Runtime == nil && opts.Backend != nil {
		opts.Runtime = NewRuntime(opts.Backend, RuntimeOptions{Log: opts.Log})
	}
	mux := http.NewServeMux()
	cfg := httpapi.Config("Racksail agent protocol", ProtocolVersion)
	cfg.Info.Description = "The typed operations the panel can ask the agent to run. " +
		"Every operation validates its input and is idempotent; with check=true it reports what it would change without changing anything. " +
		"Every call is recorded in the agent's audit trail. Streams move data too large for JSON, such as Btrfs send streams."
	s := &Server{backend: opts.Backend, runtime: opts.Runtime, log: opts.Log, version: opts.Version, mux: mux}
	s.api = humago.New(mux, cfg)
	huma.Register(s.api, huma.Operation{
		OperationID: "health",
		Method:      http.MethodGet,
		Path:        "/v1/health",
		Summary:     "Report that the agent is running",
		Tags:        []string{"core"},
	}, func(ctx context.Context, _ *struct{}) (*healthOutput, error) {
		h := &healthOutput{}
		h.Body.Status = "ok"
		h.Body.Version = s.version
		h.Body.Protocol = ProtocolVersion
		if s.backend != nil {
			h.Body.Backend = s.backend.Name
		}
		return h, nil
	})
	for _, e := range Operations() {
		e.register(s.api, s)
	}
	s.registerStreams()
	s.registerEvents()
	return s
}

// Runtime returns the server's runtime.
func (s *Server) Runtime() *Runtime { return s.runtime }

// Health is the agent's answer to a health check.
type Health struct {
	Status   string `json:"status" example:"ok"`
	Version  string `json:"version" doc:"Version of the racksail binary"`
	Protocol string `json:"protocol" doc:"Version of the agent protocol"`
	Backend  string `json:"backend" doc:"linux or sim"`
}

type healthOutput struct {
	Body Health
}

type callerKey struct{}

type auditedKey struct{}

// auditState notes whether a request reached an operation, which records
// it; requests refused before that are recorded by the middleware.
type auditState struct{ done bool }

func markAudited(ctx context.Context) {
	if st, ok := ctx.Value(auditedKey{}).(*auditState); ok {
		st.done = true
	}
}

func callerOf(ctx context.Context) string {
	if c, ok := ctx.Value(callerKey{}).(string); ok {
		return c
	}
	return CallerLocal
}

func (s *Server) env(ctx context.Context, check bool, owner store.ID) *Env {
	return &Env{System: s.backend, Check: check, Owner: owner, Log: s.log, Runtime: s.runtime, Caller: callerOf(ctx)}
}

// statusWriter remembers the status of a response.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// identify finds who is calling: the identity in a remote panel's
// certificate, or the local panel on the Unix socket. Connections an agent
// opened to the panel's hub carry the panel's identity in their context,
// verified when they were dialed.
func identify(r *http.Request) (string, error) {
	if c, ok := r.Context().Value(callerKey{}).(string); ok {
		return c, nil
	}
	if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
		return CallerLocal, nil
	}
	id, err := IdentityOf(r.TLS.PeerCertificates[0])
	if err != nil {
		return "", err
	}
	if id.Role != RolePanel {
		return "", fmt.Errorf("only the panel may call the agent, not %s", id)
	}
	return id.String(), nil
}

// presented names whoever a refused request came from, as far as its
// certificate tells.
func presented(r *http.Request) string {
	if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
		return "unknown"
	}
	if id, err := IdentityOf(r.TLS.PeerCertificates[0]); err == nil {
		return id.String()
	}
	return "an unrecognized certificate"
}

// requestedOp names what a request asked for, as the audit trail names
// operations and streams.
func requestedOp(p string) string {
	if op, ok := strings.CutPrefix(p, "/v1/ops/"); ok {
		return op
	}
	if stream, ok := strings.CutPrefix(p, "/v1/streams/"); ok {
		return "streams/" + stream
	}
	return p
}

// Handler returns the HTTP handler.
func (s *Server) Handler() http.Handler {
	capped := http.MaxBytesHandler(s.mux, maxBody)
	// Streams carry Btrfs send streams and uploads far larger than any
	// operation's JSON; each bounds what it reads itself.
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, pattern := s.mux.Handler(r); strings.HasPrefix(pattern, "POST /v1/streams/") {
			s.mux.ServeHTTP(w, r)
			return
		}
		capped.ServeHTTP(w, r)
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		caller, err := identify(r)
		if err != nil {
			if s.runtime != nil {
				s.runtime.refuse(requestedOp(r.URL.Path), presented(r), r.RemoteAddr, err)
			} else {
				s.log.Warn("refused a caller", "remote", r.RemoteAddr, "error", err)
			}
			writeProblem(w, ErrNotAllowed.New("op", r.URL.Path, "target", "this agent"))
			return
		}
		st := &auditState{}
		ctx := context.WithValue(context.WithValue(r.Context(), callerKey{}, caller), auditedKey{}, st)
		sw := &statusWriter{ResponseWriter: w}
		inner.ServeHTTP(sw, r.WithContext(ctx))
		// Operations and streams record themselves once they are reached; the
		// middleware records what was refused first, so every call is in the
		// trail: a malformed operation or stream request, and any path under
		// /v1 that no handler served.
		if !st.done && s.runtime != nil {
			if op := refusedCall(r.URL.Path, sw.status); op != "" {
				env := s.env(ctx, false, store.ID(r.Header.Get("Racksail-Owner")))
				env.audit(op, nil, []byte{}, nil, refusal(op, sw.status), time.Now())
			}
		}
	})
}

// refusedCall names a call the middleware should record because no operation
// or stream recorded it: an operation or stream request, or any other path
// under /v1 that was refused. Paths that answer normally without reaching an
// operation, such as the health check and the event stream, aren't calls to
// record.
func refusedCall(path string, status int) string {
	switch {
	case strings.HasPrefix(path, "/v1/ops/"), strings.HasPrefix(path, "/v1/streams/"):
		return requestedOp(path)
	case strings.HasPrefix(path, "/v1/") && status >= 400:
		return requestedOp(path)
	}
	return ""
}

// refusal describes a request the API refused before it reached an
// operation.
func refusal(op string, status int) error {
	switch status {
	case http.StatusNotFound:
		return ErrUnknownOperation.New("op", op)
	case http.StatusRequestEntityTooLarge:
		return i18n.ErrTooLarge.New()
	}
	return ErrInvalidInput.New("op", op, "reason", http.StatusText(status))
}

// OpenAPI returns the agent protocol's OpenAPI document.
func (s *Server) OpenAPI() ([]byte, error) {
	return json.MarshalIndent(s.api.OpenAPI(), "", "  ")
}

// Serve answers requests on ln until ctx is done. It tells systemd it is
// ready once it accepts connections.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	return s.serve(ctx, ln, s.httpServer())
}

// ServeTLS answers requests from remote panels on ln with mutual TLS: it
// presents cert and accepts only clients holding a panel certificate the
// CA issued. Peers refused during the handshake are recorded in the audit
// trail.
func (s *Server) ServeTLS(ctx context.Context, ln net.Listener, cfg *tls.Config) error {
	srv := s.httpServer()
	srv.TLSConfig = cfg
	refuse := func(remote, peer string, err error) {
		if s.runtime != nil {
			s.runtime.refuse("connect", peer, remote, err)
		}
	}
	return s.serve(ctx, newHandshakeListener(ln, cfg, refuse), srv)
}

// handshakeListener completes the TLS handshake of each connection before
// handing it to the HTTP server, so a peer refused there can be recorded:
// the HTTP server would only log it.
type handshakeListener struct {
	net.Listener
	cfg    *tls.Config
	refuse func(remote, peer string, err error)
	conns  chan net.Conn
	errs   chan error
	closed chan struct{}
	once   sync.Once
	// slots bounds the handshakes in progress.
	slots chan struct{}
}

func newHandshakeListener(ln net.Listener, cfg *tls.Config, refuse func(remote, peer string, err error)) *handshakeListener {
	l := &handshakeListener{
		Listener: ln, cfg: cfg, refuse: refuse,
		conns: make(chan net.Conn), errs: make(chan error, 1), closed: make(chan struct{}), slots: make(chan struct{}, 64),
	}
	go l.accept()
	return l
}

func (l *handshakeListener) accept() {
	backoff := 5 * time.Millisecond
	for {
		raw, err := l.Listener.Accept()
		if err != nil {
			// Running out of descriptors or a connection reset before it was
			// accepted passes; anything else ends the listener.
			if errors.Is(err, syscall.EMFILE) || errors.Is(err, syscall.ENFILE) || errors.Is(err, syscall.ECONNABORTED) || errors.Is(err, syscall.ECONNRESET) {
				select {
				case <-time.After(backoff):
				case <-l.closed:
					return
				}
				backoff = min(backoff*2, time.Second)
				continue
			}
			select {
			case l.errs <- err:
			case <-l.closed:
			}
			return
		}
		backoff = 5 * time.Millisecond
		select {
		case l.slots <- struct{}{}:
		case <-l.closed:
			raw.Close()
			return
		}
		go l.handshake(raw)
	}
}

func (l *handshakeListener) handshake(raw net.Conn) {
	defer func() { <-l.slots }()
	conn := tls.Server(raw, l.cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := conn.HandshakeContext(ctx); err != nil {
		peer := "unknown"
		if certs := conn.ConnectionState().PeerCertificates; len(certs) > 0 {
			peer = "an unrecognized certificate"
			if id, err := IdentityOf(certs[0]); err == nil {
				peer = id.String()
			}
		}
		l.refuse(raw.RemoteAddr().String(), peer, err)
		conn.Close()
		return
	}
	select {
	case l.conns <- conn:
	case <-l.closed:
		conn.Close()
	}
}

// Accept implements net.Listener.
func (l *handshakeListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case err := <-l.errs:
		return nil, err
	case <-l.closed:
		return nil, net.ErrClosed
	}
}

// Close implements net.Listener.
func (l *handshakeListener) Close() error {
	l.once.Do(func() { close(l.closed) })
	return l.Listener.Close()
}

func (s *Server) httpServer() *http.Server {
	return &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		ErrorLog:          slog.NewLogLogger(s.log.Handler(), slog.LevelWarn),
	}
}

func (s *Server) serve(ctx context.Context, ln net.Listener, srv *http.Server) error {
	if err := Err(); err != nil {
		return err
	}
	if err := s.backend.Check(); err != nil {
		return err
	}
	if s.runtime != nil {
		if err := s.runtime.Start(ctx); err != nil {
			return err
		}
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdown); err != nil {
			s.log.Warn("agent shutdown", "error", err)
		}
	}()
	// Under systemd the unit is of type notify; anywhere else this does
	// nothing.
	if _, err := daemon.SdNotify(false, daemon.SdNotifyReady); err != nil {
		s.log.Warn("telling systemd the agent is ready", "error", err)
	}
	err := srv.Serve(ln)
	if errors.Is(err, http.ErrServerClosed) {
		<-done
		return nil
	}
	return err
}

// ListenUnix listens on a Unix socket, replacing a stale socket file, and
// sets its permissions so only the given group can connect.
func ListenUnix(path string, perm fs.FileMode, gid int) (net.Listener, error) {
	if fi, err := os.Lstat(path); err == nil && fi.Mode()&fs.ModeSocket != 0 {
		if err := os.Remove(path); err != nil {
			return nil, fmt.Errorf("removing stale socket %s: %w", path, err)
		}
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, perm); err != nil {
		ln.Close()
		return nil, err
	}
	if gid >= 0 {
		if err := os.Chown(path, -1, gid); err != nil {
			ln.Close()
			return nil, err
		}
	}
	return ln, nil
}

// writeProblem answers with an error as problem details, for handlers
// outside Huma.
func writeProblem(w http.ResponseWriter, err error) {
	p := httpapi.FromError(err)
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(p.Status)
	json.NewEncoder(w).Encode(p)
}

// registerEvents serves the runtime's events as a stream of JSON lines,
// so the panel learns about drift and network rollbacks as they happen.
func (s *Server) registerEvents() {
	s.api.OpenAPI().AddOperation(&huma.Operation{
		OperationID: "events",
		Method:      http.MethodGet,
		Path:        "/v1/events",
		Summary:     "Stream events",
		Description: "Streams what the agent notices on its own, one JSON object per line: drift from what Racksail put on the machine, " +
			"once per change, starting with the drift the last check found, and network changes confirmed or rolled back.",
		Tags: []string{"core"},
		Responses: map[string]*huma.Response{"200": {
			Description: "Events as JSON lines",
			Content:     map[string]*huma.MediaType{"application/jsonl": {Schema: s.api.OpenAPI().Components.Schemas.Schema(reflect.TypeFor[Event](), true, "")}},
		}},
	})
	s.mux.HandleFunc("GET /v1/events", func(w http.ResponseWriter, r *http.Request) {
		if s.runtime == nil {
			http.Error(w, "no runtime", http.StatusServiceUnavailable)
			return
		}
		events, cancel := s.runtime.Subscribe()
		defer cancel()
		w.Header().Set("Content-Type", "application/jsonl")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		rc := http.NewResponseController(w)
		enc := json.NewEncoder(w)
		// A panel that connects late still learns about drift found before.
		for _, d := range s.runtime.CurrentDrift() {
			if err := enc.Encode(Event{Type: EventDrift, Time: time.Now().UTC(), Drift: &d}); err != nil {
				return
			}
		}
		rc.Flush()
		keepalive := time.NewTicker(30 * time.Second)
		defer keepalive.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case <-keepalive.C:
				if _, err := w.Write([]byte("\n")); err != nil {
					return
				}
			case e, ok := <-events:
				if !ok {
					return
				}
				if err := enc.Encode(e); err != nil {
					return
				}
			}
			rc.Flush()
		}
	})
}
