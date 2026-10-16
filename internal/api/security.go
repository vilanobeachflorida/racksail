package api

import (
	"context"
	"encoding/json"
	"fmt"
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
// under its hostname by proxying to it, and tells it the browser's address in
// X-Forwarded-For and whether it used HTTPS in X-Forwarded-Proto. Those are
// believed only from a trusted forwarder (see trustedForwarder): the edge
// reaches the panel over the Unix socket under /run/racksail, which account
// sandboxes can't reach. A site that reaches the panel's TCP port over
// loopback is therefore not believed, so it can't set the client address the
// allowlist, the rate limiter and the lockouts key on.
func clientOf(r *http.Request, trusted bool) auth.Client {
	c := auth.Client{IP: auth.HostOf(r.RemoteAddr), UserAgent: r.UserAgent(), Secure: r.TLS != nil}
	if !trusted {
		return c
	}
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		parts := strings.Split(fwd, ",")
		if ip, err := netip.ParseAddr(strings.TrimSpace(parts[len(parts)-1])); err == nil {
			c.IP = ip.Unmap().String()
		}
	}
	if strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		c.Secure = true
	}
	return c
}

// trustedForwarder reports whether the request's X-Forwarded-* headers may be
// believed. On a server the edge reaches the panel over the Unix socket under
// /run/racksail, which the account sandboxes are kept out of, so only a
// request that arrived on a Unix socket is trusted; a site reaching the
// panel's TCP port over loopback is not. In development the panel listens on
// loopback for the in-process edge and the tests, and trusts a loopback peer
// there.
func trustedForwarder(r *http.Request, dev bool) bool {
	if la, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr); ok && la.Network() == "unix" {
		return true
	}
	if dev {
		if peer, err := netip.ParseAddr(auth.HostOf(r.RemoteAddr)); err == nil && peer.Unmap().IsLoopback() {
			return true
		}
	}
	return false
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

// crossSite refuses requests that change something and were sent by
// another site's page: browsers say where a request comes from in
// Sec-Fetch-Site or Origin, and the session cookie is also SameSite=Strict.
// Programs send neither, and API tokens can't be sent by another site;
// nor can the tokens in the paths of extensions' PathCredentials.
func (s *Server) crossSite(r *http.Request) error {
	if r.Header.Get("Authorization") != "" {
		return nil
	}
	if err := s.crossOrigin.Check(r); err != nil {
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

// confirmerKey carries the server of an API request to Submit, which every
// operation that changes something answers through, those of modules and
// of the extension system included, so it asks for the confirmations the
// request's changes need, whatever operation sent them.
type confirmerKey struct{}

func withConfirmer(ctx context.Context, s *Server) context.Context {
	return context.WithValue(ctx, confirmerKey{}, s)
}

// confirmerFor is what Submit has the pipeline ask about a request of the
// API's once it is planned, after what the request asks already; outside
// an API request it is only that.
func confirmerFor(ctx context.Context, actor *auth.Actor, also changeset.Confirm) changeset.Confirm {
	s, ok := ctx.Value(confirmerKey{}).(*Server)
	if !ok {
		return also
	}
	mine := s.confirmPlan(actor)
	if also == nil {
		return mine
	}
	return func(ctx context.Context, cs *changeset.ChangeSet) error {
		if err := also(ctx, cs); err != nil {
			return err
		}
		return mine(ctx, cs)
	}
}

// confirmPlan asks a person in the panel to confirm it's them before a
// change set is written, when what it does needs it. The pipeline asks it
// about the change set as planned, which it writes only at the revisions it
// was planned against, so a change that lands meanwhile can't make it do
// something it wasn't judged by: whatever the actor may read, and however
// the changes were sent, previews applied later and undos included.
func (s *Server) confirmPlan(actor *auth.Actor) changeset.Confirm {
	return func(ctx context.Context, cs *changeset.ChangeSet) error {
		if !s.planConfirms(cs) {
			return nil
		}
		return s.stepUpFor(actor, store.RiskCritical)
	}
}

// planConfirms reports whether a planned change set asks people in the
// panel to confirm it's them: a critical one, one with a change of a kind
// that asks for it, or one that gives a user another username or email
// address than they have, their own or someone else's, which outside
// sign-in may match people by the first time they sign in, so it could
// become a way in. What hooks added counts as much as what was asked for.
func (s *Server) planConfirms(cs *changeset.ChangeSet) bool {
	if cs.Risk == store.RiskCritical {
		return true
	}
	for _, c := range cs.Changes {
		if s.confirms(c.Kind, c.Op, c.Action) || signInMatchChanged(c) {
			return true
		}
	}
	return false
}

// signInMatchChanged reports whether a planned change of a user changes
// what outside sign-in matches them by. Settings that can't be read count
// as a change.
func signInMatchChanged(c changeset.ResourceChange) bool {
	if c.Kind != identity.KindUser || (c.Op != changeset.OpUpdate && c.Op != changeset.OpAction) {
		return false
	}
	var before, after identity.UserSpec
	if json.Unmarshal(c.Before, &before) != nil || json.Unmarshal(c.After, &after) != nil {
		return true
	}
	return identity.SignInMatchChanged(before, after)
}

// requireIdentity refuses identity operations on a panel without the
// identity service, such as when the API document is exported.
func (s *Server) requireIdentity() (*identity.Service, error) {
	if s.opts.Identity == nil {
		return nil, ErrNoSignIn.New()
	}
	return s.opts.Identity, nil
}
