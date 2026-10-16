package notify

import (
	"context"
	"errors"
	"testing"

	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

// users is the panel's users as the identity service describes them.
type users map[store.ID]auth.DirectoryUser

func (u users) User(ctx context.Context, id store.ID) (auth.DirectoryUser, error) {
	if id == "user_broken" {
		return auth.DirectoryUser{}, errors.New("the store is down")
	}
	found, ok := u[id]
	if !ok {
		return auth.DirectoryUser{}, store.ErrNotFound.New("what", string(id))
	}
	return found, nil
}

func (u users) Members(ctx context.Context, account store.ID) ([]auth.DirectoryUser, error) {
	return nil, nil
}

func (u users) SSHKeys(ctx context.Context, user store.ID) ([]auth.SSHKey, error) { return nil, nil }

// People are the panel's users, with their grants, until they're turned
// off or gone; before the panel has users, nobody is found.
func TestUsersAsPeople(t *testing.T) {
	ctx := context.Background()
	owner := auth.Grant{Role: auth.RoleOwner, Scope: store.AccountScope("acct_1")}
	var dir auth.Directory
	people := Users(func() auth.Directory { return dir })
	if p, err := people.Person(ctx, "user_ada"); p != nil || err != nil {
		t.Errorf("before the panel has users = %+v, %v", p, err)
	}
	dir = users{
		"user_ada": {ID: "user_ada", Username: "ada", Name: "Ada Lovelace", Email: "ada@example.com", Grants: []auth.Grant{owner}},
		"user_bob": {ID: "user_bob", Username: "bob", Email: "bob@example.com"},
		"user_eve": {ID: "user_eve", Username: "eve", Email: "eve@example.com", Disabled: true},
	}
	ada, err := people.Person(ctx, "user_ada")
	if err != nil {
		t.Fatal(err)
	}
	if ada.ID != "user_ada" || ada.Name != "Ada Lovelace" || ada.Email != "ada@example.com" || ada.Actor == nil ||
		ada.Actor.Type != auth.ActorUser || ada.Actor.User != "user_ada" || len(ada.Actor.Grants) != 1 || ada.Actor.Grants[0].Role != auth.RoleOwner {
		t.Errorf("ada = %+v, actor %+v", ada, ada.Actor)
	}
	if bob, _ := people.Person(ctx, "user_bob"); bob == nil || bob.Name != "bob" {
		t.Errorf("someone without a name goes by their username: %+v", bob)
	}
	for _, id := range []store.ID{"user_eve", "user_gone"} {
		if p, err := people.Person(ctx, id); p != nil || err != nil {
			t.Errorf("%s = %+v, %v", id, p, err)
		}
	}
	if _, err := people.Person(ctx, "user_broken"); err == nil {
		t.Error("a store that fails isn't someone who is gone")
	}
}
