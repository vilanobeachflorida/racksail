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
	register(api huma.API, srv *Server)
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
	in, err := o.decode(input)
	if err != nil {
		return nil, err
	}
	out, err := o.run(ctx, env, in)
	if err != nil {
		return nil, err
	}
	return json.Marshal(out)
}

func (o *Op[In, Out]) run(ctx context.Context, env *Env, in In) (Out, error) {
	if env.Log != nil {
		env.Log.Debug("agent operation", "op", o.def.Name, "check", env.Check, "owner", env.Owner)
	}
	out, err := o.def.Run(ctx, env, in)
	if err != nil {
		return out, failed(o.def.Name, err)
	}
	return out, nil
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

func (o *Op[In, Out]) register(api huma.API, srv *Server) {
	tag, _, ok := strings.Cut(o.def.Name, ".")
	if !ok {
		tag = "core"
	}
	huma.Register(api, huma.Operation{
		OperationID: o.def.Name,
		Method:      "POST",
		Path:        "/v1/ops/" + o.def.Name,
		Summary:     o.def.Summary,
		Description: o.def.Description,
		Tags:        []string{tag},
		Errors:      []int{400, 404, 409, 413, 422, 500},
		// Huma would cap bodies at 1 MiB; the socket's own limit applies.
		MaxBodyBytes:    maxBody,
		BodyReadTimeout: bodyReadTimeout,
	}, func(ctx context.Context, req *opRequest[In]) (*opResponse[Out], error) {
		if err := req.Body.Validate(); err != nil {
			return nil, ErrInvalidInput.Wrap(err, "op", o.def.Name, "reason", err.Error())
		}
		env := srv.env(req.Check, store.ID(req.Owner))
		out, err := o.run(ctx, env, req.Body)
		if err != nil {
			return nil, err
		}
		return &opResponse[Out]{Body: out}, nil
	})
}
