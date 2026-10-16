package account

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/agent"
	"github.com/vilanobeachflorida/racksail/internal/reconcile"
	"github.com/vilanobeachflorida/racksail/internal/store"
	"github.com/vilanobeachflorida/racksail/internal/system"
)

// Reconciler returns the reconciler of accounts. kinds lists every kind, so
// an account is only removed once nothing else belongs to it.
func Reconciler(kinds *store.KindRegistry) reconcile.Reconciler {
	return reconcile.Typed(Kind, &reconciler{kinds: kinds})
}

type reconciler struct {
	kinds *store.KindRegistry
}

// homeMode keeps other accounts out of an account's files; the account's
// services reach them as its user.
const homeMode = 0o750

// SliceLimits turns an account's limits into its slice's.
func SliceLimits(l AccountLimits) system.ResourceLimits {
	return system.ResourceLimits{
		MemoryHighBytes: l.MemoryHigh, MemoryMaxBytes: l.MemoryMax, CPUQuotaPercent: l.CPU,
		CPUWeight: l.CPUWeight, IOWeight: l.IOWeight, TasksMax: l.Tasks,
	}
}

// Steps returns what makes the machine match an account whose Linux IDs
// are uid: its group and user, its home and its slice.
func Steps(spec AccountSpec, uid int) ([]agent.Step, error) {
	slice, err := system.AccountSliceSpec(spec.Slug, SliceLimits(spec.Limits)).Render()
	if err != nil {
		return nil, fmt.Errorf("account %s: rendering its slice: %w", spec.Slug, err)
	}
	var subIDs *system.IDRange
	if uid != 0 {
		r := system.AccountSubIDs(uid)
		subIDs = &r
	}
	home := system.AccountDir(spec.Slug)
	return []agent.Step{
		agent.EnsureGroup.Step(agent.EnsureGroupInput{Name: spec.Slug, GID: uid}),
		agent.EnsureUser.Step(agent.EnsureUserInput{
			Name: spec.Slug, UID: uid, GID: uid, Home: home, Comment: comment(spec.Name), Locked: true, SubIDs: subIDs,
		}),
		agent.EnsureDir.Step(agent.EnsureDirInput{Path: home, Mode: homeMode, Owner: system.Owner{User: spec.Slug, Group: spec.Slug}}),
		agent.EnsureUnit.Step(agent.EnsureUnitInput{Name: system.AccountSlice(spec.Slug), Content: slice}),
	}, nil
}

// comment is the account's name as /etc/passwd can hold it.
func comment(name string) string {
	c := "Racksail account " + strings.Map(func(r rune) rune {
		if r < 0x20 || r == ':' || r == 0x7f {
			return ' '
		}
		return r
	}, name)
	for len(c) > 128 {
		c = c[:len(c)-1]
	}
	return c
}

// RemoveSteps returns what removes an account from the machine, its files
// included; the change pipeline snapshots them first where the storage
// allows.
func RemoveSteps(spec AccountSpec) []agent.Step {
	return []agent.Step{
		agent.RemoveUnit.Step(agent.UnitInput{Name: system.AccountSlice(spec.Slug)}),
		agent.DeleteUser.Step(agent.DeleteUserInput{Name: spec.Slug}),
		agent.DeleteGroup.Step(agent.NameInput{Name: spec.Slug}),
		agent.RemovePath.Step(agent.RemovePathInput{Path: system.AccountDir(spec.Slug), Recursive: true}),
	}
}

func (r *reconciler) Plan(ctx context.Context, obj *store.Object[AccountSpec, AccountStatus], req reconcile.Request) (reconcile.Plan, error) {
	if obj.Deleted() {
		if err := r.checkEmpty(ctx, obj, req.Read); err != nil {
			return reconcile.Plan{}, err
		}
		return reconcile.Plan{Steps: RemoveSteps(obj.Spec)}, nil
	}
	var steps []agent.Step
	if obj.Status.UID == 0 {
		// The account's IDs are picked when it is first applied; the
		// preview shows the steps with the ID still to choose.
		steps = append(steps, agent.AllocateAccountID.Step(agent.AllocateAccountIDInput{}))
	}
	rest, err := Steps(obj.Spec, obj.Status.UID)
	if err != nil {
		return reconcile.Plan{}, err
	}
	return reconcile.Plan{Steps: append(steps, rest...)}, nil
}

func (r *reconciler) Apply(ctx context.Context, obj *store.Object[AccountSpec, AccountStatus], plan reconcile.Plan, ex reconcile.Executor) (AccountStatus, error) {
	st := obj.Status
	now := time.Now()
	if !obj.Deleted() && st.UID == 0 {
		results, err := ex.Run(ctx, []agent.Step{agent.AllocateAccountID.Step(agent.AllocateAccountIDInput{})})
		var id agent.AccountID
		if err == nil && (len(results) != 1 || json.Unmarshal(results[0].Output, &id) != nil || id.UID == 0) {
			err = fmt.Errorf("account %s: the agent picked no account ID", obj.Spec.Slug)
		}
		if err != nil {
			// Recorded the way a failed step is, so the account shows why.
			return st, reconcile.ApplySteps(ctx, obj.Generation, reconcile.Plan{}, failed{err}, &st.Status, now)
		}
		st.UID = id.UID
		steps, err := Steps(obj.Spec, st.UID)
		if err != nil {
			return st, err
		}
		plan = reconcile.Plan{Steps: steps, Checks: plan.Checks, Effects: plan.Effects}
	}
	err := reconcile.ApplySteps(ctx, obj.Generation, plan, ex, &st.Status, now)
	if err == nil && !obj.Deleted() {
		st.User, st.Home, st.Slice = obj.Spec.Slug, system.AccountDir(obj.Spec.Slug), system.AccountSlice(obj.Spec.Slug)
	}
	return st, err
}

// failed is an executor whose every run fails with err, so ApplySteps
// records a failure that happened before any step ran.
type failed struct{ err error }

func (f failed) Run(ctx context.Context, steps []agent.Step) ([]agent.StepResult, error) {
	return nil, f.err
}

// checkEmpty refuses to remove an account while resources still belong to
// it: removing its files would pull them out from under those resources.
func (r *reconciler) checkEmpty(ctx context.Context, obj *store.Object[AccountSpec, AccountStatus], rd store.Reader) error {
	if r.kinds == nil || rd == nil {
		return nil
	}
	scope := store.AccountScope(obj.ID)
	var left []string
	for _, k := range r.kinds.All() {
		info := k.Info()
		if info.Name == Kind.Name() || info.Scope != store.ScopeAccount {
			continue
		}
		page, err := rd.List(ctx, store.Query{Kind: info.Name, Scope: &scope, Limit: 2})
		if err != nil {
			return err
		}
		switch n := len(page.Items); {
		case n == 1 && page.Next == "":
			left = append(left, "1 "+strings.ToLower(info.Title))
		case n > 0:
			left = append(left, "some "+info.Plural)
		}
	}
	if len(left) == 0 {
		return nil
	}
	what := left[0]
	if len(left) > 1 {
		what = strings.Join(left[:len(left)-1], ", ") + " and " + left[len(left)-1]
	}
	return ErrNotEmpty.New("what", what)
}
