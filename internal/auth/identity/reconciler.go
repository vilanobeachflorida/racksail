package identity

import (
	"context"
	"encoding/json"
	"slices"
	"strings"

	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/events"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/reconcile"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

// UserReconciler returns the reconciler of users. Users change nothing on
// the machine; applying one brings the panel in line with it: a user
// turned off is signed out everywhere, a deleted one takes their
// credentials, sessions and tokens along, and a new SSH key gets an alert.
// Its status says how the user signs in, and the service has it
// reconciled again whenever that changes.
func (s *Service) UserReconciler() reconcile.Reconciler {
	return reconcile.Typed(UserKind, userReconciler{s})
}

type userReconciler struct{ s *Service }

func (r userReconciler) Plan(ctx context.Context, obj *store.Object[UserSpec, UserStatus], req reconcile.Request) (reconcile.Plan, error) {
	if !obj.Deleted() {
		return reconcile.Plan{}, nil
	}
	// In the change pipeline the context says who deletes the user, which
	// a reconcile of a deletion that went through already doesn't need.
	actor, ok := auth.ActorFrom(ctx)
	if !ok {
		return reconcile.Plan{}, nil
	}
	if actor.Person() == obj.ID || actor.Impersonator == obj.ID {
		return reconcile.Plan{}, auth.ErrSelfDelete.New()
	}
	if az, ok := auth.AuthorizerFrom(ctx); ok {
		gc, ok := az.(auth.GrantChecker)
		if !ok {
			return reconcile.Plan{}, i18n.ErrInternal.New()
		}
		for _, g := range obj.Spec.Grants {
			if err := gc.CanGrant(ctx, actor, g); err != nil {
				return reconcile.Plan{}, err
			}
		}
	}
	if adminGrants(obj.Spec.Grants) && !obj.Spec.Disabled && req.Read != nil {
		left, err := adminsLeft(ctx, req.Read, obj.ID)
		if err != nil {
			return reconcile.Plan{}, err
		}
		if !left {
			return reconcile.Plan{}, auth.ErrLastAdmin.New("user", obj.Spec.Username)
		}
	}
	return reconcile.Plan{}, nil
}

func (r userReconciler) Apply(ctx context.Context, obj *store.Object[UserSpec, UserStatus], plan reconcile.Plan, ex reconcile.Executor) (UserStatus, error) {
	s := r.s
	st := obj.Status
	if obj.Deleted() {
		s.forgetUser(ctx, obj.ID)
		return st, reconcile.ApplySteps(ctx, obj.Generation, plan, ex, &st.Status, s.now())
	}
	if obj.Spec.Disabled {
		if _, err := s.opts.Store.DeleteSessions(ctx, obj.ID, ""); err != nil {
			return st, err
		}
		s.forget(ctx, obj.ID)
	}
	u, err := s.withCredentials(ctx, obj)
	if err != nil {
		return st, err
	}
	if err := s.alertNewKeys(ctx, u); err != nil {
		return st, err
	}
	st.PasswordSet, st.MustChangePassword = u.PasswordHash != "", u.MustChangePassword
	st.TwoFactor = len(s.secondFactors(ctx, u)) > 0
	st.LockedUntil, st.LastSignInAt, st.LastSignInIP = nil, timePtr(u.LastSignInAt), u.LastSignInIP
	if u.Locked(s.now()) {
		st.LockedUntil = timePtr(u.LockedUntil)
	}
	return st, reconcile.ApplySteps(ctx, obj.Generation, plan, ex, &st.Status, s.now())
}

// alertNewKeys tells the person about SSH keys added to their user since
// they were last told, whoever added them.
func (s *Service) alertNewKeys(ctx context.Context, u *User) error {
	var current, added []string
	for _, k := range u.SSHKeys {
		parsed, _, err := ParseSSHKey(k.PublicKey)
		if err != nil {
			continue
		}
		current = append(current, parsed.Fingerprint)
		if !slices.Contains(u.SSHKeysSeen, parsed.Fingerprint) {
			added = append(added, k.Name+" ("+parsed.Fingerprint+")")
		}
	}
	slices.Sort(current)
	if slices.Equal(current, u.SSHKeysSeen) {
		return nil
	}
	if err := s.opts.Store.SetSSHKeysSeen(ctx, u.ID, current); err != nil {
		return err
	}
	if len(added) > 0 {
		s.alert(ctx, u, "An SSH key was added", "The SSH key "+strings.Join(added, ", ")+" can now reach what "+u.Username+
			" may reach over SSH and SFTP. If you didn't add it, remove it from your settings.", events.SeverityNotice)
	}
	return nil
}

// PlanAction implements reconcile.ActionPlanner: the actions only change
// the user's settings.
func (r userReconciler) PlanAction(ctx context.Context, req reconcile.ActionRequest) (reconcile.ActionPlan, error) {
	obj, err := UserKind.Decode(req.Resource)
	if err != nil {
		return reconcile.ActionPlan{}, err
	}
	spec := obj.Spec
	switch req.Action {
	case ActionUpdateProfile:
		if spec, err = ApplyProfile(spec, req.Input); err != nil {
			return reconcile.ActionPlan{}, err
		}
	case "add_ssh_key":
		var in SSHKeyInput
		if err := json.Unmarshal(req.Input, &in); err != nil {
			return reconcile.ActionPlan{}, err
		}
		key, err := newSSHKey(in)
		if err != nil {
			return reconcile.ActionPlan{}, err
		}
		for _, k := range spec.SSHKeys {
			if k.ID == key.ID {
				return reconcile.ActionPlan{}, i18n.Invalid(i18n.Field("input.publicKey", auth.ErrSSHKeyExists, "name", k.Name))
			}
		}
		if len(spec.SSHKeys) >= maxSSHKeys {
			return reconcile.ActionPlan{}, auth.ErrTooMany.New("count", itoa(len(spec.SSHKeys)), "what", "SSH keys")
		}
		spec.SSHKeys = append(slices.Clone(spec.SSHKeys), key)
	case "remove_ssh_key":
		var in SSHKeyRef
		if err := json.Unmarshal(req.Input, &in); err != nil {
			return reconcile.ActionPlan{}, err
		}
		i := slices.IndexFunc(spec.SSHKeys, func(k UserSSHKey) bool { return k.ID == in.ID })
		if i < 0 {
			return reconcile.ActionPlan{}, notFound(string(in.ID))
		}
		spec.SSHKeys = slices.Delete(slices.Clone(spec.SSHKeys), i, i+1)
	default:
		return reconcile.ActionPlan{}, nil
	}
	raw, err := json.Marshal(spec)
	if err != nil {
		return reconcile.ActionPlan{}, err
	}
	return reconcile.ActionPlan{Spec: raw}, nil
}
