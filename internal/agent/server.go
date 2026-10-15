package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"

	"github.com/vilanobeachflorida/racksail/internal/api/httpapi"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/store"
	"github.com/vilanobeachflorida/racksail/internal/system"
)

// ProtocolVersion is the version of the agent protocol.
const ProtocolVersion = "1.0.0"

// maxBody bounds requests and responses on the agent socket. The largest are
// files written or read whole, whose contents travel base64-encoded in JSON,
// so it leaves room for the largest file in that encoding plus the rest of
// the request.
const maxBody = (maxFileContent+2)/3*4 + 1<<20

// bodyReadTimeout bounds reading one request. Panels on other servers reach
// the agent over mutual TLS, where a request close to maxBody takes longer
// than Huma's default of five seconds.
const bodyReadTimeout = 2 * time.Minute

// Server serves the operation allowlist over HTTP+JSON.
type Server struct {
	backend *system.Backend
	log     *slog.Logger
	version string
	mux     *http.ServeMux
	api     huma.API
}

// NewServer builds the agent's HTTP API. backend may be nil when the server
// is only built to export its OpenAPI document.
func NewServer(backend *system.Backend, version string, log *slog.Logger) *Server {
	if log == nil {
		log = slog.Default()
	}
	mux := http.NewServeMux()
	cfg := httpapi.Config("Racksail agent protocol", ProtocolVersion)
	cfg.Info.Description = "The typed operations the panel can ask the agent to run. " +
		"Every operation validates its input and is idempotent; with check=true it reports what it would change without changing anything."
	s := &Server{backend: backend, log: log, version: version, mux: mux}
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
	return s
}

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

func (s *Server) env(check bool, owner store.ID) *Env {
	return &Env{System: s.backend, Check: check, Owner: owner, Log: s.log}
}

// Handler returns the HTTP handler.
func (s *Server) Handler() http.Handler { return http.MaxBytesHandler(s.mux, maxBody) }

// OpenAPI returns the agent protocol's OpenAPI document.
func (s *Server) OpenAPI() ([]byte, error) {
	return json.MarshalIndent(s.api.OpenAPI(), "", "  ")
}

// Serve answers requests on ln until ctx is done.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	if err := Err(); err != nil {
		return err
	}
	if err := s.backend.Check(); err != nil {
		return err
	}
	srv := &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		ErrorLog:          slog.NewLogLogger(s.log.Handler(), slog.LevelWarn),
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

// HTTPClient calls an agent over HTTP: its Unix socket on the same machine,
// or mutual TLS between servers.
type HTTPClient struct {
	base string
	http *http.Client
}

// DialUnix returns a client for the agent listening on a Unix socket.
func DialUnix(socket string) *HTTPClient {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socket)
		},
		MaxIdleConns:    4,
		IdleConnTimeout: 90 * time.Second,
	}
	return &HTTPClient{base: "http://agent", http: &http.Client{Transport: transport}}
}

// NewHTTPClient returns a client for an agent at baseURL using hc, which
// carries the TLS client certificate for remote servers.
func NewHTTPClient(baseURL string, hc *http.Client) *HTTPClient {
	if hc == nil {
		hc = http.DefaultClient
	}
	return &HTTPClient{base: baseURL, http: hc}
}

// Call implements Client.
func (c *HTTPClient) Call(ctx context.Context, op string, input json.RawMessage, opts CallOptions) (json.RawMessage, error) {
	target := c.base + "/v1/ops/" + url.PathEscape(op)
	if opts.Check {
		target += "?check=true"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(input))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if opts.Owner != "" {
		req.Header.Set("Racksail-Owner", string(opts.Owner))
	}
	return c.do(req)
}

// Health asks the agent whether it is running.
func (c *HTTPClient) Health(ctx context.Context) (Health, error) {
	var h Health
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/v1/health", nil)
	if err != nil {
		return h, err
	}
	body, err := c.do(req)
	if err != nil {
		return h, err
	}
	err = json.Unmarshal(body, &h)
	return h, err
}

func (c *HTTPClient) do(req *http.Request) (json.RawMessage, error) {
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, ErrUnreachable.Wrap(err, "reason", err.Error())
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return nil, ErrUnreachable.Wrap(err, "reason", err.Error())
	}
	if len(body) > maxBody {
		return nil, ErrFailed.New("op", req.URL.Path, "reason", fmt.Sprintf("the answer is larger than %d bytes", maxBody))
	}
	if resp.StatusCode < 300 {
		return body, nil
	}
	var p httpapi.Problem
	if json.Unmarshal(body, &p) != nil || p.Code == "" {
		return nil, ErrFailed.New("op", req.URL.Path, "reason", resp.Status)
	}
	e := &i18n.Error{Code: p.Code, Status: p.Status, Message: p.Detail, Params: p.Params}
	for _, f := range p.Errors {
		e.Fields = append(e.Fields, i18n.FieldError{Path: f.Location, Code: f.Code, Message: f.Message, Params: f.Params})
	}
	return nil, e
}
