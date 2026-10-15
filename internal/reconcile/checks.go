package reconcile

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/agent"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

// Health check types.
const (
	CheckHTTP = "http"
	CheckTCP  = "tcp"
	CheckTLS  = "tls"
	CheckDNS  = "dns"
	CheckUnit = "unit"
)

// DefaultCheckTimeout is how long a health check that sets no timeout of its
// own keeps trying.
const DefaultCheckTimeout = 30 * time.Second

// ErrCheckFailed is returned when a health check didn't pass in time.
var ErrCheckFailed = i18n.Define("reconcile.check_failed", http.StatusServiceUnavailable, "The {check} health check didn't pass: {reason}.")

// Checker runs the health checks that confirm a plan worked: the verify
// stage of the change pipeline.
type Checker interface {
	// Check runs one check for a resource placed on server, and returns an
	// error when it doesn't pass in time.
	Check(ctx context.Context, server store.ID, c HealthCheck) error
}

// AgentChecker is the default Checker. Unit checks ask the agent; http, tcp,
// tls and dns checks go out from the panel, as visitors' requests would.
// Every check is tried again until it passes or its timeout runs out, since
// a service that was just started may need a moment.
type AgentChecker struct {
	Agent agent.Client
	// HTTP makes the requests of http checks. Nil means a client that
	// doesn't follow redirects, so a check can expect one.
	HTTP *http.Client
	// Interval is the pause between attempts; the default is one second.
	Interval time.Duration
}

// Check implements Checker.
func (c AgentChecker) Check(ctx context.Context, server store.ID, hc HealthCheck) error {
	if !slices.Contains([]string{CheckHTTP, CheckTCP, CheckTLS, CheckDNS, CheckUnit}, hc.Type) {
		return ErrCheckFailed.New("check", hc.Name, "reason", fmt.Sprintf("%q isn't a type of health check", hc.Type))
	}
	if hc.Type == CheckUnit && c.Agent == nil {
		return ErrCheckFailed.New("check", hc.Name, "reason", "there is no agent to ask about units")
	}
	timeout := hc.Timeout
	if timeout <= 0 {
		timeout = DefaultCheckTimeout
	}
	interval := c.Interval
	if interval <= 0 {
		interval = time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var last error
	for {
		err := c.once(ctx, hc)
		if err == nil {
			return nil
		}
		// An attempt cut short by the timeout says less than the one before.
		if last == nil || ctx.Err() == nil {
			last = err
		}
		select {
		case <-ctx.Done():
			return ErrCheckFailed.Wrap(last, "check", hc.Name, "reason", reason(last))
		case <-time.After(interval):
		}
	}
}

func (c AgentChecker) once(ctx context.Context, hc HealthCheck) error {
	switch hc.Type {
	case CheckUnit:
		st, err := agent.UnitStatus.Call(ctx, c.Agent, agent.UnitStatusInput{Name: hc.Target}, agent.CallOptions{})
		if err != nil {
			return err
		}
		want := hc.Expect
		if want == "" {
			want = "active"
		}
		if st.ActiveState != want {
			return fmt.Errorf("%s is %s, not %s", hc.Target, st.ActiveState, want)
		}
		return nil
	case CheckTCP:
		var d net.Dialer
		conn, err := d.DialContext(ctx, "tcp", hc.Target)
		if err != nil {
			return err
		}
		return conn.Close()
	case CheckTLS:
		host, _, err := net.SplitHostPort(hc.Target)
		if err != nil {
			return err
		}
		name := hc.Expect
		if name == "" {
			name = host
		}
		// The handshake checks the certificate's chain, dates and name.
		d := tls.Dialer{Config: &tls.Config{ServerName: name, MinVersion: tls.VersionTLS12}}
		conn, err := d.DialContext(ctx, "tcp", hc.Target)
		if err != nil {
			return err
		}
		return conn.Close()
	case CheckDNS:
		addrs, err := net.DefaultResolver.LookupHost(ctx, hc.Target)
		if err != nil {
			return err
		}
		if hc.Expect != "" && !slices.Contains(addrs, hc.Expect) {
			return fmt.Errorf("%s resolves to %v, not %s", hc.Target, addrs, hc.Expect)
		}
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, hc.Target, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "racksail-health-check")
	client := c.HTTP
	if client == nil {
		client = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if hc.Expect != "" {
		if strconv.Itoa(resp.StatusCode) != hc.Expect {
			return fmt.Errorf("%s answered %d, not %s", hc.Target, resp.StatusCode, hc.Expect)
		}
		return nil
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("%s answered %d", hc.Target, resp.StatusCode)
	}
	return nil
}

// reason is the text of an error for a message: a coded error's own message,
// or the error itself.
func reason(err error) string {
	var coded *i18n.Error
	if errors.As(err, &coded) {
		return coded.Message
	}
	return err.Error()
}
