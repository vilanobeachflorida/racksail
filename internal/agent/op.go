package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/vilanobeachflorida/racksail/internal/store"
	"github.com/vilanobeachflorida/racksail/internal/system"
)

// Input is implemented by every operation input. Validate must check every
// field before anything runs.
type Input interface {
	Validate() error
}

// Env is what an operation runs with.
type Env struct {
	System *system.Backend
	// Check asks for a report of what would change, without changing it.
	Check bool
	// Owner is the resource the call is made for.
	Owner store.ID
	Log   *slog.Logger
	// Runtime holds the agent's state across operations; nil when an
	// operation is run on its own, as previews do.
	Runtime *Runtime
	// Caller is who asked, for the audit trail: "local", or the identity
	// of a remote panel.
	Caller string
}

// remember records what an operation put on the machine for its owner, so
// drift can be reported. Operations call it after a change succeeded.
func (env *Env) remember(ctx context.Context, kind, target string, content []byte) {
	if env.Runtime == nil || env.Check {
		return
	}
	if err := env.Runtime.remember(ctx, kind, target, env.Owner, content); err != nil && env.Log != nil {
		env.Log.Warn("recording what the agent wrote", "target", target, "error", err)
	}
}

// forget drops removed targets from the drift ledger.
func (env *Env) forget(ctx context.Context, targets ...string) {
	if env.Runtime == nil || env.Check {
		return
	}
	if err := env.Runtime.forget(ctx, targets...); err != nil && env.Log != nil {
		env.Log.Warn("updating what the agent wrote", "error", err)
	}
}

// audit records one operation. Every operation is recorded, including
// reads and refusals; the input is kept as a hash only.
func (env *Env) audit(op string, in any, raw []byte, out any, err error, start time.Time) {
	if env.Runtime == nil {
		return
	}
	r := AuditRecord{
		Time: start.UTC(), Op: op, Caller: env.Caller, Owner: env.Owner, Check: env.Check,
		Targets: auditTargets(in), Changed: resultChanged(out), Duration: time.Since(start),
	}
	// A hash of a secret would let anyone holding the trail test guesses
	// of it.
	if e, ok := Lookup(op); !ok || !e.sensitive() {
		if raw == nil {
			raw, _ = json.Marshal(in)
		}
		r.InputSHA256 = inputHash(raw)
	}
	r.Code, r.Error = auditError(err)
	if aerr := env.Runtime.audit.Record(r); aerr != nil && env.Log != nil {
		env.Log.Error("writing the audit record", "op", op, "error", aerr)
	}
	if env.Log != nil {
		level := slog.LevelDebug
		if r.Changed || r.Failed() {
			level = slog.LevelInfo
		}
		env.Log.Log(context.Background(), level, "agent operation", "op", op, "caller", r.Caller, "owner", r.Owner,
			"check", r.Check, "targets", r.Targets, "changed", r.Changed, "code", r.Code, "duration", r.Duration)
	}
}

// resultChanged reports whether an operation's output says it changed
// something, for outputs that are or embed a Result.
func resultChanged(out any) bool {
	switch r := out.(type) {
	case Result:
		return r.Changed
	case *Result:
		return r != nil && r.Changed
	}
	v := reflect.Indirect(reflect.ValueOf(out))
	if v.Kind() == reflect.Struct {
		if f := v.FieldByName("Changed"); f.IsValid() && f.Kind() == reflect.Bool {
			return f.Bool()
		}
	}
	return false
}

// Result is the output of operations that change the machine.
type Result struct {
	Changed bool `json:"changed" doc:"Whether anything changed, or in check mode would change"`
	// Effects lists what changed, or in check mode what would change.
	Effects []system.Effect `json:"effects,omitempty" doc:"What changed, or would change"`
}

// Unchanged is the result of an operation whose effect is already in place.
func Unchanged() Result { return Result{} }

// Changed is the result of an operation that changed something.
func Changed(effects ...system.Effect) Result { return Result{Changed: true, Effects: effects} }

// Def defines an operation.
type Def[In Input, Out any] struct {
	// Name is CamelCase. Core operations have bare names ("WriteFile");
	// module operations are prefixed with the module ("dns.ReloadZones").
	Name string
	// Summary and Description document the operation in the agent protocol.
	Summary     string
	Description string
	// ReadOnly operations never change the machine.
	ReadOnly bool
	// Sensitive operations take or return secrets, so the audit trail keeps
	// no hash of their input.
	Sensitive bool
	// Local operations run only for callers in the agent's own process, the
	// installer and the self-update code, through Local or from another
	// operation; the agent's socket and panels on other servers are refused
	// them, since they write what runs as root.
	Local bool
	// Effects describes what a step would do, without asking the machine.
	// Previews use it when check mode isn't available.
	Effects func(in In) []system.Effect
	Run     func(ctx context.Context, env *Env, in In) (Out, error)
}

// Op is a registered operation.
type Op[In Input, Out any] struct {
	def Def[In, Out]
}

// Step is one operation in a plan: the operation's name and its input.
type Step struct {
	Op string `json:"op" doc:"Operation name"`
	// Title describes the step in progress logs.
	Title string `json:"title,omitempty" doc:"What the step does, for progress logs"`
	Input any    `json:"input" doc:"The operation's input"`
}

// Entry is the untyped view of a registered operation.
type Entry interface {
	Name() string
	Summary() string
	Description() string
	ReadOnly() bool
	InputType() reflect.Type
	OutputType() reflect.Type
	// Effects describes what running the step would change.
	Effects(input json.RawMessage) ([]system.Effect, error)
	// Invoke decodes and validates the input, then runs the operation.
	Invoke(ctx context.Context, env *Env, input json.RawMessage) (json.RawMessage, error)
	// Local reports whether only callers in the agent's own process may run
	// the operation.
	Local() bool
	register(api huma.API, srv *Server)
	sensitive() bool
}

var registry = struct {
	sync.RWMutex
	ops  map[string]Entry
	errs []error
}{ops: map[string]Entry{}}

var opNamePattern = regexp.MustCompile(`^([a-z][a-z0-9]*\.)?[A-Z][A-Za-z0-9]*$`)

// Register adds an operation to the allowlist. It is meant for package
// variables:
//
//	var WriteFile = agent.Register(agent.Def[WriteFileInput, agent.Result]{...})
//
// A malformed or duplicate definition is recorded and reported by Err, which
// the agent checks before serving.
func Register[In Input, Out any](def Def[In, Out]) *Op[In, Out] {
	op := &Op[In, Out]{def: def}
	registry.Lock()
	defer registry.Unlock()
	switch {
	case !opNamePattern.MatchString(def.Name):
		registry.errs = append(registry.errs, fmt.Errorf("agent: operation name %q must be CamelCase, optionally prefixed with a module", def.Name))
	case strings.TrimSpace(def.Summary) == "":
		registry.errs = append(registry.errs, fmt.Errorf("agent: operation %s needs a summary", def.Name))
	case def.Run == nil:
		registry.errs = append(registry.errs, fmt.Errorf("agent: operation %s has no Run function", def.Name))
	case registry.ops[def.Name] != nil:
		registry.errs = append(registry.errs, fmt.Errorf("agent: operation %s is registered twice", def.Name))
	default:
		registry.ops[def.Name] = op
	}
	return op
}

// Lookup returns the registered operation with the given name.
func Lookup(name string) (Entry, bool) {
	registry.RLock()
	defer registry.RUnlock()
	e, ok := registry.ops[name]
	return e, ok
}

// Operations returns every registered operation sorted by name.
func Operations() []Entry {
	registry.RLock()
	defer registry.RUnlock()
	out := make([]Entry, 0, len(registry.ops))
	for _, e := range registry.ops {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out
}

// Err reports problems found while operations were registered.
func Err() error {
	registry.RLock()
	defer registry.RUnlock()
	return errors.Join(registry.errs...)
}

// Name returns the operation name.
func (o *Op[In, Out]) Name() string { return o.def.Name }

// Summary returns the one-line description.
func (o *Op[In, Out]) Summary() string { return o.def.Summary }

// Description returns the longer description.
func (o *Op[In, Out]) Description() string { return o.def.Description }

// ReadOnly reports whether the operation never changes the machine.
func (o *Op[In, Out]) ReadOnly() bool { return o.def.ReadOnly }

func (o *Op[In, Out]) sensitive() bool { return o.def.Sensitive }

// Local reports whether only callers in the agent's own process may run the
// operation.
func (o *Op[In, Out]) Local() bool { return o.def.Local }

// InputType returns the Go type of the input.
func (o *Op[In, Out]) InputType() reflect.Type { return reflect.TypeFor[In]() }

// OutputType returns the Go type of the output.
func (o *Op[In, Out]) OutputType() reflect.Type { return reflect.TypeFor[Out]() }

// Step returns a plan step that runs the operation with in.
func (o *Op[In, Out]) Step(in In) Step {
	return Step{Op: o.def.Name, Title: o.def.Summary, Input: in}
}

// Titled returns a plan step with a specific title, such as "Write the PHP
// pool for blog".
func (o *Op[In, Out]) Titled(title string, in In) Step {
	return Step{Op: o.def.Name, Title: title, Input: in}
}

// Call runs the operation through a client and decodes its output.
func (o *Op[In, Out]) Call(ctx context.Context, c Client, in In, opts CallOptions) (Out, error) {
	var out Out
	raw, err := json.Marshal(in)
	if err != nil {
		return out, fmt.Errorf("encoding input of %s: %w", o.def.Name, err)
	}
	res, err := c.Call(ctx, o.def.Name, raw, opts)
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal(res, &out); err != nil {
		return out, fmt.Errorf("decoding output of %s: %w", o.def.Name, err)
	}
	return out, nil
}

func (o *Op[In, Out]) decode(input json.RawMessage) (In, error) {
	var in In
	dec := json.NewDecoder(bytes.NewReader(input))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		return in, ErrInvalidInput.Wrap(err, "op", o.def.Name, "reason", err.Error())
	}
	if err := in.Validate(); err != nil {
		return in, ErrInvalidInput.Wrap(err, "op", o.def.Name, "reason", err.Error())
	}
	return in, nil
}

// Effects implements Entry.
func (o *Op[In, Out]) Effects(input json.RawMessage) ([]system.Effect, error) {
	in, err := o.decode(input)
	if err != nil {
		return nil, err
	}
	if o.def.Effects == nil {
		return nil, nil
	}
	return o.def.Effects(in), nil
}

// Invoke implements Entry.
func (o *Op[In, Out]) Invoke(ctx context.Context, env *Env, input json.RawMessage) (json.RawMessage, error) {
	start := time.Now()
	in, err := o.decode(input)
	if err != nil {
		env.audit(o.def.Name, in, input, nil, err, start)
		return nil, err
	}
	out, err := o.run(ctx, env, in, input)
	if err != nil {
		return nil, err
	}
	return json.Marshal(out)
}

func (o *Op[In, Out]) run(ctx context.Context, env *Env, in In, raw []byte) (Out, error) {
	start := time.Now()
	out, err := o.def.Run(ctx, env, in)
	if err != nil {
		err = failed(o.def.Name, err)
	}
	env.audit(o.def.Name, in, raw, out, err, start)
	return out, err
}

// opRequest and opResponse are the HTTP shapes of an operation call.
type opRequest[In any] struct {
	Check bool   `query:"check" doc:"Report what would change without changing anything"`
	Owner string `header:"Racksail-Owner" doc:"The resource the call is made for"`
	Body  In
}

type opResponse[Out any] struct {
	Body Out
}

// errNotInProcess refuses a local operation to a caller outside the agent.
func errNotInProcess(op string) error {
	return ErrNotAllowed.New("op", op, "target", "anything for a caller outside the agent's own process")
}

func (o *Op[In, Out]) register(api huma.API, srv *Server) {
	tag, _, ok := strings.Cut(o.def.Name, ".")
	if !ok {
		tag = "core"
	}
	description := o.def.Description
	if o.def.Local {
		description = strings.TrimSpace(description + " Only the installer and the self-update code, which run in the agent's own process, " +
			"can call it; the agent's socket and panels on other servers are refused.")
	}
	huma.Register(api, huma.Operation{
		OperationID: o.def.Name,
		Method:      "POST",
		Path:        "/v1/ops/" + o.def.Name,
		Summary:     o.def.Summary,
		Description: description,
		Tags:        []string{tag},
		Errors:      []int{400, 404, 409, 413, 422, 500},
		// Huma would cap bodies at 1 MiB; the socket's own limit applies.
		MaxBodyBytes:    maxBody,
		BodyReadTimeout: bodyReadTimeout,
	}, func(ctx context.Context, req *opRequest[In]) (*opResponse[Out], error) {
		env := srv.env(ctx, req.Check && !o.def.ReadOnly, store.ID(req.Owner))
		markAudited(ctx)
		// Server.Handler refuses local operations before this; the handler
		// refuses them too, whatever serves it.
		if o.def.Local {
			err := errNotInProcess(o.def.Name)
			env.audit(o.def.Name, req.Body, nil, nil, err, time.Now())
			return nil, err
		}
		if err := req.Body.Validate(); err != nil {
			err = ErrInvalidInput.Wrap(err, "op", o.def.Name, "reason", err.Error())
			env.audit(o.def.Name, req.Body, nil, nil, err, time.Now())
			return nil, err
		}
		out, err := o.run(ctx, env, req.Body, nil)
		if err != nil {
			return nil, err
		}
		return &opResponse[Out]{Body: out}, nil
	})
}
