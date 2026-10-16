package addons

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/api/httpapi"
	"github.com/vilanobeachflorida/racksail/internal/hooks"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
)

// maxReply bounds what an add-on may answer with; a modify hook returns a
// change set, which is far smaller.
const maxReply = 8 << 20

// Client calls an add-on over its Unix socket, as the add-on protocol in
// spec/addon-protocol.json describes.
type Client struct {
	// Name is the add-on's name, for messages.
	Name string
	// Socket is the add-on's socket.
	Socket string
	http   *http.Client
}

// ErrUnreachable answers a request for an add-on that didn't answer.
var ErrUnreachable = i18n.Define("addons.unreachable", http.StatusBadGateway, "{addon} didn't answer. It may be starting, stopped or in safe mode.")

// NewClient returns a client for the add-on called name serving on socket.
func NewClient(name, socket string) *Client {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socket)
		},
		MaxIdleConns:        4,
		IdleConnTimeout:     time.Minute,
		DisableCompression:  true,
		MaxResponseHeaderBytes: 64 << 10,
	}
	return &Client{Name: name, Socket: socket, http: &http.Client{Transport: transport}}
}

// Close drops the client's idle connections.
func (c *Client) Close() { c.http.CloseIdleConnections() }

// ErrUnavailable says an add-on didn't answer.
var ErrUnavailable = errors.New("the add-on didn't answer")

// do sends one request and decodes a JSON answer into out. A problem the
// add-on answers with becomes an error with its detail.
func (c *Client) do(ctx context.Context, method, path string, in, out any, header http.Header) error {
	var body io.Reader
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://addon"+path, body)
	if err != nil {
		return err
	}
	for k, v := range header {
		req.Header[k] = v
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Racksail-Hook-API", hooks.APIVersion)
	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxReply+1))
	if err != nil {
		return fmt.Errorf("reading the add-on's answer: %w", err)
	}
	if len(data) > maxReply {
		return fmt.Errorf("the add-on answered with more than %d bytes", maxReply)
	}
	if resp.StatusCode >= 300 {
		var p httpapi.Problem
		if json.Unmarshal(data, &p) == nil && p.Detail != "" {
			return fmt.Errorf("the add-on answered %d: %s", resp.StatusCode, p.Detail)
		}
		return fmt.Errorf("the add-on answered %d", resp.StatusCode)
	}
	if out == nil {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(out); err != nil {
		return fmt.Errorf("the add-on's answer isn't what the protocol says: %w", err)
	}
	return nil
}

// Health asks the add-on how it is.
func (c *Client) Health(ctx context.Context) (hooks.AddonHealth, error) {
	var h hooks.AddonHealth
	err := c.do(ctx, http.MethodGet, "/health", nil, &h, nil)
	return h, err
}

// Hook runs one of the add-on's hooks.
func (c *Client) Hook(ctx context.Context, call hooks.HookCall) (hooks.HookReply, error) {
	var reply hooks.HookReply
	err := c.do(ctx, http.MethodPost, "/hooks/"+url.PathEscape(string(call.Point)), call, &reply, nil)
	return reply, err
}

// Provide calls a method of a capability the add-on provides.
func (c *Client) Provide(ctx context.Context, capability, method string, in, out any) error {
	return c.do(ctx, http.MethodPost, "/provide/"+url.PathEscape(capability)+"/"+url.PathEscape(method), hooks.ProvideCall{Input: in}, out, nil)
}

// Proxy returns a handler that passes requests to the add-on under prefix,
// such as /routes/ or /ui/, with what the add-on may know about who asks.
// The panel's cookies and credentials never reach the add-on.
func (c *Client) Proxy(prefix string, who func(r *http.Request) string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.PathValue("path"), "/")
		target := "http://addon" + prefix + rest
		if r.URL.RawQuery != "" {
			target += "?" + r.URL.RawQuery
		}
		var body io.Reader
		if r.Body != nil && r.Body != http.NoBody {
			body = http.MaxBytesReader(w, r.Body, maxReply)
		}
		req, err := http.NewRequestWithContext(r.Context(), r.Method, target, body)
		if err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		for _, h := range []string{"Content-Type", "Accept", "Accept-Language", "If-None-Match", "If-Modified-Since"} {
			if v := r.Header.Get(h); v != "" {
				req.Header.Set(h, v)
			}
		}
		req.Header.Set("Racksail-Hook-API", hooks.APIVersion)
		if who != nil {
			req.Header.Set("Racksail-Actor", who(r))
		}
		if id := r.Header.Get("X-Request-Id"); id != "" {
			req.Header.Set("Racksail-Request-Id", id)
		}
		resp, err := c.http.Do(req)
		if err != nil {
			p := httpapi.FromError(ErrUnreachable.Wrap(err, "addon", c.Name))
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(p.Status)
			json.NewEncoder(w).Encode(p)
			return
		}
		defer resp.Body.Close()
		for _, h := range []string{"Content-Type", "Cache-Control", "ETag", "Last-Modified", "Content-Language"} {
			if v := resp.Header.Get(h); v != "" {
				w.Header().Set(h, v)
			}
		}
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, io.LimitReader(resp.Body, maxReply))
	})
}
