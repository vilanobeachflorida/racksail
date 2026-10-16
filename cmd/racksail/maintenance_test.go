package main

import (
	"errors"
	"os/user"
	"strings"
	"testing"
)

// The console hands the safe mode flag it wrote as root to the panel's
// user, and says so when it can't, so the panel can always read why safe
// mode is on.
func TestHandToPanel(t *testing.T) {
	panel := func(name string) (*user.User, error) {
		if name != "racksail" {
			return nil, user.UnknownUserError(name)
		}
		return &user.User{Username: "racksail", Uid: "998", Gid: "997"}, nil
	}
	var chowned []int
	chown := func(path string, uid, gid int) error {
		chowned = append(chowned, uid, gid)
		return nil
	}
	if err := handToPanel("/var/lib/racksail/safe-mode", 0, panel, chown); err != nil || len(chowned) != 2 || chowned[0] != 998 || chowned[1] != 997 {
		t.Errorf("as root: %v, %v", chowned, err)
	}
	chowned = nil
	if err := handToPanel("/var/lib/racksail/safe-mode", 1000, panel, chown); err != nil || chowned != nil {
		t.Errorf("as someone else, nothing is handed over: %v, %v", chowned, err)
	}
	none := func(name string) (*user.User, error) { return nil, user.UnknownUserError(name) }
	if err := handToPanel("/tmp/safe-mode", 0, none, chown); err != nil || chowned != nil {
		t.Errorf("without the panel's user: %v, %v", chowned, err)
	}
	for name, c := range map[string]struct {
		lookup func(string) (*user.User, error)
		chown  func(string, int, int) error
		want   string
	}{
		"a lookup that fails": {func(string) (*user.User, error) { return nil, errors.New("nsswitch is broken") }, chown, "nsswitch is broken"},
		"a bad user ID":       {func(string) (*user.User, error) { return &user.User{Uid: "x", Gid: "1"}, nil }, chown, "isn't a number"},
		"a chown that fails":  {panel, func(string, int, int) error { return errors.New("read-only file system") }, "read-only file system"},
	} {
		if err := handToPanel("/var/lib/racksail/safe-mode", 0, c.lookup, c.chown); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
}
