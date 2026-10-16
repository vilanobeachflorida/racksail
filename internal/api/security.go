package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/vilanobeachflorida/racksail/internal/api/httpapi"
	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/auth/identity"
	"github.com/vilanobeachflorida/racksail/internal/changeset"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

// apiPolicy is the Content Security Policy of API answers, which load
// nothing and are never framed.
const apiPolicy = "default-src 'none'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'"

// Extensions of operations about who may use them: ExtStepUp marks those
// that need a person in the panel to have confirmed it's them recently,
// whatever their risk, and ExtSession those only a person in the panel may
// use, not an API token.
const (
	ExtStepUp  = "x-racksail-step-up"
	ExtSession = "x-racksail-session"
)

// retryAfterHeader says when to try again in the Retry-After header of
// answers whose problem says so, as the API's rate limit does: the identity
// service's limits on signing in answer through the operations.
func retryAfterHeader(ctx huma.Context, status string, v any) (any, error) {
	p, ok := v.(*httpapi.Problem)
	if !ok || p.Status != http.StatusTooManyRequests {
		return v, nil
	}
	if after, ok := p.Params["retryAfter"]; ok {
		if n, err := strconv.Atoi(fmt.Sprint(after)); err == nil && n > 0 {
			ctx.SetHeader("Retry-After", strconv.Itoa(n))
		}
	}
	return v, nil
}

// clientOf works out where a request comes from. The edge serves the panel
// under its hostname by proxying to it over loopback, and tells it the
// browser's address in X-Forwarded-For and whether it used HTTPS in
// X-Forwarded-Proto; those are believed only from loopback, where account
// workloads can't connect from (they're denied the panel's address).
func clientOf(r *http.Request) auth.Client {
	c := auth.Client{IP: auth.HostOf(r.RemoteAddr), UserAgent: r.UserAgent(), Secure: r.TLS != nil}
	if peer, err := netip.ParseAddr(c.IP); err == nil && peer.Unmap().IsLoopback() {
		if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
			parts := strings.Split(fwd, ",")
			if ip, err := netip.ParseAddr(strings.TrimSpace(parts[len(parts)-1])); err == nil {
				c.IP = ip.Unmap().String()
			}
		}
		if strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
			c.Secure = true
		}
	}
	return c
}

// addrPort turns a client's address into the host:port form the rate
// limiter and the allowlist read.
func addrPort(ip string) string { return net.JoinHostPort(ip, "0") }

// setSecurityHeaders sets the headers every answer carries; the UI's own
// page adds its Content Security Policy.
func setSecurityHeaders(h http.Header, api, secure bool) {
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cross-Origin-Opener-Policy", "same-origin")
	h.Set("Cross-Origin-Resource-Policy", "same-origin")
	if secure {
		h.Set("Strict-Transport-Security", "max-age=31536000")
	}
	if api {
		h.Set("Content-Security-Policy", apiPolicy)
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
	}
}

// crossOrigin refuses requests that change something and were sent by
// another site's page: browsers say where a request comes from in
// Sec-Fetch-Site or Origin, and the session cookie is also SameSite=Strict.
// Programs send neither, and API tokens can't be sent by another site.
var crossOrigin = http.NewCrossOriginProtection()

func crossSite(r *http.Request) error {
	if r.Header.Get("Authorization") != "" {
		return nil
	}
	if err := crossOrigin.Check(r); err != nil {
		return auth.ErrCrossSite.New()
	}
	return nil
}

// allowedWhilePending are the operations someone may use while they must
// first set up a second step or choose a new password: those that let them
// do it, and what the UI needs to show them how.
var allowedWhilePending = map[string]bool{
	"getSession": true, "saveSessionPreferences": true, "signOut": true, "getMe": true, "changeMyPassword": true,
	"beginTotp": true, "confirmTotp": true, "listMyPasskeys": true, "beginPasskeyRegistration": true,
	"finishPasskeyRegistration": true, "listMyMfa": true, "beginMfa": true, "finishMfa": true, "stepUp": true,
	"beginStepUpPasskey": true, "stopImpersonating": true,
}

// gate keeps people to what they must do first, and asks them to confirm
// it's them before sensitive actions: those marked as needing it, and
// critical ones in the panel.
func (s *Server) gate(ctx huma.Context, next func(huma.Context)) {
	op := ctx.Operation()
	actor, ok := auth.ActorFrom(ctx.Context())
	if !ok || len(op.Security) == 0 {
		next(ctx)
		return
	}
	if actor.Pending != "" && !allowedWhilePending[op.OperationID] {
		err := auth.ErrTwoFactorRequired.New()
		if actor.Pending == identity.PendingPassword {
			err = auth.ErrPasswordChange.New()
		}
		huma.WriteErr(s.api, ctx, err.Status, err.Message, err)
		return
	}
	if err := s.stepUp(actor, op, previewOnly(ctx, op)); err != nil {
		p := httpapi.FromError(err)
		huma.WriteErr(s.api, ctx, p.Status, p.Detail, err)
		return
	}
	next(ctx)
}

// previewOnly reports whether a request only previews its change: the
// operation answers dryRun with a change set and changes nothing, and the
// request asks for one. Operations that don't take dryRun ignore it and go
// ahead, so for them it never counts. The query is read both the way Huma
// fills typed inputs and the way url.Values does, and only a request both
// read as a dry run is one, so no handler can read it otherwise.
func previewOnly(ctx huma.Context, op *huma.Operation) bool {
	if takes, _ := op.Extensions[ExtDryRun].(bool); !takes {
		return false
	}
	typed, err := strconv.ParseBool(ctx.Query("dryRun"))
	if err != nil || !typed {
		return false
	}
	u := ctx.URL()
	values, err := strconv.ParseBool(u.Query().Get("dryRun"))
	return err == nil && values
}

// stepUp says whether an operation needs the actor to confirm it's them
// first: those only people in the panel use refuse API tokens, and those
// marked as needing it, or critical, need a recent confirmation from people
// in the panel. Previews change nothing, so they never need it.
func (s *Server) stepUp(actor *auth.Actor, op *huma.Operation, preview bool) error {
	if session, _ := op.Extensions[ExtSession].(bool); session && actor.Type != auth.ActorUser {
		return auth.ErrPanelOnly.New()
	}
	id := s.opts.Identity
	if id == nil || preview {
		return nil
	}
	if marked, _ := op.Extensions[ExtStepUp].(bool); marked {
		return id.RequireStepUp(actor)
	}
	if risk, _ := op.Extensions[ExtRisk].(string); risk == string(store.RiskCritical) {
		return s.stepUpFor(actor, store.RiskCritical)
	}
	return nil
}

// stepUpFor asks a person in the panel to confirm it's them before a
// change of the given risk; programs with API tokens are scoped instead.
func (s *Server) stepUpFor(actor *auth.Actor, risk store.Risk) error {
	id := s.opts.Identity
	if id == nil || risk != store.RiskCritical || actor == nil || actor.Type != auth.ActorUser || actor.SessionID == "" {
		return nil
	}
	return id.RequireStepUp(actor)
}

// confirms reports whether a change of a kind asks people in the panel to
// confirm it's them first, as its own operations do: every change of a
// kind marked Confirm, but the actions of low risk.
func (s *Server) confirms(kind string, op changeset.Op, action string) bool {
	k, ok := s.kinds.Get(kind)
	if !ok || !k.Info().Confirm {
		return false
	}
	if op != changeset.OpAction {
		return true
	}
	a, ok := k.Info().Action(action)
	return !ok || a.Risk != store.RiskLow
}

// stepUpForKinds asks a person in the panel to confirm it's them before
// changes of kinds that ask for it, however the changes are sent.
func (s *Server) stepUpForKinds(actor *auth.Actor, confirms bool) error {
	if !confirms {
		return nil
	}
	return s.stepUpFor(actor, store.RiskCritical)
}

// confirmerKey carries the server of an API request to Submit, which every
// operation that changes something answers through, those of modules and
// of the extension system included, so it asks for the confirmations the
// request's changes need, whatever operation sent them.
type confirmerKey struct{}

func withConfirmer(ctx context.Context, s *Server) context.Context {
	return context.WithValue(ctx, confirmerKey{}, s)
}

// confirmSubmitted asks for the confirmations a request's changes need,
// unless the request only previews them.
func confirmSubmitted(ctx context.Context, actor *auth.Actor, dryRun bool, changes []changeset.Change) error {
	s, ok := ctx.Value(confirmerKey{}).(*Server)
	if !ok || dryRun {
		return nil
	}
	return s.confirmChanges(ctx, actor, changes)
}

// confirmChanges asks a person in the panel to confirm it's them before a
// request's changes when one needs it: a change of a kind that asks for it,
// or a profile change that gives a user another email address, which
// outside sign-in may match people by, so it could become a way in. A
// profile change that keeps the address is held to the user as read here:
// one that lands meanwhile fails it with core.conflict rather than make it
// change the address after all.
func (s *Server) confirmChanges(ctx context.Context, actor *auth.Actor, changes []changeset.Change) error {
	needs := s.stepUpFor(actor, store.RiskCritical)
	if needs == nil {
		// Confirmed recently, or not a person in the panel.
		return nil
	}
	held := map[store.ID]bool{}
	for i := range changes {
		c := &changes[i]
		if s.confirms(c.Kind, c.Op, c.Action) {
			return needs
		}
		if c.Kind != identity.KindUser || c.Op != changeset.OpAction || c.Action != identity.ActionUpdateProfile {
			continue
		}
		// A user who is gone, or whom the actor may not change, is the
		// pipeline's to answer for, as is a profile that isn't valid.
		u, ok := s.userFor(ctx, c.ID, auth.PermUserUpdateProfile)
		if !ok {
			continue
		}
		next, err := identity.ApplyProfile(u.Spec, c.Input)
		if err != nil {
			continue
		}
		if identity.SignInMatchChanged(u.Spec, next) {
			return needs
		}
		// Only the first change of a user can name its revision; later ones
		// plan against what the first one leaves.
		if !held[c.ID] && c.Revision == 0 {
			c.Revision = u.Revision
		}
		held[c.ID] = true
	}
	return nil
}

// userFor reads a user the signed-in actor may use perm on, to decide what
// a change of them needs; ok is false when they're gone or out of reach.
func (s *Server) userFor(ctx context.Context, id store.ID, perm auth.Permission) (*store.Object[identity.UserSpec, identity.UserStatus], bool) {
	st := s.store()
	if st == nil {
		return nil, false
	}
	u, err := identity.UserKind.Get(ctx, st, id)
	if err != nil || u.Deleted() || allowed(ctx, s.authorizer(), perm, auth.Target{Scope: u.Scope, Object: u.ID}) != nil {
		return nil, false
	}
	return u, true
}

// changeSetConfirms reports whether applying a preview asks for a
// confirmation: a change of a kind that asks for it, or one that would
// leave a user with another username or email address than the store has
// now, which is what applying it changes.
func (s *Server) changeSetConfirms(ctx context.Context, cs *changeset.ChangeSet) bool {
	for _, c := range cs.Changes {
		if s.confirms(c.Kind, c.Op, c.Action) {
			return true
		}
		if c.Kind != identity.KindUser || (c.Op != changeset.OpUpdate && c.Op != changeset.OpAction) {
			continue
		}
		u, ok := s.userFor(ctx, c.ID, auth.PermUserRead)
		if !ok {
			continue
		}
		// Settings that can't be read count as a change.
		var next identity.UserSpec
		if json.Unmarshal(c.After, &next) != nil || identity.SignInMatchChanged(u.Spec, next) {
			return true
		}
	}
	return false
}

// undoConfirms reports whether undoing a change set asks for a
// confirmation, from the changes its undo makes.
func (s *Server) undoConfirms(cs *changeset.ChangeSet) bool {
	for _, c := range undoChanges(cs) {
		if s.confirms(c.kind, c.op, "") {
			return true
		}
	}
	return false
}

// undoChange is what undoing a change set does to one resource.
type undoChange struct {
	kind string
	op   changeset.Op
}

// undoChanges is what undoing a change set does, as Undo builds it:
// creates become deletes, deletes become creates, and updates and actions
// become updates that put back the settings they replaced, except those
// that changed nothing and those of a resource whose creation is undone
// too.
func undoChanges(cs *changeset.ChangeSet) []undoChange {
	created := map[store.ID]bool{}
	for _, c := range cs.Changes {
		if c.Op == changeset.OpCreate {
			created[c.ID] = true
		}
	}
	var out []undoChange
	for _, c := range cs.Changes {
		switch {
		case c.Op == changeset.OpCreate:
			out = append(out, undoChange{c.Kind, changeset.OpDelete})
		case c.Op == changeset.OpDelete:
			out = append(out, undoChange{c.Kind, changeset.OpCreate})
		case created[c.ID], bytes.Equal(c.Before, c.After) && maps.Equal(c.Labels, c.LabelsBefore):
		default:
			out = append(out, undoChange{c.Kind, changeset.OpUpdate})
		}
	}
	return out
}

// requireIdentity refuses identity operations on a panel without the
// identity service, such as when the API document is exported.
func (s *Server) requireIdentity() (*identity.Service, error) {
	if s.opts.Identity == nil {
		return nil, ErrNoSignIn.New()
	}
	return s.opts.Identity, nil
}

// requestRisk is the highest risk the kinds of a request's changes
// declare, known before anything is planned.
func (s *Server) requestRisk(changes []changeset.Change) store.Risk {
	risk := store.RiskRead
	for _, c := range changes {
		k, ok := s.kinds.Get(c.Kind)
		if !ok {
			continue
		}
		risk = store.MaxRisk(risk, opRisk(k.Info(), c.Op, c.Action))
	}
	return risk
}

// undoRisk is the risk of undoing a change set, from the changes its undo
// makes.
func (s *Server) undoRisk(cs *changeset.ChangeSet) store.Risk {
	risk := store.RiskRead
	for _, c := range undoChanges(cs) {
		if k, ok := s.kinds.Get(c.kind); ok {
			risk = store.MaxRisk(risk, opRisk(k.Info(), c.op, ""))
		}
	}
	return risk
}

func opRisk(info *store.KindInfo, op changeset.Op, action string) store.Risk {
	switch op {
	case changeset.OpCreate:
		return info.Risks.Create
	case changeset.OpDelete:
		return info.Risks.Delete
	case changeset.OpAction:
		if a, ok := info.Action(action); ok {
			return a.Risk
		}
		return store.RiskMedium
	}
	return info.Risks.Update
}
