package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/api/httpapi"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
)

// HTTPClient calls an agent over HTTP: its Unix socket on the same machine,
// or mutual TLS between servers.
type HTTPClient struct {
	base string
	http *http.Client
	// retries is how many times a call that never reached the agent is
	// tried again. Operations are idempotent, so this is always safe; it
	// matters for connections an agent opened to the panel, which can be
	// gone by the time they're used.
	retries int
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
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(input))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		if opts.Owner != "" {
			req.Header.Set("Racksail-Owner", string(opts.Owner))
		}
		out, err := c.do(req)
		if err == nil || attempt >= c.retries || !ErrUnreachable.Is(err) || ctx.Err() != nil {
			return out, err
		}
	}
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
	return nil, problemError(req.URL.Path, resp.Status, body)
}

// problemError turns problem details back into the coded error the agent
// returned.
func problemError(path, status string, body []byte) error {
	var p httpapi.Problem
	if json.Unmarshal(body, &p) != nil || p.Code == "" {
		return ErrFailed.New("op", path, "reason", status)
	}
	e := &i18n.Error{Code: p.Code, Status: p.Status, Message: p.Detail, Params: p.Params}
	for _, f := range p.Errors {
		e.Fields = append(e.Fields, i18n.FieldError{Path: f.Location, Code: f.Code, Message: f.Message, Params: f.Params})
	}
	return e
}

// Events calls fn with each event the agent publishes until ctx is done or
// the connection ends.
func (c *HTTPClient) Events(ctx context.Context, fn func(Event)) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/v1/events", nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return ErrUnreachable.Wrap(err, "reason", err.Error())
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return problemError(req.URL.Path, resp.Status, body)
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var e Event
		if err := json.Unmarshal(line, &e); err == nil {
			fn(e)
		}
	}
	if err := sc.Err(); err != nil && ctx.Err() == nil && !errors.Is(err, io.EOF) {
		return ErrUnreachable.Wrap(err, "reason", err.Error())
	}
	return ctx.Err()
}
