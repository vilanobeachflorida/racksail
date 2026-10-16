package edge

import (
	"context"
	"log/slog"
	"maps"
	"slices"
	"sync"

	"github.com/vilanobeachflorida/racksail/internal/store"
)

// Link is the panel's Router for an edge in another process. systemd
// starts the edge the first time anything connects to its admin socket, and
// the edge should only run once something needs it, so the link reaches the
// edge only while the panel routes something the edge serves, or while the
// edge runs anyway. Until then changes wait, nobody polls for the edge's
// questions and the edge stays stopped.
//
// With the first route that isn't passive, the link sends what waited, the
// route and the panel's own route, starts answering the edge's questions
// and has the edge start at boot, so sites come back after a reboot even
// when the panel doesn't. When the last such route goes, it takes the
// panel's route away, stops answering and the edge no longer starts at
// boot.
type Link struct {
	opts LinkOptions
	base context.Context

	mu      sync.Mutex
	active  map[store.ID]bool
	pending map[store.ID][]Route
	stop    context.CancelFunc
	done    chan struct{}
}

// LinkOptions configure a Link.
type LinkOptions struct {
	Client *AdminClient
	// Responder answers the edge's questions.
	Responder Responder
	// Panel is the passive route that serves the panel under its hostname,
	// or nil.
	Panel *Route
	// Running reports whether the edge runs, so a running edge gets changes
	// at once and a stopped one isn't started for them.
	Running func(ctx context.Context) (bool, error)
	// Boot has the edge start at boot, or not. Nil leaves that alone.
	Boot func(ctx context.Context, start bool) error
	Log  *slog.Logger
}

// NewLink returns a link that answers questions until ctx is done.
func NewLink(ctx context.Context, opts LinkOptions) *Link {
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	return &Link{opts: opts, base: ctx, active: map[store.ID]bool{}, pending: map[store.ID][]Route{}}
}

// Active reports whether the panel routes something the edge serves.
func (l *Link) Active() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.active) > 0
}

// Answering reports whether the link answers the edge's questions.
func (l *Link) Answering() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.stop != nil
}

// running asks whether the edge runs. When that can't be told, the change
// goes to the edge, which keeps it consistent at the cost of starting it.
func (l *Link) running(ctx context.Context) bool {
	if l.opts.Running == nil {
		return true
	}
	ok, err := l.opts.Running(ctx)
	if err != nil {
		l.opts.Log.Warn("checking whether the edge runs", "error", err)
		return true
	}
	return ok
}

// SetRoutes implements Router.
func (l *Link) SetRoutes(ctx context.Context, owner store.ID, routes []Route) error {
	routes, err := ownedRoutes(owner, routes, Route.Validate)
	if err != nil {
		return err
	}
	serves := slices.ContainsFunc(routes, func(r Route) bool { return !r.Passive })
	l.mu.Lock()
	defer l.mu.Unlock()
	wasActive := len(l.active) > 0
	switch {
	case serves && !wasActive:
		// The edge starts with this request, if it isn't running yet.
		if err := l.flush(ctx); err != nil {
			return err
		}
		if err := l.opts.Client.SetRoutes(ctx, owner, routes); err != nil {
			return err
		}
		l.active[owner] = true
		l.activate(ctx)
	case wasActive:
		if err := l.opts.Client.SetRoutes(ctx, owner, routes); err != nil {
			return err
		}
		if serves {
			l.active[owner] = true
		} else {
			delete(l.active, owner)
		}
		if len(l.active) == 0 {
			l.deactivate(ctx)
		}
	case l.running(ctx):
		// The edge runs anyway, so what waited goes along.
		delete(l.pending, owner)
		if err := l.flush(ctx); err != nil {
			return err
		}
		return l.opts.Client.SetRoutes(ctx, owner, routes)
	default:
		// Nothing the edge serves needs it yet: the change waits.
		if routes == nil {
			routes = []Route{}
		}
		l.pending[owner] = routes
	}
	return nil
}

// DeleteRoutes implements Router.
func (l *Link) DeleteRoutes(ctx context.Context, owner store.ID) error {
	return l.SetRoutes(ctx, owner, nil)
}

// Routes implements Router: the routes the edge serves. A stopped edge
// serves none and isn't started to say so.
func (l *Link) Routes(ctx context.Context) ([]Route, error) {
	l.mu.Lock()
	active := len(l.active) > 0
	l.mu.Unlock()
	if !active && !l.running(ctx) {
		return []Route{}, nil
	}
	return l.opts.Client.Routes(ctx)
}

// Certificates lists the certificates the edge serves, with their state. A
// stopped edge serves none and isn't started to say so.
func (l *Link) Certificates(ctx context.Context) ([]Certificate, error) {
	l.mu.Lock()
	active := len(l.active) > 0
	l.mu.Unlock()
	if !active && !l.running(ctx) {
		return []Certificate{}, nil
	}
	return l.opts.Client.Certificates(ctx)
}

// flush sends the changes that waited. The caller holds l.mu.
func (l *Link) flush(ctx context.Context) error {
	for _, owner := range slices.Sorted(maps.Keys(l.pending)) {
		if err := l.opts.Client.SetRoutes(ctx, owner, l.pending[owner]); err != nil {
			return err
		}
		delete(l.pending, owner)
	}
	return nil
}

// activate sends the panel's route, starts answering questions and has the
// edge start at boot. The caller holds l.mu.
func (l *Link) activate(ctx context.Context) {
	if l.opts.Panel != nil {
		if err := l.opts.Client.SetRoutes(ctx, PanelOwner, []Route{*l.opts.Panel}); err != nil {
			l.opts.Log.Warn("the edge won't serve the panel under its hostname", "error", err)
		}
	}
	if l.stop == nil && l.opts.Responder != nil {
		qctx, cancel := context.WithCancel(l.base)
		done := make(chan struct{})
		l.stop, l.done = cancel, done
		go func() {
			defer close(done)
			AnswerQuestions(qctx, l.opts.Client, l.opts.Responder, l.opts.Log)
		}()
	}
	if l.opts.Boot != nil {
		if err := l.opts.Boot(ctx, true); err != nil {
			l.opts.Log.Warn("having the edge start at boot", "error", err)
		}
	}
	l.opts.Log.Info("the edge serves routes; the panel answers its questions")
}

// deactivate undoes activate. The caller holds l.mu.
func (l *Link) deactivate(ctx context.Context) {
	if l.opts.Panel != nil {
		if err := l.opts.Client.DeleteRoutes(ctx, PanelOwner); err != nil {
			l.opts.Log.Warn("taking the panel's route off the edge", "error", err)
		}
	}
	// The loop ends on its own once cancelled; waiting for it here could
	// hold up a question whose answer sets routes.
	if l.stop != nil {
		l.stop()
		l.stop, l.done = nil, nil
	}
	if l.opts.Boot != nil {
		if err := l.opts.Boot(ctx, false); err != nil {
			l.opts.Log.Warn("keeping the edge from starting at boot", "error", err)
		}
	}
	l.opts.Log.Info("the edge has no routes left; the panel stops answering its questions")
}

// Close stops answering questions and waits until the last answer is
// sent.
func (l *Link) Close() {
	l.mu.Lock()
	stop, done := l.stop, l.done
	l.stop, l.done = nil, nil
	l.mu.Unlock()
	if stop != nil {
		stop()
		<-done
	}
}
