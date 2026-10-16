package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/danielgtaylor/huma/v2"

	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/auth/identity"
	"github.com/vilanobeachflorida/racksail/internal/changeset"
	"github.com/vilanobeachflorida/racksail/internal/hooks"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

// A change set is judged by what its plan does, change by change, whoever
// asked for each change and whatever they may read.
func TestPlansAreJudgedByWhatTheyDo(t *testing.T) {
	s := newStack(t, RateLimit{})
	user := func(username, email string) json.RawMessage {
		raw, _ := json.Marshal(identity.UserSpec{Username: username, Name: "Sam", Email: email})
		return raw
	}
	site := json.RawMessage(`{"domains":["a.test"],"content":"x"}`)
	profile := func(before, after json.RawMessage) changeset.ResourceChange {
		return changeset.ResourceChange{Op: changeset.OpAction, Kind: identity.KindUser, Action: identity.ActionUpdateProfile, Before: before, After: after}
	}
	for _, c := range []struct {
		name   string
		risk   store.Risk
		change changeset.ResourceChange
		want   bool
	}{
		{"a site's update", store.RiskMedium, changeset.ResourceChange{Op: changeset.OpUpdate, Kind: siteKind.Name(), Before: site, After: site}, false},
		{"a critical change", store.RiskCritical, changeset.ResourceChange{Op: changeset.OpDelete, Kind: siteKind.Name(), Before: site}, true},
		{"a new user", store.RiskHigh, changeset.ResourceChange{Op: changeset.OpCreate, Kind: identity.KindUser, After: user("sam", "")}, true},
		{"a user's update", store.RiskHigh, changeset.ResourceChange{Op: changeset.OpUpdate, Kind: identity.KindUser, Before: user("sam", ""), After: user("sam", "")}, true},
		{"an SSH key", store.RiskMedium, changeset.ResourceChange{Op: changeset.OpAction, Kind: identity.KindUser, Action: "add_ssh_key", Before: user("sam", ""), After: user("sam", "")}, true},
		{"a new name", store.RiskLow, profile(user("sam", "sam@example.com"), user("sam", "sam@example.com")), false},
		{"the address in capitals", store.RiskLow, profile(user("sam", "sam@example.com"), user("sam", "SAM@example.com")), false},
		{"a new address", store.RiskLow, profile(user("sam", "sam@example.com"), user("sam", "sam@example.net")), true},
		{"the address taken away", store.RiskLow, profile(user("sam", "sam@example.com"), user("sam", "")), true},
		{"an address that folds to another", store.RiskLow, profile(user("sam", "sam@example.com"), user("sam", "ſam@example.com")), true},
		{"settings that can't be read", store.RiskLow, profile(user("sam", "sam@example.com"), json.RawMessage(`[]`)), true},
	} {
		cs := &changeset.ChangeSet{Risk: c.risk, Changes: []changeset.ResourceChange{c.change}}
		if got := s.server.planConfirms(cs); got != c.want {
			t.Errorf("%s asks for a confirmation = %t, want %t", c.name, got, c.want)
		}
	}
}

// What a modify hook adds to a plan is judged with it, and an undo by the
// changes it makes: here a hook gives a user another address whenever an
// update leaves a site's content "moved", which no request asks for.
func TestWhatHooksAddIsConfirmedToo(t *testing.T) {
	s := newStack(t, RateLimit{})
	admin := s.setUp()
	ctx := context.Background()
	acct := s.openAccount(admin, "acme")
	home := map[string]any{"level": "account", "id": acct}
	admin.do("POST", "/api/v1/session/step-up", map[string]any{"password": testPassword}, nil)
	resp := admin.do("POST", "/api/v1/core/users", map[string]any{"scope": home, "spec": map[string]any{
		"username": "bea", "email": "bea@example.com", "grants": []map[string]any{{"role": "readonly", "scope": home}},
	}}, nil)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("adding bea = %d %s", resp.StatusCode, bodyOf(resp))
	}
	bea := s.finish("admin", changeSetOf(t, resp)).Changes[0].ID
	if err := s.hooks.Register(hooks.Registration{
		Point: hooks.KindPoint(siteKind.Name(), "update", changeset.StagePlan), Kind: hooks.Modify, Owner: "addon:mover",
		OnModify: func(ctx context.Context, c *hooks.Context) error {
			var spec siteSpec
			if err := json.Unmarshal(c.ChangeSet.Changes[c.Changes[0]].After, &spec); err != nil || spec.Content != "moved" {
				return err
			}
			u, err := identity.UserKind.Get(ctx, s.store, bea)
			if err != nil {
				return err
			}
			next := u.Spec
			next.Email = "bea@mover.example"
			raw, err := json.Marshal(next)
			if err != nil {
				return err
			}
			c.ChangeSet.Changes = append(c.ChangeSet.Changes, changeset.ResourceChange{Op: changeset.OpUpdate, Kind: identity.KindUser, ID: bea, After: raw})
			return nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	address := func() string {
		t.Helper()
		u, err := identity.UserKind.Get(ctx, s.store, bea)
		if err != nil {
			t.Fatal(err)
		}
		return u.Spec.Email
	}

	// Ada again, in a browser where she hasn't confirmed it's her.
	b := s.browser()
	b.do("POST", "/api/v1/auth/sign-in", map[string]any{"username": "ada", "password": testPassword}, nil)
	resp = b.do("POST", "/api/v1/test/sites", map[string]any{"scope": home, "spec": map[string]any{"domains": []string{"a.test"}, "content": "moved"}}, nil)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("creating a site = %d %s", resp.StatusCode, bodyOf(resp))
	}
	site := s.finish("admin", changeSetOf(t, resp)).Changes[0].ID
	path := "/api/v1/test/sites/" + string(site)
	move := func(domain, content string) map[string]any {
		return map[string]any{"spec": map[string]any{"domains": []string{domain}, "content": content}}
	}
	asks := func(what string, resp *http.Response) {
		t.Helper()
		if resp.StatusCode != http.StatusForbidden || s.problem(resp) != auth.ErrStepUpRequired.Code {
			t.Errorf("%s = %d %s", what, resp.StatusCode, bodyOf(resp))
		}
		if got := address(); got != "bea@example.com" {
			t.Fatalf("after %s, bea's address is %s", what, got)
		}
	}
	asks("giving the site another domain", b.do("PUT", path, move("b.test", "moved"), nil))
	resp = b.do("PUT", path+"?dryRun=true", move("b.test", "moved"), nil)
	if resp.StatusCode != http.StatusOK || len(changeSetOf(t, resp).Changes) != 2 {
		t.Fatalf("a preview shows what the hook adds = %d %s", resp.StatusCode, bodyOf(resp))
	}
	asks("applying its preview", b.do("POST", "/api/v1/changesets/"+string(changeSetOf(t, resp).ID)+"/apply", nil, nil))

	// Other content needs nothing, but undoing that change leaves the
	// content "moved" again.
	resp = b.do("PUT", path, move("a.test", "stays"), nil)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("changing the content = %d %s", resp.StatusCode, bodyOf(resp))
	}
	changed := s.finish("admin", changeSetOf(t, resp))
	asks("undoing it", b.do("POST", "/api/v1/changesets/"+string(changed.ID)+"/undo", nil, nil))

	b.do("POST", "/api/v1/session/step-up", map[string]any{"password": testPassword}, nil)
	resp = b.do("POST", "/api/v1/changesets/"+string(changed.ID)+"/undo", nil, nil)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("undoing it once confirmed = %d %s", resp.StatusCode, bodyOf(resp))
	}
	s.finish("admin", changeSetOf(t, resp))
	if got := address(); got != "bea@mover.example" {
		t.Errorf("once confirmed, bea's address is %s", got)
	}
}

type addUserInput struct {
	ChangeParams
}

// Operations modules and the extension system add answer through Submit
// too, which asks for what their changes need confirming, as when a
// catalog's recipe adds a user.
func TestSubmitAsksWhicheverOperationSends(t *testing.T) {
	s := newStack(t, RateLimit{})
	admin := s.setUp()
	acct := s.openAccount(admin, "acme")
	op := huma.Operation{OperationID: "addTestUser", Method: http.MethodPost, Path: Prefix + "/test/users", Security: Protected}
	Describe(&op, Info{Risk: store.RiskLow, DryRun: true})
	huma.Register(s.server.API(), op, func(ctx context.Context, in *addUserInput) (*ChangeSetOutput, error) {
		spec, _ := json.Marshal(identity.UserSpec{Username: "bea", Grants: []auth.Grant{{Role: auth.RoleReadOnly, Scope: store.AccountScope(acct)}}})
		return Submit(ctx, s.server.pipeline(), in.ChangeParams, changeset.Request{Changes: []changeset.Change{{
			Op: changeset.OpCreate, Kind: identity.KindUser, Scope: store.AccountScope(acct), Spec: spec,
		}}})
	})
	if resp := admin.do("POST", Prefix+"/test/users", nil, nil); s.problem(resp) != auth.ErrStepUpRequired.Code {
		t.Errorf("adding a user without confirming = %d %s", resp.StatusCode, bodyOf(resp))
	}
	if resp := admin.do("POST", Prefix+"/test/users?dryRun=true", nil, nil); resp.StatusCode != http.StatusOK {
		t.Errorf("a preview = %d %s", resp.StatusCode, bodyOf(resp))
	}
	admin.do("POST", "/api/v1/session/step-up", map[string]any{"password": testPassword}, nil)
	if resp := admin.do("POST", Prefix+"/test/users", nil, nil); resp.StatusCode != http.StatusAccepted {
		t.Errorf("once confirmed = %d %s", resp.StatusCode, bodyOf(resp))
	}
}
