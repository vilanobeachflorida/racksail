package auth

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

type owners map[store.ID]store.ID

func (o owners) ResellerOf(ctx context.Context, account store.ID) (store.ID, error) {
	return o[account], nil
}

func TestAuthorize(t *testing.T) {
	ctx := context.Background()
	reseller, acctA, acctB, site := store.NewID("rsl"), store.NewID("acct"), store.NewID("acct"), store.NewID("site")
	az := NewRoleAuthorizer(owners{acctA: reseller})

	admin := &Actor{Type: ActorUser, Grants: []Grant{{Role: RoleAdmin, Scope: store.ServerScope()}}}
	res := &Actor{Type: ActorUser, Grants: []Grant{{Role: RoleReseller, Scope: store.ResellerScope(reseller)}}}
	owner := &Actor{Type: ActorUser, Grants: []Grant{{Role: RoleOwner, Scope: store.AccountScope(acctA)}}}
	viewer := &Actor{Type: ActorUser, Grants: []Grant{{Role: RoleReadOnly, Scope: store.AccountScope(acctA)}}}
	collab := &Actor{Type: ActorUser, Grants: []Grant{{Role: RoleCollaborator, Scope: store.AccountScope(acctA), Object: site, Permissions: []Permission{"web.site.deploy", "web.site.read"}}}}

	tests := []struct {
		name   string
		actor  *Actor
		perm   Permission
		target Target
		allow  bool
	}{
		{"admin anything", admin, "web.site.delete", Target{Scope: store.AccountScope(acctB)}, true},
		{"admin lacks override", admin, PermOverride, Target{Scope: store.ServerScope()}, false},
		{"reseller own account", res, "web.site.create", Target{Scope: store.AccountScope(acctA)}, true},
		{"reseller other account", res, "web.site.create", Target{Scope: store.AccountScope(acctB)}, false},
		{"reseller server kinds", res, "storage.pool.create", Target{Scope: store.AccountScope(acctA)}, false},
		{"owner own account", owner, "mail.mailbox.create", Target{Scope: store.AccountScope(acctA)}, true},
		{"owner other account", owner, "mail.mailbox.read", Target{Scope: store.AccountScope(acctB)}, false},
		{"owner server scope", owner, "web.site.read", Target{Scope: store.ServerScope()}, false},
		{"viewer reads", viewer, "web.site.read", Target{Scope: store.AccountScope(acctA)}, true},
		{"viewer writes", viewer, "web.site.update", Target{Scope: store.AccountScope(acctA)}, false},
		{"collaborator granted object", collab, "web.site.deploy", Target{Scope: store.AccountScope(acctA), Object: site}, true},
		{"collaborator other object", collab, "web.site.deploy", Target{Scope: store.AccountScope(acctA), Object: store.NewID("site")}, false},
		{"collaborator ungranted perm", collab, "web.site.delete", Target{Scope: store.AccountScope(acctA), Object: site}, false},
		{"nobody", nil, "web.site.read", Target{Scope: store.ServerScope()}, false},
	}
	for _, tt := range tests {
		err := az.Authorize(ctx, tt.actor, tt.perm, tt.target)
		if (err == nil) != tt.allow {
			t.Errorf("%s: err = %v, want allowed %v", tt.name, err, tt.allow)
		}
		if err != nil && tt.actor != nil && !errors.Is(err, i18n.ErrForbidden) {
			t.Errorf("%s: denial should be core.forbidden, got %v", tt.name, err)
		}
	}
	overrider := &Actor{Grants: []Grant{{Role: RoleAdmin, Scope: store.ServerScope(), Permissions: []Permission{PermOverride}}}}
	if err := az.Authorize(ctx, overrider, PermOverride, Target{Scope: store.ServerScope()}); err != nil {
		t.Errorf("an explicit grant of core.override should allow it: %v", err)
	}
}

func TestActor(t *testing.T) {
	user, token := store.NewID("user"), store.NewID("tok")
	a := &Actor{Type: ActorToken, ID: token, User: user, Source: SourceCLI}
	if got := a.String(); got != "token:"+string(token)+" for user:"+string(user)+" via cli" {
		t.Errorf("String() = %q", got)
	}
	ctx := WithActor(context.Background(), a)
	if got, ok := ActorFrom(ctx); !ok || got != a {
		t.Error("the actor should travel in the context")
	}
	if _, ok := ActorFrom(context.Background()); ok {
		t.Error("an empty context has no actor")
	}
	now := time.Now()
	a.SteppedUpAt = now.Add(-2 * time.Minute)
	if !a.SteppedUpWithin(5*time.Minute, now) || a.SteppedUpWithin(time.Minute, now) {
		t.Error("step-up window is wrong")
	}
	if System("reconciler").String() != "system:reconciler" {
		t.Errorf("system actor = %q", System("reconciler"))
	}
}

func TestAuthenticators(t *testing.T) {
	req := httptest.NewRequest("GET", "/api/v1/health", nil)
	a, err := DevAuthenticator{}.Authenticate(context.Background(), req)
	if err != nil || a == nil || a.Grants[0].Role != RoleAdmin {
		t.Errorf("dev = %+v, %v", a, err)
	}
	if a, err := (NoAuthenticator{}).Authenticate(context.Background(), req); a != nil || err != nil {
		t.Error("NoAuthenticator finds nobody")
	}
}
