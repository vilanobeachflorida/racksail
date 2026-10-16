// Package serve answers the panel on an add-on's socket, as the add-on
// protocol in spec/addon-protocol.json describes: the add-on's health, and
// the methods of the capabilities it provides, at
// POST /provide/<capability>/<method>.
//
// The panel sends each call the provider's settings, as the admin saved
// them with its secret filled in, and what the method takes. It never
// sends anything else, so an add-on keeps no settings of its own.
package serve

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"
)

// APIVersion is the hook API version the add-ons speak.
const APIVersion = "v1"

// maxCall bounds what the panel sends one call.
const maxCall = 1 << 20

// Method is one method of a capability: it gets the provider's settings and
// what the method takes, and returns what it answers with.
type Method func(ctx context.Context, settings, input json.RawMessage) (any, error)

// Addon is what an add-on serves.
type Addon struct {
	Name    string
	Version string
	// Capabilities are the methods of each capability the add-on provides,
	// by capability and method, such as auth-provider and check-password.
	Capabilities map[string]map[string]Method
	Log          *slog.Logger
}

// call is what the panel sends: what the method takes, with the provider's
// settings next to it.
type call struct {
	Input struct {
		Settings json.RawMessage `json:"settings"`
		Input    json.RawMessage `json:"input"`
	} `json:"input"`
}

// Failure is how a method says what went wrong, for the panel to show.
type Failure struct {
	Status int
	Detail string
}

func (f *Failure) Error() string { return f.Detail }

// Failed returns a failure the panel shows as the add-on's answer, such as
// a directory that doesn't answer.
func Failed(format string, args ...any) error {
	return &Failure{Status: http.StatusBadGateway, Detail: fmt.Sprintf(format, args...)}
}

// Invalid returns a failure for settings or input that can't work.
func Invalid(format string, args ...any) error {
	return &Failure{Status: http.StatusUnprocessableEntity, Detail: fmt.Sprintf(format, args...)}
}

// Handler serves the add-on protocol.
func (a *Addon) Handler() http.Handler {
	log := a.Log
	if log == nil {
		log = slog.Default()
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "version": a.Version, "apiVersion": APIVersion})
	})
	mux.HandleFunc("POST /provide/{capability}/{method}", func(w http.ResponseWriter, r *http.Request) {
		methods, ok := a.Capabilities[r.PathValue("capability")]
		if !ok {
			problem(w, http.StatusNotFound, a.Name+" doesn't provide "+r.PathValue("capability"))
			return
		}
		method, ok := methods[r.PathValue("method")]
		if !ok {
			problem(w, http.StatusNotFound, r.PathValue("capability")+" has no method "+r.PathValue("method"))
			return
		}
		var c call
		body, err := io.ReadAll(io.LimitReader(r.Body, maxCall+1))
		if err != nil || len(body) > maxCall || json.Unmarshal(body, &c) != nil {
			problem(w, http.StatusBadRequest, "the call isn't what the add-on protocol says")
			return
		}
		out, err := method(r.Context(), c.Input.Settings, c.Input.Input)
		if err != nil {
			var f *Failure
			if !errors.As(err, &f) {
				f = &Failure{Status: http.StatusInternalServerError, Detail: err.Error()}
			}
			log.Warn("a call failed", "capability", r.PathValue("capability"), "method", r.PathValue("method"), "error", f.Detail)
			problem(w, f.Status, f.Detail)
			return
		}
		writeJSON(w, http.StatusOK, out)
	})
	return mux
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func problem(w http.ResponseWriter, status int, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]any{"title": http.StatusText(status), "status": status, "detail": detail})
}

// listenFDsStart is the first file descriptor systemd passes a service it
// starts from a socket.
const listenFDsStart = 3

// Listen returns the socket systemd passed the add-on, as its
// racksail-addon@ socket unit does, or, to run it by hand, a new one at
// path.
func Listen(path string) (net.Listener, error) {
	if pid, err := strconv.Atoi(os.Getenv("LISTEN_PID")); err == nil && pid == os.Getpid() {
		if n, err := strconv.Atoi(os.Getenv("LISTEN_FDS")); err == nil && n >= 1 {
			f := os.NewFile(uintptr(listenFDsStart), "panel")
			l, err := net.FileListener(f)
			f.Close()
			return l, err
		}
	}
	if path == "" {
		return nil, errors.New("systemd passed no socket; give one to listen on with --socket")
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return net.Listen("unix", path)
}

// Run serves the add-on on its socket until it is told to stop.
func Run(a *Addon, socket string) error {
	l, err := Listen(socket)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: a.Handler(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	errs := make(chan error, 1)
	go func() { errs <- srv.Serve(l) }()
	select {
	case err := <-errs:
		return err
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(shutdown)
}

// Decode reads JSON into v, refusing what v doesn't have, so a misspelt
// setting is reported rather than ignored.
func Decode(raw json.RawMessage, v any, what string) error {
	if len(raw) == 0 {
		raw = json.RawMessage("{}")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return Invalid("the %s can't be read: %v", what, err)
	}
	return nil
}
