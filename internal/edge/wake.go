package edge

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"path"
	"sync"
	"time"
)

// WakeProxy forwards connections from a socket systemd holds to an
// application that sleeps when idle. systemd starts the proxy on the first
// connection and, through the proxy's dependency on it, the application.
// The proxy waits for the application to accept connections, forwards to
// it, and exits once nothing has used it for IdleTimeout, which lets
// systemd stop the application too.
type WakeProxy struct {
	// Upstream is the application's Unix socket path or host:port.
	Upstream     string
	IdleTimeout  time.Duration
	StartTimeout time.Duration
	Log          *slog.Logger
}

// upstreamRetry is how often a starting application is tried again.
const upstreamRetry = 100 * time.Millisecond

func (p *WakeProxy) network() string {
	if path.IsAbs(p.Upstream) {
		return "unix"
	}
	return "tcp"
}

// Validate checks the proxy's settings.
func (p *WakeProxy) Validate() error {
	if p.network() == "tcp" {
		if _, _, err := net.SplitHostPort(p.Upstream); err != nil {
			return fmt.Errorf("upstream %q must be a socket path or host:port", p.Upstream)
		}
	}
	if p.IdleTimeout <= 0 || p.StartTimeout <= 0 {
		return errors.New("the idle and start timeouts must be positive")
	}
	return nil
}

// dial connects to the application, waiting for it to start.
func (p *WakeProxy) dial(ctx context.Context) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(ctx, p.StartTimeout)
	defer cancel()
	var d net.Dialer
	for {
		conn, err := d.DialContext(ctx, p.network(), p.Upstream)
		if err == nil {
			return conn, nil
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("the application didn't accept connections within %s: %w", p.StartTimeout, err)
		case <-time.After(upstreamRetry):
		}
	}
}

// Run serves the listeners until ctx is done or the proxy has been idle
// for IdleTimeout.
func (p *WakeProxy) Run(ctx context.Context, listeners []net.Listener) error {
	if err := p.Validate(); err != nil {
		return err
	}
	if len(listeners) == 0 {
		return errors.New("no socket to serve; the wake proxy runs under systemd socket activation")
	}
	if p.Log == nil {
		p.Log = slog.Default()
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var mu sync.Mutex
	active := 0
	idleSince := time.Now()
	var wg sync.WaitGroup
	conns := make(chan net.Conn)
	for _, ln := range listeners {
		go func() {
			for {
				conn, err := ln.Accept()
				if err != nil {
					return
				}
				select {
				case conns <- conn:
				case <-ctx.Done():
					conn.Close()
					return
				}
			}
		}()
	}
	defer func() {
		for _, ln := range listeners {
			ln.Close()
		}
		wg.Wait()
	}()

	tick := time.NewTicker(min(p.IdleTimeout/4, time.Second))
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case conn := <-conns:
			mu.Lock()
			active++
			mu.Unlock()
			wg.Add(1)
			go func() {
				defer wg.Done()
				p.forward(ctx, conn)
				mu.Lock()
				active--
				if active == 0 {
					idleSince = time.Now()
				}
				mu.Unlock()
			}()
		case <-tick.C:
			mu.Lock()
			idle := active == 0 && time.Since(idleSince) >= p.IdleTimeout
			mu.Unlock()
			if idle {
				p.Log.Info("idle; letting the application sleep", "idle", p.IdleTimeout)
				return nil
			}
		}
	}
}

// forward copies between a client and the application until both sides
// are done.
func (p *WakeProxy) forward(ctx context.Context, client net.Conn) {
	defer client.Close()
	upstream, err := p.dial(ctx)
	if err != nil {
		p.Log.Error("reaching the application", "upstream", p.Upstream, "error", err)
		return
	}
	defer upstream.Close()
	done := make(chan struct{}, 2)
	pipe := func(dst, src net.Conn) {
		io.Copy(dst, src)
		// Pass the end of one direction on, so request bodies and
		// responses can finish independently.
		if cw, ok := dst.(interface{ CloseWrite() error }); ok {
			cw.CloseWrite()
		} else {
			dst.Close()
		}
		done <- struct{}{}
	}
	go pipe(upstream, client)
	go pipe(client, upstream)
	for range 2 {
		select {
		case <-done:
		case <-ctx.Done():
			return
		}
	}
}
