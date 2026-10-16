package auth

import (
	"context"
	"fmt"
	"net/url"
)

// Caller calls a method of a capability that runs somewhere else, such as
// in an add-on's own process over its socket: it sends in and decodes the
// answer into out. A provider whose Open returns a Caller rather than one
// of the identity interfaces is reached through RemoteIdentityProvider or
// RemoteMFAMethod, which speak the methods below.
type Caller interface {
	Call(ctx context.Context, method string, in, out any) error
}

// The methods of the auth.provider and auth.mfa capabilities, as add-ons
// answer them at POST /provide/<capability>/<method>.
const (
	// MethodDescribe takes nothing and answers with a ProviderDescription.
	MethodDescribe = "describe"
	// MethodCheckPassword takes a PasswordCheck and answers with an
	// IdentityAnswer.
	MethodCheckPassword = "check-password"
	// MethodBegin takes a RedirectStart and answers with a RedirectBegun.
	MethodBegin = "begin"
	// MethodComplete takes a RedirectReturn and answers with an
	// IdentityAnswer.
	MethodComplete = "complete"
	// MethodChallenge takes an MFACheck and answers with an MFAQuestion.
	MethodChallenge = "challenge"
	// MethodVerify takes an MFAResponse and answers with an MFAResult.
	MethodVerify = "verify"
)

// ProviderDescription says how an identity provider signs people in.
type ProviderDescription struct {
	Method ProviderMethod `json:"method" enum:"password,redirect" doc:"password for a username and password typed on the panel's screen, redirect for the provider's own page"`
}

// PasswordCheck asks a password provider whose credentials these are.
type PasswordCheck struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// IdentityAnswer is who an identity provider says signed in, or that it
// refused them.
type IdentityAnswer struct {
	Identity *ExternalIdentity `json:"identity,omitempty"`
	// Refused says the credentials were wrong, or the person turned the
	// sign-in down at the provider; Reason says which, for people.
	Refused bool   `json:"refused,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

// RedirectStart asks a redirect provider where to send the browser.
type RedirectStart struct {
	// Callback is where the provider sends the browser back to.
	Callback string `json:"callback"`
}

// RedirectBegun is where to send the browser, and what the panel keeps
// until it comes back.
type RedirectBegun struct {
	Redirect string `json:"redirect"`
	State    []byte `json:"state"`
}

// RedirectReturn finishes a sign-in at a redirect provider with what the
// browser came back with.
type RedirectReturn struct {
	State    []byte     `json:"state"`
	Callback string     `json:"callback"`
	Query    url.Values `json:"query"`
}

// MFACheck starts a check with a second-factor method.
type MFACheck struct {
	User MFAUser `json:"user"`
}

// MFAQuestion is what a second-factor method asks of the person, and what
// the panel keeps until they answer.
type MFAQuestion struct {
	Prompt string `json:"prompt"`
	Input  bool   `json:"input"`
	State  []byte `json:"state,omitempty"`
}

// MFAResponse is the person's answer to an MFAQuestion.
type MFAResponse struct {
	User     MFAUser `json:"user"`
	State    []byte  `json:"state,omitempty"`
	Response string  `json:"response,omitempty"`
}

// MFAResult is how a check with a second-factor method went: passed,
// pending while the person hasn't answered, or failed.
type MFAResult struct {
	Result string `json:"result" enum:"passed,pending,failed"`
}

// Results of MFAResult.
const (
	MFAPassed  = "passed"
	MFAPending = "pending"
	MFAFailed  = "failed"
)

// RemoteIdentityProvider asks a Caller how it signs people in and returns
// it as the PasswordProvider or RedirectProvider that is.
func RemoteIdentityProvider(ctx context.Context, c Caller) (IdentityProvider, error) {
	var d ProviderDescription
	if err := c.Call(ctx, MethodDescribe, struct{}{}, &d); err != nil {
		return nil, err
	}
	switch d.Method {
	case MethodPassword:
		return remotePassword{c}, nil
	case MethodRedirect:
		return remoteRedirect{c}, nil
	}
	return nil, fmt.Errorf("auth: the provider signs people in with %q, neither password nor redirect", d.Method)
}

type remotePassword struct{ c Caller }

func (remotePassword) Method() ProviderMethod { return MethodPassword }

func (p remotePassword) CheckPassword(ctx context.Context, username, password string) (*ExternalIdentity, error) {
	var a IdentityAnswer
	if err := p.c.Call(ctx, MethodCheckPassword, PasswordCheck{Username: username, Password: password}, &a); err != nil {
		return nil, err
	}
	switch {
	case a.Refused:
		return nil, ErrBadCredentials.New()
	case a.Identity == nil:
		return nil, fmt.Errorf("auth: the provider said neither who signed in nor that it refused them")
	}
	return a.Identity, nil
}

type remoteRedirect struct{ c Caller }

func (remoteRedirect) Method() ProviderMethod { return MethodRedirect }

func (p remoteRedirect) Begin(ctx context.Context, callback string) (string, []byte, error) {
	var b RedirectBegun
	if err := p.c.Call(ctx, MethodBegin, RedirectStart{Callback: callback}, &b); err != nil {
		return "", nil, err
	}
	return b.Redirect, b.State, nil
}

func (p remoteRedirect) Complete(ctx context.Context, state []byte, callback string, query url.Values) (*ExternalIdentity, error) {
	var a IdentityAnswer
	if err := p.c.Call(ctx, MethodComplete, RedirectReturn{State: state, Callback: callback, Query: query}, &a); err != nil {
		return nil, err
	}
	switch {
	case a.Refused:
		reason := a.Reason
		if reason == "" {
			reason = "the provider turned it down"
		}
		return nil, ErrProviderRefused.New("reason", reason)
	case a.Identity == nil:
		return nil, fmt.Errorf("auth: the provider said neither who signed in nor that it refused them")
	}
	return a.Identity, nil
}

// RemoteMFAMethod returns a Caller as a second-factor method.
func RemoteMFAMethod(c Caller) MFAMethod { return remoteMFA{c} }

type remoteMFA struct{ c Caller }

func (m remoteMFA) Challenge(ctx context.Context, user MFAUser) (*MFAChallenge, error) {
	var q MFAQuestion
	if err := m.c.Call(ctx, MethodChallenge, MFACheck{User: user}, &q); err != nil {
		return nil, err
	}
	return &MFAChallenge{Prompt: q.Prompt, Input: q.Input, State: q.State}, nil
}

func (m remoteMFA) Verify(ctx context.Context, user MFAUser, state []byte, response string) error {
	var r MFAResult
	if err := m.c.Call(ctx, MethodVerify, MFAResponse{User: user, State: state, Response: response}, &r); err != nil {
		return err
	}
	switch r.Result {
	case MFAPassed:
		return nil
	case MFAPending:
		return ErrPending.New()
	case MFAFailed:
		return ErrBadCode.New()
	}
	return fmt.Errorf("auth: the second step answered %q, not passed, pending or failed", r.Result)
}
