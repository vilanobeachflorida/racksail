package api

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/vilanobeachflorida/racksail/internal/auth/identity"
	"github.com/vilanobeachflorida/racksail/internal/changeset"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

// An undo is judged by the changes it makes, as Undo builds them, rather
// than by the changes it undoes: putting back the settings a low-risk
// action changed is an update like any other.
func TestUndosAreJudgedByWhatTheyDo(t *testing.T) {
	s := newStack(t, RateLimit{})
	site, user, other := store.NewID("tsite"), store.NewID(identity.PrefixUser), store.NewID("tsite")
	before, after := json.RawMessage(`{"name":"Ada"}`), json.RawMessage(`{"name":"Ada L."}`)
	cs := &changeset.ChangeSet{Changes: []changeset.ResourceChange{
		{Op: changeset.OpCreate, Kind: siteKind.Name(), ID: site, After: after},
		{Op: changeset.OpUpdate, Kind: siteKind.Name(), ID: site, Before: after, After: before},
		{Op: changeset.OpAction, Kind: siteKind.Name(), ID: other, Action: "deploy", Before: before, After: before},
		{Op: changeset.OpAction, Kind: identity.KindUser, ID: user, Action: identity.ActionUpdateProfile, Before: before, After: after},
		{Op: changeset.OpDelete, Kind: siteKind.Name(), ID: other, Before: before},
	}}
	got := undoChanges(cs)
	want := []undoChange{
		{siteKind.Name(), changeset.OpDelete},
		{identity.KindUser, changeset.OpUpdate},
		{siteKind.Name(), changeset.OpCreate},
	}
	if !slices.Equal(got, want) {
		t.Errorf("undo changes = %+v, want %+v", got, want)
	}
	if !s.server.undoConfirms(cs) {
		t.Error("undoing a profile change puts back a user's settings, which asks for a confirmation")
	}
	if s.server.undoConfirms(&changeset.ChangeSet{Changes: cs.Changes[:3]}) {
		t.Error("undoing changes of a kind that doesn't ask needs none")
	}
	if risk := s.server.undoRisk(&changeset.ChangeSet{Changes: cs.Changes[3:4]}); risk != identity.UserKind.Info().Risks.Update {
		t.Errorf("undoing a profile change = %s, a user's update is %s", risk, identity.UserKind.Info().Risks.Update)
	}
}
