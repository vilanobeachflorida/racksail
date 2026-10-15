package edge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/store"
)

// The panel talks to the edge over the edge's admin socket, which only the
// panel's group can reach:
//
//	GET    /health                         the edge's state
//	GET    /routes                         the routing table
//	PUT    /routes/{owner}                 replace an owner's routes
//	DELETE /routes/{owner}                 remove an owner's routes
//	GET    /settings, PUT /settings        ACME and protocol settings
//	GET    /certificates                   certificates and their expiry
//	GET    /certificates/{subject}/export  an issued certificate and key
//	POST   /certificates/{subject}/renew   renew a certificate now
//	GET    /activity                       requests per route
//	GET    /questions?wait=25s             questions for the panel
//	POST   /answers                        the panel's answers

// maxAdminBody bounds requests to the admin API; a large routing table for
// one owner still fits.
const maxAdminBody = 8 << 20

// AdminHandler returns the admin API.
func (e *Edge) AdminHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, e.Health())
	})
	mux.HandleFunc("GET /routes", func(w http.ResponseWriter, r *http.Request) {
		routes, _ := e.table.Routes(r.Context())
		if routes == nil {
			routes = []Route{}
		}
		writeJSON(w, http.StatusOK, routes)
	})
	mux.HandleFunc("PUT /routes/{owner}", func(w http.ResponseWriter, r *http.Request) {
		var routes []Route
		if !decodeJSON(w, r, &routes) {
			return
		}
		if err := e.table.SetRoutes(r.Context(), store.ID(r.PathValue("owner")), routes); err != nil {
			writeError(w, http.StatusUnprocessableEntity, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("DELETE /routes/{owner}", func(w http.ResponseWriter, r *http.Request) {
		if err := e.table.DeleteRoutes(r.Context(), store.ID(r.PathValue("owner"))); err != nil {
			writeError(w, http.StatusUnprocessableEntity, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /settings", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, e.Settings())
	})
	mux.HandleFunc("PUT /settings", func(w http.ResponseWriter, r *http.Request) {
		var s Settings
		if !decodeJSON(w, r, &s) {
			return
		}
		if err := e.SetSettings(s); err != nil {
			writeError(w, http.StatusUnprocessableEntity, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /certificates", func(w http.ResponseWriter, r *http.Request) {
		certs := e.Certificates(r.Context())
		if certs == nil {
			certs = []Certificate{}
		}
		writeJSON(w, http.StatusOK, certs)
	})
	mux.HandleFunc("GET /certificates/{subject}/export", func(w http.ResponseWriter, r *http.Request) {
		certPEM, keyPEM, err := e.ExportCertificate(r.Context(), r.PathValue("subject"))
		if err != nil {
			writeError(w, statusFor(err), err)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, ExportedCertificate{Certificate: string(certPEM), Key: string(keyPEM)})
	})
	mux.HandleFunc("POST /certificates/{subject}/renew", func(w http.ResponseWriter, r *http.Request) {
		if err := e.RenewCertificate(r.PathValue("subject")); err != nil {
			writeError(w, statusFor(err), err)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	})
	mux.HandleFunc("GET /activity", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, e.Activity())
	})
	mux.HandleFunc("GET /questions", func(w http.ResponseWriter, r *http.Request) {
		wait := 25 * time.Second
		if v := r.URL.Query().Get("wait"); v != "" {
			d, err := time.ParseDuration(v)
			if err != nil || d < 0 || d > time.Minute {
				writeError(w, http.StatusBadRequest, fmt.Errorf("wait %q must be a duration up to a minute", v))
				return
			}
			wait = d
		}
		questions := e.questions.poll(r.Context(), wait)
		if questions == nil {
			questions = []Question{}
		}
		writeJSON(w, http.StatusOK, questions)
	})
	mux.HandleFunc("POST /answers", func(w http.ResponseWriter, r *http.Request) {
		var answers []Answer
		if !decodeJSON(w, r, &answers) {
			return
		}
		e.questions.answer(answers)
		w.WriteHeader(http.StatusNoContent)
	})
	return mux
}

// ExportedCertificate is an issued certificate and its key.
type ExportedCertificate struct {
	Certificate string `json:"certificate" doc:"PEM chain, leaf first"`
	Key         string `json:"key" doc:"PEM private key"`
}

func statusFor(err error) int {
	if errors.Is(err, fs.ErrNotExist) {
		return http.StatusNotFound
	}
	return http.StatusConflict
}

func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxAdminBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]string{"error": err.Error()})
}

// AdminClient is the panel's Router for an edge in another process, and
// its way to the rest of the admin API.
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
		MaxIdleConns:    4,
		IdleConnTimeout: 90 * time.Second,
	}}}
}

// AdminError is an error the edge answered with.
type AdminError struct {
	Status  int
	Message string
}

func (e *AdminError) Error() string { return "edge: " + http.StatusText(e.Status) + ": " + e.Message }

// Is lets errors.Is match fs.ErrNotExist for answers that found nothing.
func (e *AdminError) Is(target error) bool {
	return target == fs.ErrNotExist && e.Status == http.StatusNotFound
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
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
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
		json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&e)
		return &AdminError{Status: resp.StatusCode, Message: e.Error}
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

// Health reports the edge's state.
func (c *AdminClient) Health(ctx context.Context) (Health, error) {
	var h Health
	err := c.do(ctx, http.MethodGet, "/health", nil, &h)
	return h, err
}

// Settings returns the edge's settings.
func (c *AdminClient) Settings(ctx context.Context) (Settings, error) {
	var s Settings
	err := c.do(ctx, http.MethodGet, "/settings", nil, &s)
	return s, err
}

// SetSettings changes the edge's settings.
func (c *AdminClient) SetSettings(ctx context.Context, s Settings) error {
	return c.do(ctx, http.MethodPut, "/settings", s, nil)
}

// Certificates lists the certificates the edge serves.
func (c *AdminClient) Certificates(ctx context.Context) ([]Certificate, error) {
	var certs []Certificate
	err := c.do(ctx, http.MethodGet, "/certificates", nil, &certs)
	return certs, err
}

// ExportCertificate returns an issued certificate and its key.
func (c *AdminClient) ExportCertificate(ctx context.Context, subject string) (ExportedCertificate, error) {
	var out ExportedCertificate
	err := c.do(ctx, http.MethodGet, "/certificates/"+url.PathEscape(subject)+"/export", nil, &out)
	return out, err
}

// RenewCertificate starts renewing a certificate.
func (c *AdminClient) RenewCertificate(ctx context.Context, subject string) error {
	return c.do(ctx, http.MethodPost, "/certificates/"+url.PathEscape(subject)+"/renew", nil, nil)
}

// Activity returns how much each route was used.
func (c *AdminClient) Activity(ctx context.Context) ([]RouteActivity, error) {
	var out []RouteActivity
	err := c.do(ctx, http.MethodGet, "/activity", nil, &out)
	return out, err
}

// Questions waits up to wait for the edge's questions.
func (c *AdminClient) Questions(ctx context.Context, wait time.Duration) ([]Question, error) {
	var out []Question
	err := c.do(ctx, http.MethodGet, "/questions?wait="+url.QueryEscape(wait.String()), nil, &out)
	return out, err
}

// Answer delivers answers to the edge's questions.
func (c *AdminClient) Answer(ctx context.Context, answers []Answer) error {
	return c.do(ctx, http.MethodPost, "/answers", answers, nil)
}
