package edge

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/store"
)

// CertSource provides installed certificates by name.
type CertSource interface {
	Certificate(name string) (*tls.Certificate, error)
}

// FileCerts loads certificates from <dir>/<name>/fullchain.pem and
// privkey.pem, as the agent installs them.
type FileCerts struct {
	Dir string
}

// Certificate implements CertSource.
func (f FileCerts) Certificate(name string) (*tls.Certificate, error) {
	if strings.ContainsAny(name, "/\\") || name == "" || name == "." || name == ".." {
		return nil, fmt.Errorf("certificate name %q", name)
	}
	cert, err := tls.LoadX509KeyPair(filepath.Join(f.Dir, name, "fullchain.pem"), filepath.Join(f.Dir, name, "privkey.pem"))
	if err != nil {
		return nil, err
	}
	return &cert, nil
}

// Server answers requests from a routing table.
type Server struct {
	Table *Table
	Certs CertSource
	Log   *slog.Logger

	counters sync.Map
	unixMu   sync.Mutex
	unix     map[string]*http.Transport
}

// NewServer returns a server for table.
func NewServer(table *Table, certs CertSource, log *slog.Logger) *Server {
	if log == nil {
		log = slog.Default()
	}
	return &Server{Table: table, Certs: certs, Log: log, unix: map[string]*http.Transport{}}
}

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	route, ok := s.Table.Match(r.Host, r.URL.Path)
	if !ok {
		page(w, http.StatusNotFound, "No site here", "This address isn't set up on this server yet.")
		return
	}
	for _, h := range route.Headers {
		w.Header().Set(h.Name, h.Value)
	}
	switch route.Handler.Type {
	case HandleStatic:
		s.serveStatic(w, r, route.Handler.Root)
	case HandleProxy:
		s.serveProxy(w, r, route)
	case HandleFastCGI:
		s.serveFastCGI(w, r, route)
	case HandleRedirect:
		target := strings.ReplaceAll(route.Handler.To, "{uri}", r.URL.RequestURI())
		code := route.Handler.Code
		if code == 0 {
			code = http.StatusPermanentRedirect
		}
		http.Redirect(w, r, target, code)
	case HandleRespond:
		code := route.Handler.Code
		if code == 0 {
			code = http.StatusOK
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(code)
		io.WriteString(w, route.Handler.Body)
	}
}

func page(w http.ResponseWriter, code int, title, message string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	fmt.Fprintf(w, "<!doctype html><html lang=\"en\"><meta charset=\"utf-8\"><meta name=\"viewport\" content=\"width=device-width\"><title>%s</title>"+
		"<body style=\"font-family:system-ui,sans-serif;margin:4rem auto;max-width:32rem;padding:0 1rem\"><h1>%s</h1><p>%s</p></body></html>",
		html.EscapeString(title), html.EscapeString(title), html.EscapeString(message))
}

// serveStatic serves files under root. The root is opened with os.Root, so
// symbolic links can't reach outside it, and directories without an
// index.html aren't listed.
func (s *Server) serveStatic(w http.ResponseWriter, r *http.Request, root string) {
	dir, err := os.OpenRoot(root)
	if err != nil {
		page(w, http.StatusNotFound, "Not found", "The site's files aren't there.")
		return
	}
	defer dir.Close()
	http.FileServerFS(noListing{dir.FS()}).ServeHTTP(w, r)
}

// noListing refuses to open directories that have no index.html.
type noListing struct{ fs.FS }

func (n noListing) Open(name string) (fs.File, error) {
	f, err := n.FS.Open(name)
	if err != nil {
		return nil, err
	}
	if fi, err := f.Stat(); err == nil && fi.IsDir() {
		if _, err := fs.Stat(n.FS, path.Join(name, "index.html")); err != nil {
			f.Close()
			return nil, fs.ErrNotExist
		}
	}
	return f, nil
}

// pick chooses an upstream in weighted round-robin order.
func (s *Server) pick(route Route) Upstream {
	ups := route.Handler.Upstreams
	total := 0
	for _, u := range ups {
		total += max(u.Weight, 1)
	}
	key := string(route.Owner) + "/" + route.ID
	v, _ := s.counters.LoadOrStore(key, new(atomic.Uint64))
	n := int(v.(*atomic.Uint64).Add(1) % uint64(total))
	for _, u := range ups {
		n -= max(u.Weight, 1)
		if n < 0 {
			return u
		}
	}
	return ups[0]
}

func (s *Server) transport(up Upstream) http.RoundTripper {
	if up.Network != "unix" {
		return http.DefaultTransport
	}
	s.unixMu.Lock()
	defer s.unixMu.Unlock()
	if t, ok := s.unix[up.Address]; ok {
		return t
	}
	socket := up.Address
	t := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socket)
		},
		MaxIdleConnsPerHost: 16,
		IdleConnTimeout:     90 * time.Second,
	}
	s.unix[up.Address] = t
	return t
}

func (s *Server) serveProxy(w http.ResponseWriter, r *http.Request, route Route) {
	up := s.pick(route)
	target := &url.URL{Scheme: "http", Host: up.Address}
	if up.Network == "unix" {
		target.Host = "unix"
	}
	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.Host = r.Host
			pr.SetXForwarded()
		},
		Transport: s.transport(up),
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			s.badGateway(w, route, err)
		},
	}
	proxy.ServeHTTP(w, r)
}

func (s *Server) badGateway(w http.ResponseWriter, route Route, err error) {
	s.Log.Warn("upstream failed", "route", route.ID, "owner", route.Owner, "error", err)
	page(w, http.StatusBadGateway, "The site isn't answering", "The site's application didn't respond. Try again in a moment.")
}

// TLSConfig serves routes with installed certificates. ACME certificates
// come with the embedded Caddy runtime.
func (s *Server) TLSConfig() *tls.Config {
	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		GetCertificate: func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
			route, ok := s.Table.Match(hello.ServerName, "/")
			if !ok || route.TLS.Mode != TLSCertificate || s.Certs == nil {
				return nil, fmt.Errorf("no certificate for %q", hello.ServerName)
			}
			return s.Certs.Certificate(route.TLS.Certificate)
		},
	}
}

// NewAdmin returns the handler the panel talks to over the edge's socket:
// GET /routes, PUT and DELETE /routes/{owner} and GET /health.
func NewAdmin(table *Table) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "routes": table.Len(), "version": table.Version()})
	})
	mux.HandleFunc("GET /routes", func(w http.ResponseWriter, r *http.Request) {
		routes, _ := table.Routes(r.Context())
		writeJSON(w, http.StatusOK, routes)
	})
	mux.HandleFunc("PUT /routes/{owner}", func(w http.ResponseWriter, r *http.Request) {
		var routes []Route
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<20))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&routes); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		if err := table.SetRoutes(r.Context(), store.ID(r.PathValue("owner")), routes); err != nil {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("DELETE /routes/{owner}", func(w http.ResponseWriter, r *http.Request) {
		if err := table.DeleteRoutes(r.Context(), store.ID(r.PathValue("owner"))); err != nil {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	return mux
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

// AdminClient is the panel's Router for an edge process.
type AdminClient struct {
	http *http.Client
	base string
}

// DialAdmin returns a client for the edge's socket.
func DialAdmin(socket string) *AdminClient {
	return &AdminClient{base: "http://edge", http: &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socket)
		},
	}}}
}

func (c *AdminClient) do(ctx context.Context, method, path string, body any, out any) error {
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rd)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("edge: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		var e struct {
			Error string `json:"error"`
		}
		json.NewDecoder(resp.Body).Decode(&e)
		return fmt.Errorf("edge: %s: %s", resp.Status, e.Error)
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

// SetRoutes implements Router.
func (c *AdminClient) SetRoutes(ctx context.Context, owner store.ID, routes []Route) error {
	if routes == nil {
		routes = []Route{}
	}
	return c.do(ctx, http.MethodPut, "/routes/"+url.PathEscape(string(owner)), routes, nil)
}

// DeleteRoutes implements Router.
func (c *AdminClient) DeleteRoutes(ctx context.Context, owner store.ID) error {
	return c.do(ctx, http.MethodDelete, "/routes/"+url.PathEscape(string(owner)), nil, nil)
}

// Routes implements Router.
func (c *AdminClient) Routes(ctx context.Context) ([]Route, error) {
	var routes []Route
	err := c.do(ctx, http.MethodGet, "/routes", nil, &routes)
	return routes, err
}

// Run serves the admin socket at once and the HTTP and HTTPS listeners from
// the moment the table has its first route, until ctx is done. admin may be
// nil when the table is changed in the same process.
func (s *Server) Run(ctx context.Context, admin net.Listener, httpAddr, httpsAddr string) error {
	adminSrv := &http.Server{Handler: NewAdmin(s.Table), ReadHeaderTimeout: 10 * time.Second}
	errs := make(chan error, 3)
	if admin != nil {
		go func() { errs <- serve(adminSrv, admin) }()
	}
	var servers []*http.Server
	defer func() {
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		adminSrv.Shutdown(shutdown)
		for _, srv := range servers {
			srv.Shutdown(shutdown)
		}
	}()
	for s.Table.Len() == 0 {
		select {
		case <-ctx.Done():
			return nil
		case err := <-errs:
			return err
		case <-s.Table.Changed():
		}
	}
	plainLn, err := net.Listen("tcp", httpAddr)
	if err != nil {
		return fmt.Errorf("edge: %w", err)
	}
	secureLn, err := net.Listen("tcp", httpsAddr)
	if err != nil {
		plainLn.Close()
		return fmt.Errorf("edge: %w", err)
	}
	// The listeners' own addresses, so a port 0 shows the port it got.
	s.Log.Info("edge listening", "http", plainLn.Addr().String(), "https", secureLn.Addr().String(), "routes", s.Table.Len())
	plain := &http.Server{Handler: s, ReadHeaderTimeout: 10 * time.Second}
	secure := &http.Server{Handler: s, ReadHeaderTimeout: 10 * time.Second, TLSConfig: s.TLSConfig()}
	servers = append(servers, plain, secure)
	go func() { errs <- serve(plain, plainLn) }()
	go func() { errs <- serveTLS(secure, secureLn) }()
	select {
	case <-ctx.Done():
		return nil
	case err := <-errs:
		return err
	}
}

func serve(srv *http.Server, ln net.Listener) error {
	if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// serveTLS serves HTTPS with the certificates of srv.TLSConfig. Unlike a TLS
// listener passed to Serve, it also offers HTTP/2.
func serveTLS(srv *http.Server, ln net.Listener) error {
	if err := srv.ServeTLS(ln, "", ""); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
