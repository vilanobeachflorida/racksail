package agent

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

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

// EnsureGroup creates a group or sets its members. Racksail only touches
// groups it owns: account groups and racksail-* system groups. A create is
// held to an account group so the panel can't claim a system GID, and the
// member list of a group Racksail doesn't own is never rewritten, since
// that would let an account into a group such as ssl-cert.
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
			if err := checkCreatableGroup(in, env.Check); err != nil {
				return Result{}, err
			}
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
		if !ownedGroup(cur) {
			return Result{}, ErrNotAllowed.New("op", "EnsureGroup", "target", in.Name)
		}
		effect := system.Effect{Type: system.EffectUser, Action: "update-group", Target: in.Name}
		if env.Check {
			return Changed(effect), nil
		}
		cur.Members = *in.Members
		return Changed(effect), u.UpdateGroup(ctx, cur)
	},
})

// checkCreatableGroup holds a group created over a free name to what Racksail
// can manage afterwards, the rule DeleteGroup and member changes already use:
// a system group must be racksail-*, and an account group must take an
// account-range GID and a name no user or group of the system or its
// packages has or may get, which a package would adopt with whoever Racksail
// put in it. An account's GID is allocated at apply time, so a preview still
// carrying the zero placeholder is let through.
func checkCreatableGroup(in EnsureGroupInput, check bool) error {
	if in.System {
		if !strings.HasPrefix(in.Name, "racksail-") {
			return fmt.Errorf("a system group Racksail creates must be named racksail-*, not %s: %w", in.Name, system.ErrInvalid)
		}
		// The system chooses a GID for racksail-* groups; a fixed one has to be
		// an account's.
		if in.GID != 0 && (in.GID < system.AccountUIDMin || in.GID > system.AccountUIDMax) {
			return fmt.Errorf("gid %d is outside the account range, so Racksail won't create group %s with it: %w", in.GID, in.Name, system.ErrInvalid)
		}
		return nil
	}
	if ReservedName(in.Name) {
		return fmt.Errorf("%s is the name of a user or group the system uses, so no account group takes it: %w", in.Name, system.ErrInvalid)
	}
	if check && in.GID == 0 {
		return nil
	}
	if in.GID < system.AccountUIDMin || in.GID > system.AccountUIDMax {
		return fmt.Errorf("an account group's gid must be in the account range %d-%d, not %d: %w", system.AccountUIDMin, system.AccountUIDMax, in.GID, system.ErrInvalid)
	}
	return nil
}

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
	// A primary group is the user's own: never one of the system's, such as
	// disk, which a fixed ID could otherwise name.
	switch {
	case in.GID == 0:
	case in.System && in.GID >= 100 && in.GID <= 999:
	case !in.System && in.GID >= system.AccountUIDMin && in.GID <= system.AccountUIDMax:
	default:
		return fmt.Errorf("gid %d is outside the system (100-999) and account (%d-%d) ranges", in.GID, system.AccountUIDMin, system.AccountUIDMax)
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
	// Where the home may be is checked against who the user is (an account or
	// a racksail-* system user) in the create and managed-update paths, where
	// a no-op on a user Racksail doesn't manage, such as root, isn't refused
	// for its home.
	if in.Shell != "" && !slices.Contains(allowedShells, in.Shell) {
		return fmt.Errorf("shell %s isn't allowed", in.Shell)
	}
	if len(in.Comment) > 128 {
		return fmt.Errorf("comment is longer than 128 characters")
	}
	// Subordinate IDs are an account's own range (decision D3), worked out
	// from its UID; an update that leaves the UID out is checked against the
	// user's.
	if in.SubIDs != nil {
		if in.System {
			return fmt.Errorf("only accounts get subordinate ids, not system user %s", in.Name)
		}
		if in.UID != 0 {
			if err := checkSubIDs(in.Name, in.UID, *in.SubIDs); err != nil {
				return err
			}
		}
	}
	return nil
}

// checkSubIDs holds an account's subordinate IDs to its own range, so it
// can't be given another account's or overlap anyone else's.
func checkSubIDs(name string, uid int, r system.IDRange) error {
	if want := system.AccountSubIDs(uid); r != want {
		return fmt.Errorf("account %s's subordinate ids are %d-%d, not %d-%d", name,
			want.Start, want.Start+want.Count-1, r.Start, r.Start+r.Count-1)
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

// EnsureUser creates a user or updates its settings. It only ever changes a
// user Racksail manages: an account or a racksail-* system user. A create
// over an existing name that isn't one of those, such as root or an
// administrator's login user, is refused, so a compromised panel can't
// rewrite another user's home, shell, groups or lock state. What a user may
// be given follows from who it is on the machine, never from the request:
// an account keeps its home in its own directory and its own subordinate
// IDs, and a request that says otherwise about whether the user is a system
// user is refused.
var EnsureUser = Register(Def[EnsureUserInput, Result]{
	Name:    "EnsureUser",
	Summary: "Create a user",
	Description: "Creates a Linux account or racksail-* system user, or updates the home, shell, groups, comment, lock state and " +
		"subordinate IDs of one. It refuses to change any other user, and user and group IDs never change, nor does whether " +
		"the user is a system user. An account's home is its own directory under " + system.AccountsDir + ", its subordinate " +
		"IDs are its own range, and it can't take the name of a privileged group or of a user or group the system or its " +
		"packages use.",
	Effects: func(in EnsureUserInput) []system.Effect {
		return []system.Effect{{Type: system.EffectUser, Action: "create", Target: in.Name}}
	},
	Run: func(ctx context.Context, env *Env, in EnsureUserInput) (Result, error) {
		u := env.System.Users
		want := in.user()
		cur, err := u.LookupUser(ctx, in.Name)
		if errors.Is(err, system.ErrNotFound) {
			effect := system.Effect{Type: system.EffectUser, Action: "create", Target: in.Name}
			if err := checkCreatableUser(in, env.Check); err != nil {
				return Result{}, err
			}
			if err := checkUserHome(!in.System, in.Name, in.Home); err != nil {
				return Result{}, err
			}
			if in.SubIDs != nil && in.UID == 0 {
				return Result{}, fmt.Errorf("account %s's subordinate ids follow from its uid, which isn't chosen yet: %w", in.Name, system.ErrInvalid)
			}
			if in.GID != 0 {
				if err := checkPrimaryGroup(ctx, u, in.GID); err != nil {
					return Result{}, err
				}
			}
			if err := checkSupplementaryGroups(ctx, u, in.Groups, env.Check); err != nil {
				return Result{}, err
			}
			if env.Check {
				return Changed(effect), nil
			}
			if err := u.CreateUser(ctx, want); err != nil {
				return Result{}, err
			}
			if env.Runtime != nil && in.UID != 0 {
				env.Runtime.release(ctx, in.UID)
			}
			return Changed(effect), nil
		}
		if err != nil {
			return Result{}, err
		}
		if (in.UID != 0 && cur.UID != in.UID) || (in.GID != 0 && cur.GID != in.GID) {
			return Result{}, fmt.Errorf("user %s has ids %d:%d, not %d:%d: %w", in.Name, cur.UID, cur.GID, in.UID, in.GID, system.ErrExists)
		}
		// Which rules hold comes from the user as the machine has it, so a
		// request can't choose a system user's home for an account, or the
		// other way round, by what it says about the user.
		account := accountUser(cur)
		if managedUser(cur) && in.System == account {
			kind := "a system user"
			if account {
				kind = "an account"
			}
			return Result{}, fmt.Errorf("user %s is %s, which doesn't change: %w", in.Name, kind, system.ErrExists)
		}
		same := cur.Shell == want.Shell && cur.Comment == want.Comment && cur.Locked == want.Locked &&
			cur.Home == want.Home && sameSet(cur.Groups, want.Groups) && sameRange(cur.SubIDs, want.SubIDs)
		if same {
			return Unchanged(), nil
		}
		// The user exists and a change is wanted: it has to be one Racksail
		// manages, never root or an administrator's own login user.
		if !managedUser(cur) {
			return Result{}, ErrNotAllowed.New("op", "EnsureUser", "target", in.Name)
		}
		if err := checkUserHome(account, in.Name, want.Home); err != nil {
			return Result{}, err
		}
		if want.SubIDs != nil {
			if !account {
				return Result{}, fmt.Errorf("only accounts get subordinate ids, not system user %s: %w", in.Name, system.ErrInvalid)
			}
			if err := checkSubIDs(in.Name, cur.UID, *want.SubIDs); err != nil {
				return Result{}, fmt.Errorf("%w: %w", err, system.ErrInvalid)
			}
		}
		if err := checkSupplementaryGroups(ctx, u, want.Groups, env.Check); err != nil {
			return Result{}, err
		}
		effect := system.Effect{Type: system.EffectUser, Action: "update", Target: in.Name}
		if env.Check {
			return Changed(effect), nil
		}
		return Changed(effect), u.UpdateUser(ctx, want)
	},
})

// checkSupplementaryGroups holds the groups an account is added to an
// allowlist: account groups and racksail-* groups, never a privileged or a
// system group such as ssl-cert that reads keys. A group an earlier step of
// the plan creates isn't on the machine yet during a preview, so a missing
// group passes in check mode.
func checkSupplementaryGroups(ctx context.Context, u system.Users, names []string, check bool) error {
	for _, name := range names {
		g, err := u.LookupGroup(ctx, name)
		switch {
		case errors.Is(err, system.ErrNotFound) && check:
		case err != nil:
			return fmt.Errorf("supplementary group %s: %w", name, err)
		case !ownedGroup(g):
			return fmt.Errorf("group %s isn't an account's or one of Racksail's, so Racksail won't add a user to it: %w", name, system.ErrInvalid)
		}
	}
	return nil
}

// checkPrimaryGroup holds a fixed primary group to the same allowlist as the
// supplementary ones: an account group or a racksail-* group, never a
// privileged or an unrelated system group such as ssl-cert, which a fixed GID
// could otherwise name to give a racksail-* user the right to read keys.
func checkPrimaryGroup(ctx context.Context, u system.Users, gid int) error {
	groups, err := u.ListGroups(ctx)
	if err != nil {
		return err
	}
	for _, g := range groups {
		if g.GID == gid {
			if !ownedGroup(g) {
				return fmt.Errorf("gid %d is group %s, which Racksail doesn't own, so it can't be a primary group: %w", gid, g.Name, system.ErrInvalid)
			}
			return nil
		}
	}
	// No group has this GID yet, such as an account's own group created in the
	// same plan. Only an account-range GID is left to allow.
	if gid < system.AccountUIDMin || gid > system.AccountUIDMax {
		return fmt.Errorf("gid %d names no group Racksail owns: %w", gid, system.ErrInvalid)
	}
	return nil
}

// checkCreatableUser holds a user created over a free name to what Racksail
// can manage afterwards, so a name a distribution package might take later
// can't be claimed in advance with panel-chosen settings: a system user must
// be racksail-* (or a platform user the installer makes), and an account
// must take an account-range UID and a name of its own. The name is its
// group's too, so it can't be a privileged group's, or a user's or group's
// the system or a package uses (ReservedName), which a package installed
// later would adopt with the account in it; and racksail-* names are for
// Racksail's service users, whose units the account sandbox doesn't bind.
// An account's UID is allocated at apply time, so a preview still carrying
// the zero placeholder is let through.
func checkCreatableUser(in EnsureUserInput, check bool) error {
	if in.System {
		if !strings.HasPrefix(in.Name, "racksail-") && !slices.Contains(platformUsers, in.Name) {
			return fmt.Errorf("a system user Racksail creates must be named racksail-*, not %s: %w", in.Name, system.ErrInvalid)
		}
		return nil
	}
	if ReservedName(in.Name) {
		return fmt.Errorf("%s is the name of a privileged group or of a user or group the system uses, so no account takes it: %w", in.Name, system.ErrInvalid)
	}
	if strings.HasPrefix(in.Name, "racksail-") {
		return fmt.Errorf("racksail-* names are for Racksail's service users, not an account such as %s: %w", in.Name, system.ErrInvalid)
	}
	if check && in.UID == 0 {
		return nil
	}
	if in.UID < system.AccountUIDMin || in.UID > system.AccountUIDMax {
		return fmt.Errorf("an account's uid must be in the account range %d-%d, not %d: %w", system.AccountUIDMin, system.AccountUIDMax, in.UID, system.ErrInvalid)
	}
	return nil
}

// checkUserHome confines where a managed user's home may point, so it can't
// be a location the panel itself writes or another account's. An account's
// home is its own directory under the accounts area (decision D1), or a
// directory inside it, never the accounts' parent, a sibling such as the
// replica or backups staging, or another account's; Racksail's own service
// users, which are system users, keep their homes out of the way: none
// (/nonexistent) or inside the panel's state.
func checkUserHome(account bool, name, home string) error {
	if !account {
		if home != "/nonexistent" && home != system.StateDir && !strings.HasPrefix(home, system.StateDir+"/") {
			return fmt.Errorf("a system user's home must be /nonexistent or under %s, not %s: %w", system.StateDir, home, system.ErrInvalid)
		}
		return nil
	}
	if own := system.AccountDir(name); home != own && !strings.HasPrefix(home, own+"/") {
		return fmt.Errorf("account %s's home must be its own directory, %s, not %s: %w", name, own, home, system.ErrInvalid)
	}
	return nil
}

// checkRemovableHome holds the home DeleteUser removes along with a user to
// where a managed user's home may be: an account's own directory, or for a
// service user a directory inside the panel's state, never the state
// itself. A home set elsewhere before can't take another account's data or
// the panel's state with it.
func checkRemovableHome(u system.User) error {
	if u.Home == "/nonexistent" {
		return nil
	}
	if !accountUser(u) && u.Home == system.StateDir {
		return fmt.Errorf("user %s's home is the panel's state, which isn't removed with it: %w", u.Name, system.ErrInvalid)
	}
	return checkUserHome(accountUser(u), u.Name, u.Home)
}

// accountUser reports whether a user is an account: its UID is in the
// account range.
func accountUser(u system.User) bool {
	return u.UID >= system.AccountUIDMin && u.UID <= system.AccountUIDMax
}

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
		if !managedUser(cur) {
			return Result{}, ErrNotAllowed.New("op", "DeleteUser", "target", in.Name)
		}
		if in.RemoveHome {
			if err := checkRemovableHome(cur); err != nil {
				return Result{}, err
			}
		}
		effect := system.Effect{Type: system.EffectUser, Action: "delete", Target: in.Name, Destructive: in.RemoveHome}
		if env.Check {
			return Changed(effect), nil
		}
		return Changed(effect), u.DeleteUser(ctx, in.Name, in.RemoveHome)
	},
})

// managedUser reports whether a user is one Racksail manages, so an
// operation may change or remove it: an account (its UID in the account
// range) or a racksail-* system user. Every other user is left alone: root
// and the administrators' own login users, and the platform's own identities
// (racksail, racksail-edge), which only the installer and the self-update
// paths create, much as the platform units are rendered only by
// InstallPlatformUnits.
func managedUser(u system.User) bool {
	if slices.Contains(platformUsers, u.Name) {
		return false
	}
	if accountUser(u) {
		return true
	}
	return u.System && strings.HasPrefix(u.Name, "racksail-") && len(u.Name) > len("racksail-")
}

// ownedGroup reports whether a group is one Racksail manages: an account's
// group (its GID in the account range) or a racksail-* system group.
// Racksail never changes the membership of any other group, and never of a
// privileged one such as racksail or racksail-edge.
func ownedGroup(g system.Group) bool {
	if slices.Contains(privilegedGroups, g.Name) {
		return false
	}
	if g.GID >= system.AccountUIDMin && g.GID <= system.AccountUIDMax {
		return true
	}
	return g.System && strings.HasPrefix(g.Name, "racksail-") && len(g.Name) > len("racksail-")
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
		if !ownedGroup(g) {
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

// AllocateAccountIDInput asks for a free account ID.
type AllocateAccountIDInput struct {
	// Exclude lists IDs the panel has given to accounts elsewhere, such as
	// on other servers of a cluster, so an account keeps its ID when it
	// moves.
	Exclude []int `json:"exclude,omitempty" doc:"IDs already given to accounts elsewhere"`
}

// Validate implements Input.
func (in AllocateAccountIDInput) Validate() error {
	if len(in.Exclude) > system.AccountUIDMax-system.AccountUIDMin+1 {
		return fmt.Errorf("too many excluded ids")
	}
	for _, id := range in.Exclude {
		if id < system.AccountUIDMin || id > system.AccountUIDMax {
			return fmt.Errorf("id %d is outside the account range", id)
		}
	}
	return nil
}

// AccountID is an allocated account ID with its subordinate ID range.
type AccountID struct {
	UID    int            `json:"uid" doc:"User and group ID for the account"`
	SubIDs system.IDRange `json:"subIds" doc:"Subordinate IDs for the account's rootless containers"`
}

// AllocateAccountID picks the lowest account ID no user or group has, and
// holds it for ten minutes so the next allocation doesn't pick it too.
// EnsureUser with that UID uses it up.
var AllocateAccountID = Register(Def[AllocateAccountIDInput, AccountID]{
	Name:        "AllocateAccountID",
	Summary:     "Pick a free account ID",
	Description: "Returns the lowest ID in the account range that no user or group on this server has and the panel didn't exclude, with its subordinate ID range. The ID is held for ten minutes so concurrent allocations don't collide.",
	ReadOnly:    true,
	Run: func(ctx context.Context, env *Env, in AllocateAccountIDInput) (AccountID, error) {
		if env.Runtime == nil {
			return AccountID{}, errNoRuntime
		}
		uid, err := env.Runtime.reserve(ctx, in.Exclude)
		if err != nil {
			return AccountID{}, err
		}
		return AccountID{UID: uid, SubIDs: system.AccountSubIDs(uid)}, nil
	},
})
