package edge

import (
	"context"
	"log/slog"
	"maps"
	"slices"

	"github.com/caddyserver/caddy/v2"
	"go.uber.org/zap/zapcore"
)

// caddyLogs sends Caddy's logs to the edge's slog logger, so the edge
// writes one kind of log whatever logs it.
type caddyLogs struct {
	Instance string `json:"instance"`

	log    *slog.Logger
	fields []zapcore.Field
}

// CaddyModule implements caddy.Module.
func (caddyLogs) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{ID: "caddy.logging.cores.racksail", New: func() caddy.Module { return new(caddyLogs) }}
}

// Provision implements caddy.Provisioner.
func (c *caddyLogs) Provision(ctx caddy.Context) error {
	e, err := lookup(c.Instance)
	if err != nil {
		return err
	}
	c.log = e.log
	return nil
}

func slogLevel(l zapcore.Level) slog.Level {
	switch {
	case l <= zapcore.DebugLevel:
		return slog.LevelDebug
	case l == zapcore.InfoLevel:
		return slog.LevelInfo
	case l == zapcore.WarnLevel:
		return slog.LevelWarn
	}
	return slog.LevelError
}

// Enabled implements zapcore.Core.
func (c *caddyLogs) Enabled(l zapcore.Level) bool {
	return c.log.Enabled(context.Background(), slogLevel(l))
}

// With implements zapcore.Core.
func (c *caddyLogs) With(fields []zapcore.Field) zapcore.Core {
	clone := *c
	clone.fields = append(slices.Clip(c.fields), fields...)
	return &clone
}

// Check implements zapcore.Core.
func (c *caddyLogs) Check(e zapcore.Entry, ce *zapcore.CheckedEntry) *zapcore.CheckedEntry {
	if c.Enabled(e.Level) {
		return ce.AddCore(e, c)
	}
	return ce
}

// Write implements zapcore.Core.
func (c *caddyLogs) Write(e zapcore.Entry, fields []zapcore.Field) error {
	enc := zapcore.NewMapObjectEncoder()
	for _, f := range c.fields {
		f.AddTo(enc)
	}
	for _, f := range fields {
		f.AddTo(enc)
	}
	attrs := make([]slog.Attr, 0, len(enc.Fields)+1)
	if e.LoggerName != "" {
		attrs = append(attrs, slog.String("logger", e.LoggerName))
	}
	for _, k := range slices.Sorted(maps.Keys(enc.Fields)) {
		attrs = append(attrs, slog.Any(k, enc.Fields[k]))
	}
	c.log.LogAttrs(context.Background(), slogLevel(e.Level), e.Message, attrs...)
	return nil
}

// Sync implements zapcore.Core.
func (c *caddyLogs) Sync() error { return nil }

var (
	_ zapcore.Core      = (*caddyLogs)(nil)
	_ caddy.Provisioner = (*caddyLogs)(nil)
)
