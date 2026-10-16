package api

import (
	"net"
	"net/http"
	"net/netip"
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

// clientOf works out where a request comes from. The edge serves the panel
// under its hostname by proxying to it over loopback, and tells it the
// browser's address in X-Forwarded-For and whether it used HTTPS in
// X-Forwarded-Proto; those are believed only from loopback, where account
// workloads can't connect from (they're denied the panel's address).
func clientOf(r *http.Request) auth.Client {
	c := auth.Client{IP: hostOf(r.RemoteAddr), UserAgent: r.UserAgent(), Secure: r.TLS != nil}
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

func hostOf(addr string) string {
	if host, _, err := net.SplitHostPort(addr); err == nil {
		return host
	}
	return addr
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
	if err := s.stepUp(actor, op, ctx.Query("dryRun") == "true"); err != nil {
		p := httpapi.FromError(err)
		huma.WriteErr(s.api, ctx, p.Status, p.Detail, err)
		return
	}
	next(ctx)
}

// stepUp says whether an operation needs the actor to confirm it's them
// first: those only people in the panel use refuse API tokens, and those
// marked as needing it, or critical, need a recent confirmation from people
// in the panel. Previews change nothing, so they never need it.
func (s *Server) stepUp(actor *auth.Actor, op *huma.Operation, dryRun bool) error {
	if session, _ := op.Extensions[ExtSession].(bool); session && actor.Type != auth.ActorUser {
		return auth.ErrPanelOnly.New()
	}
	id := s.opts.Identity
	if id == nil || dryRun {
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

// requestConfirms reports whether a request's changes ask for a
// confirmation.
func (s *Server) requestConfirms(changes []changeset.Change) bool {
	for _, c := range changes {
		if s.confirms(c.Kind, c.Op, c.Action) {
			return true
		}
	}
	return false
}

// changeSetConfirms reports whether applying or undoing a change set asks
// for a confirmation.
func (s *Server) changeSetConfirms(cs *changeset.ChangeSet) bool {
	for _, c := range cs.Changes {
		if s.confirms(c.Kind, c.Op, c.Action) {
			return true
		}
	}
	return false
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

// undoRisk is the risk of undoing a change set: its creates become deletes
// and its deletes creates.
func (s *Server) undoRisk(cs *changeset.ChangeSet) store.Risk {
	risk := store.RiskRead
	for _, c := range cs.Changes {
		k, ok := s.kinds.Get(c.Kind)
		if !ok {
			continue
		}
		op := c.Op
		switch op {
		case changeset.OpCreate:
			op = changeset.OpDelete
		case changeset.OpDelete:
			op = changeset.OpCreate
		}
		risk = store.MaxRisk(risk, opRisk(k.Info(), op, c.Action))
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
