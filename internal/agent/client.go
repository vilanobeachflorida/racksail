package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/store"
	"github.com/vilanobeachflorida/racksail/internal/system"
)

// CallerLocal is the caller of operations that arrive in-process or on the
// agent's Unix socket.
const CallerLocal = "local"

// Client calls agent operations.
type Client interface {
	// Call runs one operation with a JSON input and returns its JSON output.
	Call(ctx context.Context, op string, input json.RawMessage, opts CallOptions) (json.RawMessage, error)
}

// CallOptions apply to one call.
type CallOptions struct {
	// Check reports what would change without changing anything.
	Check bool
	// Owner is the resource the call is made for.
	Owner store.ID
}

// Local runs operations in the same process, against any backend. Development
// mode uses it with the simulated server, and tests use it everywhere.
type Local struct {
	backend *system.Backend
	log     *slog.Logger
	runtime *Runtime
}

// NewLocal returns a client that runs operations against backend, with a
// runtime of its own.
func NewLocal(backend *system.Backend, log *slog.Logger) *Local {
	if log == nil {
		log = slog.Default()
	}
	return NewLocalRuntime(NewRuntime(backend, RuntimeOptions{Log: log}))
}

// NewLocalRuntime returns a client that shares a runtime, such as the one
// of the server answering on the agent's socket in development mode.
func NewLocalRuntime(rt *Runtime) *Local {
	return &Local{backend: rt.backend, log: rt.log, runtime: rt}
}

// Runtime returns the client's runtime.
func (l *Local) Runtime() *Runtime { return l.runtime }

// Call implements Client. The input goes through the same decoding and
// validation as calls that arrive over the socket.
func (l *Local) Call(ctx context.Context, op string, input json.RawMessage, opts CallOptions) (json.RawMessage, error) {
	env := &Env{System: l.backend, Check: opts.Check, Owner: opts.Owner, Log: l.log, Runtime: l.runtime, Caller: CallerLocal}
	e, ok := Lookup(op)
	if !ok {
		err := ErrUnknownOperation.New("op", op)
		env.audit(op, nil, input, nil, err, time.Now())
		return nil, err
	}
	if e.ReadOnly() {
		env.Check = false
	}
	return e.Invoke(ctx, env, input)
}

// EventSource streams what an agent notices on its own. The clients of the
// local agent and of agents on other servers implement it.
type EventSource interface {
	// Events calls fn with each event until ctx is done or the stream ends.
	Events(ctx context.Context, fn func(Event)) error
}

// Events implements EventSource.
func (l *Local) Events(ctx context.Context, fn func(Event)) error { return l.runtime.Events(ctx, fn) }

// FollowEvents calls fn with an agent's events until ctx is done, connecting
// again whenever the stream ends, such as when the agent restarts.
func FollowEvents(ctx context.Context, src EventSource, fn func(Event), log *slog.Logger) {
	if log == nil {
		log = slog.Default()
	}
	backoff := time.Second
	for ctx.Err() == nil {
		started := time.Now()
		err := src.Events(ctx, fn)
		if ctx.Err() != nil {
			return
		}
		if time.Since(started) > time.Minute {
			backoff = time.Second
		}
		log.Debug("the agent's event stream ended", "error", err, "retry", backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, 30*time.Second)
	}
}

// StepResult is the outcome of one step of a plan.
type StepResult struct {
	Step   Step            `json:"step"`
	Output json.RawMessage `json:"output,omitempty"`
	// Result is filled for operations whose output is a Result.
	Result *Result `json:"result,omitempty"`
}

// Execute runs the steps of a plan in order and stops at the first error.
// progress, when not nil, is called before each step. In check mode nothing
// changes and the results say what would.
func Execute(ctx context.Context, c Client, steps []Step, opts CallOptions, progress func(i int, s Step)) ([]StepResult, error) {
	results := make([]StepResult, 0, len(steps))
	for i, s := range steps {
		if err := ctx.Err(); err != nil {
			return results, err
		}
		if progress != nil {
			progress(i, s)
		}
		input, err := json.Marshal(s.Input)
		if err != nil {
			return results, fmt.Errorf("step %d (%s): encoding input: %w", i+1, s.Op, err)
		}
		out, err := c.Call(ctx, s.Op, input, opts)
		if err != nil {
			return results, fmt.Errorf("step %d (%s): %w", i+1, s.Op, err)
		}
		r := StepResult{Step: s, Output: out}
		var res Result
		if json.Unmarshal(out, &res) == nil && (res.Changed || len(res.Effects) > 0) {
			r.Result = &res
		}
		results = append(results, r)
	}
	return results, nil
}

// PlannedEffects describes what steps would change, from each operation's
// static description. It needs no agent; previews use check mode when the
// agent is reachable and fall back to this.
func PlannedEffects(steps []Step) ([]system.Effect, error) {
	var effects []system.Effect
	for i, s := range steps {
		e, ok := Lookup(s.Op)
		if !ok {
			return nil, ErrUnknownOperation.New("op", s.Op)
		}
		input, err := json.Marshal(s.Input)
		if err != nil {
			return nil, fmt.Errorf("step %d (%s): encoding input: %w", i+1, s.Op, err)
		}
		eff, err := e.Effects(input)
		if err != nil {
			return nil, err
		}
		effects = append(effects, eff...)
	}
	return effects, nil
}
