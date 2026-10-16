package rules

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/common/types"
	"cel.dev/cel-go/common/types/ref"
	"cel.dev/cel-go/ext"
	"google.golang.org/protobuf/types/known/structpb"
)

// Limits on what a rule may cost. Rules run in the panel for every change
// they apply to, so each evaluation is bounded; CEL itself has no loops,
// side effects or network access.
const (
	// DefaultCostLimit bounds one evaluation of one expression, in CEL's cost
	// units: roughly one per operation, and one per element a macro such as
	// exists visits.
	DefaultCostLimit = 100_000
	// MaxExpressionLength is the longest expression a rule may have.
	MaxExpressionLength = 4096
)

// Variables are what rule expressions see.
var Variables = []string{"request", "resource", "oldResource", "account", "plan", "user", "server"}

// newCELEnv builds the environment rule expressions compile in: every
// variable is dynamic, as specs are JSON documents, and the string, list,
// set and math extensions are available.
func newCELEnv() (*cel.Env, error) {
	opts := []cel.EnvOption{
		cel.CrossTypeNumericComparisons(true),
		cel.ParserExpressionSizeLimit(MaxExpressionLength),
		cel.OptionalTypes(),
		ext.Strings(),
		ext.Lists(),
		ext.Sets(),
		ext.Math(),
	}
	for _, v := range Variables {
		opts = append(opts, cel.Variable(v, cel.DynType))
	}
	return cel.NewEnv(opts...)
}

// program is a compiled expression.
type program struct {
	source string
	prg    cel.Program
}

// compile parses and checks an expression. want is the type the result
// must have, or nil for any.
func compile(env *cel.Env, source string, want *cel.Type, limit uint64) (*program, error) {
	if strings.TrimSpace(source) == "" {
		return nil, errors.New("the expression is empty")
	}
	if len(source) > MaxExpressionLength {
		return nil, fmt.Errorf("the expression is longer than %d characters", MaxExpressionLength)
	}
	ast, issues := env.Compile(source)
	if issues != nil && issues.Err() != nil {
		return nil, issueError(issues.Err())
	}
	if want != nil {
		out := ast.OutputType()
		if !out.IsExactType(want) && !out.IsExactType(cel.DynType) {
			return nil, fmt.Errorf("the expression gives %s, and it has to give %s", out, want)
		}
	}
	prg, err := env.Program(ast, cel.CostLimit(limit), cel.InterruptCheckFrequency(100), cel.EvalOptions(cel.OptTrackCost))
	if err != nil {
		return nil, err
	}
	return &program{source: source, prg: prg}, nil
}

// issueError keeps the first line of CEL's report, which says what is
// wrong and where, and drops the source excerpt it adds.
func issueError(err error) error {
	msg := strings.TrimSpace(err.Error())
	msg = strings.TrimPrefix(msg, "ERROR: ")
	if i := strings.Index(msg, "\n"); i >= 0 {
		msg = msg[:i]
	}
	msg = strings.TrimPrefix(msg, "<input>:")
	return errors.New(msg)
}

// eval runs a program and returns its result as a plain Go value, made of
// the types JSON decodes into, and the cost it took.
func (p *program) eval(ctx context.Context, vars map[string]any) (any, uint64, error) {
	val, details, err := p.prg.ContextEval(ctx, vars)
	var cost uint64
	if details != nil && details.ActualCost() != nil {
		cost = *details.ActualCost()
	}
	if err != nil {
		return nil, cost, err
	}
	out, err := native(val)
	return out, cost, err
}

var jsonValue = reflect.TypeFor[*structpb.Value]()

// native converts a CEL value to the value JSON would decode it into.
func native(val ref.Val) (any, error) {
	if types.IsError(val) {
		return nil, fmt.Errorf("%v", val)
	}
	conv, err := val.ConvertToNative(jsonValue)
	if err != nil {
		return nil, fmt.Errorf("the result can't be written as JSON: %w", err)
	}
	return conv.(*structpb.Value).AsInterface(), nil
}
