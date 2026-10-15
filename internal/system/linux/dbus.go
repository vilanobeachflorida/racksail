//go:build linux

package linux

import (
	"context"
	"fmt"
	"maps"
	"path"

	sddbus "github.com/coreos/go-systemd/v22/dbus"
)

// systemdBus implements Systemd over systemd's D-Bus API.
type systemdBus struct {
	conn *sddbus.Conn
}

// DialSystemd connects to the system instance of systemd.
func DialSystemd(ctx context.Context) (Systemd, error) {
	conn, err := sddbus.NewSystemConnectionContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("connecting to systemd: %w", err)
	}
	return &systemdBus{conn: conn}, nil
}

// job queues a job with mode "replace" and waits for systemd to report it
// done.
func (s *systemdBus) job(ctx context.Context, what, name string, start func(context.Context, string, string, chan<- string) (int, error)) error {
	done := make(chan string, 1)
	if _, err := start(ctx, name, "replace", done); err != nil {
		return fmt.Errorf("%s %s: %w", what, name, err)
	}
	select {
	case result := <-done:
		if result != "done" {
			return fmt.Errorf("%s %s: job %s", what, name, result)
		}
		return nil
	case <-ctx.Done():
		return fmt.Errorf("%s %s: %w", what, name, ctx.Err())
	}
}

func (s *systemdBus) DaemonReload(ctx context.Context) error { return s.conn.ReloadContext(ctx) }

func (s *systemdBus) Start(ctx context.Context, name string) error {
	return s.job(ctx, "starting", name, s.conn.StartUnitContext)
}

func (s *systemdBus) Stop(ctx context.Context, name string) error {
	return s.job(ctx, "stopping", name, s.conn.StopUnitContext)
}

func (s *systemdBus) Restart(ctx context.Context, name string) error {
	return s.job(ctx, "restarting", name, s.conn.RestartUnitContext)
}

func (s *systemdBus) Reload(ctx context.Context, name string) error {
	return s.job(ctx, "reloading", name, s.conn.ReloadUnitContext)
}

func (s *systemdBus) Enable(ctx context.Context, names []string) error {
	_, _, err := s.conn.EnableUnitFilesContext(ctx, names, false, true)
	return err
}

func (s *systemdBus) Disable(ctx context.Context, names []string) error {
	_, err := s.conn.DisableUnitFilesContext(ctx, names, false)
	return err
}

func (s *systemdBus) Properties(ctx context.Context, name string) (map[string]any, error) {
	props, err := s.conn.GetUnitPropertiesContext(ctx, name)
	if err != nil {
		return nil, err
	}
	if typ := unitType(name); typ != "" && props["LoadState"] == "loaded" {
		extra, err := s.conn.GetUnitTypePropertiesContext(ctx, name, typ)
		if err == nil {
			maps.Copy(props, extra)
		}
	}
	return props, nil
}

func unitType(name string) string {
	switch path.Ext(name) {
	case ".service":
		return "Service"
	case ".socket":
		return "Socket"
	case ".slice":
		return "Slice"
	case ".timer":
		return "Timer"
	case ".mount":
		return "Mount"
	}
	return ""
}

func (s *systemdBus) List(ctx context.Context, patterns []string) ([]string, error) {
	loaded, err := s.conn.ListUnitsByPatternsContext(ctx, nil, patterns)
	if err != nil {
		return nil, err
	}
	files, err := s.conn.ListUnitFilesByPatternsContext(ctx, nil, patterns)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []string
	for _, u := range loaded {
		if !seen[u.Name] {
			seen[u.Name] = true
			out = append(out, u.Name)
		}
	}
	for _, f := range files {
		name := path.Base(f.Path)
		if !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	return out, nil
}

func (s *systemdBus) Kill(ctx context.Context, name string, signal int32) error {
	return s.conn.KillUnitWithTarget(ctx, name, sddbus.All, signal)
}

func (s *systemdBus) ResetFailed(ctx context.Context, name string) error {
	return s.conn.ResetFailedUnitContext(ctx, name)
}
