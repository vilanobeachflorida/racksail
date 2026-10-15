package agent

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/vilanobeachflorida/racksail/internal/system"
)

// EnsureGroupInput describes a group.
type EnsureGroupInput struct {
	Name string `json:"name" doc:"Group name"`
	// GID is fixed when not zero; zero lets the system choose.
	GID    int  `json:"gid,omitempty" doc:"Group ID; chosen by the system when zero"`
	System bool `json:"system,omitempty" doc:"Create a system group"`
	// Members replaces the member list when set.
	Members *[]string `json:"members,omitempty" doc:"The complete member list; left alone when absent"`
}

// Validate implements Input.
func (in EnsureGroupInput) Validate() error {
	if err := checkAccountName("group", in.Name); err != nil {
		return err
	}
	if slices.Contains(privilegedGroups, in.Name) {
		return fmt.Errorf("group %s is privileged and isn't managed by Racksail", in.Name)
	}
	if in.GID < 0 || in.GID > system.AccountUIDMax {
		return fmt.Errorf("gid %d is out of range", in.GID)
	}
	if in.Members != nil {
		for _, m := range *in.Members {
			if err := checkAccountName("user", m); err != nil {
				return err
			}
		}
	}
	return nil
}

// EnsureGroup creates a group or sets its members.
var EnsureGroup = Register(Def[EnsureGroupInput, Result]{
	Name:    "EnsureGroup",
	Summary: "Create a group",
	Effects: func(in EnsureGroupInput) []system.Effect {
		return []system.Effect{{Type: system.EffectUser, Action: "create-group", Target: in.Name}}
	},
	Run: func(ctx context.Context, env *Env, in EnsureGroupInput) (Result, error) {
		u := env.System.Users
		cur, err := u.LookupGroup(ctx, in.Name)
		if errors.Is(err, system.ErrNotFound) {
			effect := system.Effect{Type: system.EffectUser, Action: "create-group", Target: in.Name}
			if env.Check {
				return Changed(effect), nil
			}
			g := system.Group{Name: in.Name, GID: in.GID, System: in.System}
			if in.Members != nil {
				g.Members = *in.Members
			}
			return Changed(effect), u.CreateGroup(ctx, g)
		}
		if err != nil {
			return Result{}, err
		}
		if in.GID != 0 && cur.GID != in.GID {
			return Result{}, fmt.Errorf("group %s has gid %d, not %d: %w", in.Name, cur.GID, in.GID, system.ErrExists)
		}
		if in.Members == nil || sameSet(cur.Members, *in.Members) {
			return Unchanged(), nil
		}
		effect := system.Effect{Type: system.EffectUser, Action: "update-group", Target: in.Name}
		if env.Check {
			return Changed(effect), nil
		}
		cur.Members = *in.Members
		return Changed(effect), u.UpdateGroup(ctx, cur)
	},
})

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	x, y := slices.Clone(a), slices.Clone(b)
	slices.Sort(x)
	slices.Sort(y)
	return slices.Equal(x, y)
}

// EnsureUserInput describes a user.
type EnsureUserInput struct {
	Name string `json:"name" doc:"User name"`
	// UID is fixed when not zero. Accounts always pass their UID.
	UID     int             `json:"uid,omitempty" doc:"User ID; chosen by the system when zero"`
	GID     int             `json:"gid,omitempty" doc:"Primary group ID; a group named after the user when zero"`
	Groups  []string        `json:"groups,omitempty" doc:"Supplementary groups"`
	Home    string          `json:"home" doc:"Home directory, which isn't created"`
	Shell   string          `json:"shell,omitempty" doc:"Login shell; no login when empty"`
	Comment string          `json:"comment,omitempty"`
	System  bool            `json:"system,omitempty" doc:"Create a system account"`
	Locked  bool            `json:"locked,omitempty" doc:"Disable password sign-in"`
	SubIDs  *system.IDRange `json:"subIds,omitempty" doc:"Subordinate IDs for rootless containers"`
}

// Validate implements Input.
func (in EnsureUserInput) Validate() error {
	if err := checkAccountName("user", in.Name); err != nil {
		return err
	}
	switch {
	case in.UID == 0:
	case in.System && in.UID >= 100 && in.UID <= 999:
	case !in.System && in.UID >= system.AccountUIDMin && in.UID <= system.AccountUIDMax:
	default:
		return fmt.Errorf("uid %d is outside the system (100-999) and account (%d-%d) ranges", in.UID, system.AccountUIDMin, system.AccountUIDMax)
	}
	if in.GID < 0 || in.GID > system.AccountUIDMax {
		return fmt.Errorf("gid %d is out of range", in.GID)
	}
	for _, g := range in.Groups {
		if err := checkAccountName("group", g); err != nil {
			return err
		}
		if slices.Contains(privilegedGroups, g) {
			return fmt.Errorf("group %s is privileged; Racksail never adds users to it", g)
		}
	}
	if err := cleanAbs(in.Home); err != nil {
		return err
	}
	if in.Shell != "" && !slices.Contains(allowedShells, in.Shell) {
		return fmt.Errorf("shell %s isn't allowed", in.Shell)
	}
	if len(in.Comment) > 128 {
		return fmt.Errorf("comment is longer than 128 characters")
	}
	if in.SubIDs != nil && (in.SubIDs.Start < system.SubIDStart || in.SubIDs.Count <= 0 || in.SubIDs.Count > 1<<20) {
		return fmt.Errorf("subordinate ids must start at %d or above", system.SubIDStart)
	}
	return nil
}

func (in EnsureUserInput) user() system.User {
	shell := in.Shell
	if shell == "" {
		shell = "/usr/sbin/nologin"
	}
	return system.User{
		Name: in.Name, UID: in.UID, GID: in.GID, Groups: in.Groups, Home: in.Home, Shell: shell,
		Comment: in.Comment, System: in.System, Locked: in.Locked, SubIDs: in.SubIDs,
	}
}

// EnsureUser creates a user or updates its settings.
var EnsureUser = Register(Def[EnsureUserInput, Result]{
	Name:        "EnsureUser",
	Summary:     "Create a user",
	Description: "Creates a Linux user, or updates the shell, groups, comment, lock state and subordinate IDs of an existing one. User and group IDs never change.",
	Effects: func(in EnsureUserInput) []system.Effect {
		return []system.Effect{{Type: system.EffectUser, Action: "create", Target: in.Name}}
	},
	Run: func(ctx context.Context, env *Env, in EnsureUserInput) (Result, error) {
		u := env.System.Users
		want := in.user()
		cur, err := u.LookupUser(ctx, in.Name)
		if errors.Is(err, system.ErrNotFound) {
			effect := system.Effect{Type: system.EffectUser, Action: "create", Target: in.Name}
			if env.Check {
				return Changed(effect), nil
			}
			return Changed(effect), u.CreateUser(ctx, want)
		}
		if err != nil {
			return Result{}, err
		}
		if (in.UID != 0 && cur.UID != in.UID) || (in.GID != 0 && cur.GID != in.GID) {
			return Result{}, fmt.Errorf("user %s has ids %d:%d, not %d:%d: %w", in.Name, cur.UID, cur.GID, in.UID, in.GID, system.ErrExists)
		}
		same := cur.Shell == want.Shell && cur.Comment == want.Comment && cur.Locked == want.Locked &&
			cur.Home == want.Home && sameSet(cur.Groups, want.Groups) && sameRange(cur.SubIDs, want.SubIDs)
		if same {
			return Unchanged(), nil
		}
		effect := system.Effect{Type: system.EffectUser, Action: "update", Target: in.Name}
		if env.Check {
			return Changed(effect), nil
		}
		return Changed(effect), u.UpdateUser(ctx, want)
	},
})

func sameRange(a, b *system.IDRange) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// DeleteUserInput names a user to delete.
type DeleteUserInput struct {
	Name       string `json:"name" doc:"User name"`
	RemoveHome bool   `json:"removeHome,omitempty" doc:"Also delete the home directory"`
}

// Validate implements Input.
func (in DeleteUserInput) Validate() error { return checkAccountName("user", in.Name) }

// DeleteUser removes a user Racksail created. Only account users and
// racksail-* system users can be removed.
var DeleteUser = Register(Def[DeleteUserInput, Result]{
	Name:    "DeleteUser",
	Summary: "Delete a user",
	Effects: func(in DeleteUserInput) []system.Effect {
		return []system.Effect{{Type: system.EffectUser, Action: "delete", Target: in.Name, Destructive: in.RemoveHome}}
	},
	Run: func(ctx context.Context, env *Env, in DeleteUserInput) (Result, error) {
		u := env.System.Users
		cur, err := u.LookupUser(ctx, in.Name)
		if errors.Is(err, system.ErrNotFound) {
			return Unchanged(), nil
		}
		if err != nil {
			return Result{}, err
		}
		if !removableUser(cur) {
			return Result{}, ErrNotAllowed.New("op", "DeleteUser", "target", in.Name)
		}
		effect := system.Effect{Type: system.EffectUser, Action: "delete", Target: in.Name, Destructive: in.RemoveHome}
		if env.Check {
			return Changed(effect), nil
		}
		return Changed(effect), u.DeleteUser(ctx, in.Name, in.RemoveHome)
	},
})

func removableUser(u system.User) bool {
	if u.UID >= system.AccountUIDMin && u.UID <= system.AccountUIDMax {
		return true
	}
	return u.System && len(u.Name) > 9 && u.Name[:9] == "racksail-"
}

// NameInput names one thing.
type NameInput struct {
	Name string `json:"name" doc:"Name"`
}

// Validate implements Input.
func (in NameInput) Validate() error { return checkAccountName("name", in.Name) }

// DeleteGroup removes a group that no user has as its primary group.
var DeleteGroup = Register(Def[NameInput, Result]{
	Name:    "DeleteGroup",
	Summary: "Delete a group",
	Effects: func(in NameInput) []system.Effect {
		return []system.Effect{{Type: system.EffectUser, Action: "delete-group", Target: in.Name}}
	},
	Run: func(ctx context.Context, env *Env, in NameInput) (Result, error) {
		u := env.System.Users
		g, err := u.LookupGroup(ctx, in.Name)
		if errors.Is(err, system.ErrNotFound) {
			return Unchanged(), nil
		}
		if err != nil {
			return Result{}, err
		}
		if slices.Contains(privilegedGroups, g.Name) || (g.GID < system.AccountUIDMin && !(g.System && len(g.Name) > 9 && g.Name[:9] == "racksail-")) {
			return Result{}, ErrNotAllowed.New("op", "DeleteGroup", "target", in.Name)
		}
		effect := system.Effect{Type: system.EffectUser, Action: "delete-group", Target: in.Name}
		if env.Check {
			return Changed(effect), nil
		}
		return Changed(effect), u.DeleteGroup(ctx, in.Name)
	},
})

// UserLookup is the result of LookupUser.
type UserLookup struct {
	Exists bool        `json:"exists"`
	User   system.User `json:"user,omitzero"`
}

// LookupUser describes a user.
var LookupUser = Register(Def[NameInput, UserLookup]{
	Name:     "LookupUser",
	Summary:  "Describe a user",
	ReadOnly: true,
	Run: func(ctx context.Context, env *Env, in NameInput) (UserLookup, error) {
		u, err := env.System.Users.LookupUser(ctx, in.Name)
		if errors.Is(err, system.ErrNotFound) {
			return UserLookup{}, nil
		}
		return UserLookup{Exists: err == nil, User: u}, err
	},
})
