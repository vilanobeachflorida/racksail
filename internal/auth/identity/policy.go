package identity

import (
	"context"
	"fmt"
	"net/netip"
	"slices"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/reconcile"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

// SignInPolicySpec is how people sign in to the panel: how long sessions
// last, what locks a user out, who needs a second step and where the panel
// may be used from. A server has at most one; without it the defaults
// apply.
type SignInPolicySpec struct {
	SessionIdleMinutes   int         `json:"sessionIdleMinutes" minimum:"5" maximum:"1440" default:"60" doc:"Sign people out after this many minutes without activity"`
	SessionLifetimeHours int         `json:"sessionLifetimeHours" minimum:"1" maximum:"720" default:"12" doc:"Sign people out this many hours after they signed in, however active they are"`
	RememberDeviceDays   int         `json:"rememberDeviceDays" minimum:"0" maximum:"365" default:"30" doc:"How long a browser someone trusts skips the second sign-in step; 0 turns trusting browsers off"`
	StepUpMinutes        int         `json:"stepUpMinutes" minimum:"1" maximum:"120" default:"10" doc:"How long confirming it's you lasts before sensitive actions ask again"`
	LockoutAttempts      int         `json:"lockoutAttempts" minimum:"3" maximum:"100" default:"10" doc:"Failed sign-ins in a row that lock a user out"`
	LockoutMinutes       int         `json:"lockoutMinutes" minimum:"1" maximum:"1440" default:"15" doc:"How long a lockout lasts; passkeys still sign the user in"`
	RequireTwoFactor     []auth.Role `json:"requireTwoFactor,omitempty" doc:"Roles whose users must set up an authenticator app or a passkey before they can do anything else"`
	AllowFrom            []string    `json:"allowFrom,omitempty" doc:"Addresses and networks people may sign in and use API tokens from; empty allows anywhere. The server itself is always allowed"`
	SignInAlerts         string      `json:"signInAlerts,omitempty" enum:"new-device,every,off" default:"new-device" doc:"When people get an alert about signing in: from a new device, every time, or never"`
	TokenMaxDays         int         `json:"tokenMaxDays,omitempty" minimum:"0" maximum:"3650" doc:"The longest an API token may last, in days; 0 lets tokens last until they're revoked"`
}

// SignInPolicyStatus is what the reconciler last saw of the policy.
type SignInPolicyStatus struct {
	store.Status
}

// PolicyKind is core.signin_policy.
var PolicyKind = store.Define[SignInPolicySpec, SignInPolicyStatus](store.KindInfo{
	Name: "core.signin_policy", Prefix: "signin", Scope: store.ScopeServer, Singleton: true,
	Title: "Sign-in policy", Plural: "signin-policies",
	Description: "How people sign in to the panel: how long sessions last, how many failed sign-ins lock a user out, which roles need a second step, " +
		"and which networks the panel may be used from. Without one, sessions last 12 hours or an hour idle and ten failures lock a user out for 15 minutes.",
	// A narrower allowlist or a second step for everyone changes how every
	// person signs in.
	Risks:   store.Risks{Create: store.RiskHigh, Update: store.RiskHigh, Delete: store.RiskMedium},
	Confirm: true,
	UI: store.UIHints{
		Icon: "shield-check", Hidden: true,
		Groups: []store.FieldGroup{
			{Name: "Sessions", Fields: []string{"sessionIdleMinutes", "sessionLifetimeHours", "rememberDeviceDays", "stepUpMinutes"}},
			{Name: "Lockouts", Fields: []string{"lockoutAttempts", "lockoutMinutes"}},
			{Name: "Access", Fields: []string{"requireTwoFactor", "allowFrom", "signInAlerts", "tokenMaxDays"}},
		},
	},
	Backup: store.Backup{},
}).WithValidator(validatePolicy)

func init() {
	store.RegisterKind(PolicyKind)
}

// DefaultPolicy is the policy of a server that hasn't set one.
func DefaultPolicy() SignInPolicySpec {
	return SignInPolicySpec{
		SessionIdleMinutes: 60, SessionLifetimeHours: 12, RememberDeviceDays: 30, StepUpMinutes: 10,
		LockoutAttempts: 10, LockoutMinutes: 15, SignInAlerts: "new-device",
	}
}

// effective fills what a stored policy leaves at zero with the defaults,
// except remembering devices, where zero means off.
func (p SignInPolicySpec) effective() SignInPolicySpec {
	d := DefaultPolicy()
	fill := func(v *int, def int) {
		if *v <= 0 {
			*v = def
		}
	}
	fill(&p.SessionIdleMinutes, d.SessionIdleMinutes)
	fill(&p.SessionLifetimeHours, d.SessionLifetimeHours)
	fill(&p.StepUpMinutes, d.StepUpMinutes)
	fill(&p.LockoutAttempts, d.LockoutAttempts)
	fill(&p.LockoutMinutes, d.LockoutMinutes)
	if p.SignInAlerts == "" {
		p.SignInAlerts = d.SignInAlerts
	}
	return p
}

// Idle, Lifetime, RememberDevice, StepUp and Lockout are the policy's
// durations.
func (p SignInPolicySpec) Idle() time.Duration {
	return time.Duration(p.SessionIdleMinutes) * time.Minute
}
func (p SignInPolicySpec) Lifetime() time.Duration {
	return time.Duration(p.SessionLifetimeHours) * time.Hour
}
func (p SignInPolicySpec) RememberDevice() time.Duration {
	return time.Duration(p.RememberDeviceDays) * 24 * time.Hour
}
func (p SignInPolicySpec) StepUp() time.Duration {
	return time.Duration(p.StepUpMinutes) * time.Minute
}
func (p SignInPolicySpec) Lockout() time.Duration {
	return time.Duration(p.LockoutMinutes) * time.Minute
}

// Networks returns AllowFrom as networks.
func (p SignInPolicySpec) Networks() []netip.Prefix {
	nets, _ := ParseNetworks(p.AllowFrom)
	return nets
}

// ParseNetworks reads addresses and networks such as 192.0.2.0/24 or
// 2001:db8::1. An address on its own is a network of one.
func ParseNetworks(list []string) ([]netip.Prefix, error) {
	out := make([]netip.Prefix, 0, len(list))
	for _, n := range list {
		if p, err := netip.ParsePrefix(n); err == nil {
			if a := p.Addr(); a.Is4In6() && p.Bits() >= 96 {
				p = netip.PrefixFrom(a.Unmap(), p.Bits()-96)
			}
			out = append(out, p.Masked())
			continue
		}
		a, err := netip.ParseAddr(n)
		if err != nil || a.Zone() != "" {
			return nil, fmt.Errorf("%q isn't an address or network", n)
		}
		a = a.Unmap()
		out = append(out, netip.PrefixFrom(a, a.BitLen()))
	}
	return out, nil
}

// Allowed reports whether ip is inside one of nets, or nets is empty.
func Allowed(nets []netip.Prefix, ip string) bool {
	if len(nets) == 0 {
		return true
	}
	a, err := netip.ParseAddr(ip)
	if err != nil {
		return false
	}
	a = a.Unmap()
	return slices.ContainsFunc(nets, func(p netip.Prefix) bool { return p.Contains(a) })
}

// loopback reports whether ip is the server itself, which the policy's
// allowlist never keeps out, so an admin on the server can always undo a
// mistake.
func loopback(ip string) bool {
	a, err := netip.ParseAddr(ip)
	return err == nil && a.Unmap().IsLoopback()
}

func validatePolicy(ctx context.Context, obj, old *store.Object[SignInPolicySpec, SignInPolicyStatus]) error {
	var fields []i18n.FieldError
	nets, err := ParseNetworks(obj.Spec.AllowFrom)
	if err != nil {
		fields = append(fields, i18n.Field("spec.allowFrom", i18n.ErrBadValue, "reason", err.Error()))
	}
	for i, r := range obj.Spec.RequireTwoFactor {
		if !r.Valid() {
			fields = append(fields, i18n.Field(fmt.Sprintf("spec.requireTwoFactor[%d]", i), i18n.ErrBadValue, "reason", "use admin, reseller, owner, collaborator or readonly"))
		}
	}
	if s := obj.Spec; s.SessionLifetimeHours > 0 && s.SessionIdleMinutes > s.SessionLifetimeHours*60 {
		fields = append(fields, i18n.Field("spec.sessionIdleMinutes", i18n.ErrBadValue, "reason", "it can't be longer than a session lasts"))
	}
	if len(fields) > 0 {
		return i18n.Invalid(fields...)
	}
	// An allowlist that leaves out the person setting it would lock them
	// out, unless they're on the server itself, which is always allowed.
	if actor, ok := auth.ActorFrom(ctx); ok && len(nets) > 0 && actor.IP != "" && !loopback(actor.IP) && !Allowed(nets, actor.IP) {
		return i18n.Invalid(i18n.Field("spec.allowFrom", auth.ErrAllowlistLocksOut, "address", actor.IP))
	}
	return nil
}

// policyReconciler marks the policy ready: it changes nothing on the
// machine, the panel applies it as soon as it is saved.
type policyReconciler struct{}

func (policyReconciler) Plan(ctx context.Context, obj *store.Object[SignInPolicySpec, SignInPolicyStatus], req reconcile.Request) (reconcile.Plan, error) {
	return reconcile.Plan{}, nil
}

func (policyReconciler) Apply(ctx context.Context, obj *store.Object[SignInPolicySpec, SignInPolicyStatus], plan reconcile.Plan, ex reconcile.Executor) (SignInPolicyStatus, error) {
	st := obj.Status
	err := reconcile.ApplySteps(ctx, obj.Generation, plan, ex, &st.Status, time.Now())
	return st, err
}

// PolicyReconciler returns the reconciler of the sign-in policy.
func PolicyReconciler() reconcile.Reconciler { return reconcile.Typed(PolicyKind, policyReconciler{}) }
