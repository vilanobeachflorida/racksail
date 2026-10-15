package system

import (
	"context"
	"fmt"
)

// Users manages Linux users and groups.
type Users interface {
	LookupUser(ctx context.Context, name string) (User, error)
	LookupGroup(ctx context.Context, name string) (Group, error)
	ListUsers(ctx context.Context) ([]User, error)
	ListGroups(ctx context.Context) ([]Group, error)
	// CreateUser creates a user. A zero UID lets the system pick one in the
	// system or regular range depending on User.System. A zero GID creates a
	// group with the user's name and the same ID. The home directory is not
	// created; callers create it with the ownership they need.
	CreateUser(ctx context.Context, u User) error
	// UpdateUser changes the shell, comment, supplementary groups, lock state
	// and subordinate IDs of an existing user. Names and IDs don't change.
	UpdateUser(ctx context.Context, u User) error
	DeleteUser(ctx context.Context, name string, removeHome bool) error
	// CreateGroup creates a group. A zero GID lets the system pick one.
	CreateGroup(ctx context.Context, g Group) error
	// UpdateGroup sets the members of an existing group.
	UpdateGroup(ctx context.Context, g Group) error
	DeleteGroup(ctx context.Context, name string) error
}

// User is a Linux user.
type User struct {
	Name    string   `json:"name"`
	UID     int      `json:"uid"`
	GID     int      `json:"gid"`
	Groups  []string `json:"groups,omitempty" doc:"Supplementary groups"`
	Home    string   `json:"home"`
	Shell   string   `json:"shell"`
	Comment string   `json:"comment,omitempty"`
	System  bool     `json:"system,omitempty" doc:"A system account without password aging"`
	Locked  bool     `json:"locked,omitempty" doc:"Password sign-in is disabled"`
	// SubIDs is the subordinate UID and GID range for rootless containers.
	SubIDs *IDRange `json:"subIds,omitempty"`
}

// IDRange is a range of subordinate user or group IDs.
type IDRange struct {
	Start int `json:"start"`
	Count int `json:"count"`
}

// Group is a Linux group.
type Group struct {
	Name    string   `json:"name"`
	GID     int      `json:"gid"`
	Members []string `json:"members,omitempty"`
	System  bool     `json:"system,omitempty"`
}

// AllocateAccountID returns the lowest ID in the account range that no user
// or group on the machine has and that isn't in exclude. Accounts get the
// same number as user ID and group ID, so both must be free.
func AllocateAccountID(users []User, groups []Group, exclude []int) (int, error) {
	used := make(map[int]bool, len(users)+len(groups)+len(exclude))
	for _, u := range users {
		used[u.UID] = true
		used[u.GID] = true
	}
	for _, g := range groups {
		used[g.GID] = true
	}
	for _, id := range exclude {
		used[id] = true
	}
	for id := AccountUIDMin; id <= AccountUIDMax; id++ {
		if !used[id] {
			return id, nil
		}
	}
	return 0, fmt.Errorf("every account ID from %d to %d is taken: %w", AccountUIDMin, AccountUIDMax, ErrExists)
}
