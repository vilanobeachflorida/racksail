package auth

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"sync"

	"github.com/vilanobeachflorida/racksail/internal/store"
)

// PermissionInfo describes a permission that isn't an operation on a kind,
// such as reading the audit log, for the permission matrix, the token form
// and the docs. Kinds' permissions are described by their kinds.
type PermissionInfo struct {
	Name        Permission `json:"name" doc:"The permission, such as core.audit.read"`
	Description string     `json:"description" doc:"What the permission lets someone do"`
}

// Core permissions that no kind defines.
var (
	PermUserRead   = RegisterPermission("core.user.read", "See the panel's users and what they may do.")
	PermUserCreate = RegisterPermission("core.user.create", "Add users and give them roles.")
	PermUserUpdate = RegisterPermission("core.user.update", "Change users' details and roles, and turn them off.")
	PermUserDelete = RegisterPermission("core.user.delete", "Delete users.")
	PermUserReset  = RegisterPermission("core.user.reset", "Help users back in: issue sign-in links, remove two-factor methods, end sessions and revoke API tokens.")
)

var permissions = struct {
	sync.RWMutex
	byName map[Permission]PermissionInfo
	errs   []error
}{byName: map[Permission]PermissionInfo{}}

var permissionPattern = regexp.MustCompile(`^[a-z][a-z0-9]*(\.[a-z][a-z0-9_]*)+$`)

func init() {
	for _, p := range []PermissionInfo{
		{PermOverride, "Let hooks and rules loosen safety rules such as sandbox minimums. Shown in red and logged."},
		{PermSecretsRead, "Read secret values, such as passwords kept for resources."},
		{PermImpersonate, "View the panel as one of your customers, and act for them, with every step recorded."},
	} {
		RegisterPermission(p.Name, p.Description)
	}
}

// RegisterPermission describes a permission the core or a module checks
// itself, outside the operations of a kind, and returns it. It is meant
// for package-level variables; problems are reported by Err.
func RegisterPermission(name Permission, description string) Permission {
	permissions.Lock()
	defer permissions.Unlock()
	switch {
	case !permissionPattern.MatchString(string(name)):
		permissions.errs = append(permissions.errs, fmt.Errorf("auth: permission %q must be dotted lowercase words", name))
	case description == "":
		permissions.errs = append(permissions.errs, fmt.Errorf("auth: permission %s needs a description", name))
	case permissions.byName[name].Name != "":
		permissions.errs = append(permissions.errs, fmt.Errorf("auth: permission %s is registered twice", name))
	default:
		permissions.byName[name] = PermissionInfo{Name: name, Description: description}
	}
	return name
}

// Err reports problems found while permissions were registered.
func Err() error {
	permissions.RLock()
	defer permissions.RUnlock()
	return errors.Join(permissions.errs...)
}

// Permissions lists every permission: those registered on their own and the
// operations of the given kinds, sorted by name.
func Permissions(kinds []store.Kind) []PermissionInfo {
	permissions.RLock()
	out := make([]PermissionInfo, 0, len(permissions.byName))
	for _, p := range permissions.byName {
		out = append(out, p)
	}
	permissions.RUnlock()
	seen := map[Permission]bool{}
	for _, p := range out {
		seen[p.Name] = true
	}
	for _, k := range kinds {
		for _, p := range KindPermissions(k) {
			if !seen[p.Name] {
				seen[p.Name] = true
				out = append(out, p)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// KindPermissions returns the permissions of a kind's operations: reading
// it (which covers its history), creating, changing and deleting it, and
// each of its actions.
func KindPermissions(k store.Kind) []PermissionInfo {
	info := k.Info()
	many := info.Plural
	out := []PermissionInfo{
		{KindPermission(info.Name, "read"), "See " + many + "."},
		{KindPermission(info.Name, "create"), "Create " + many + "."},
		{KindPermission(info.Name, "update"), "Change " + many + "."},
		{KindPermission(info.Name, "delete"), "Delete " + many + "."},
	}
	for _, a := range info.Actions {
		out = append(out, PermissionInfo{KindPermission(info.Name, a.Name), a.Summary})
	}
	return out
}

// Known reports whether perm is a registered permission or the permission
// of an operation on one of the kinds.
func Known(perm Permission, kinds []store.Kind) bool {
	permissions.RLock()
	_, ok := permissions.byName[perm]
	permissions.RUnlock()
	if ok {
		return true
	}
	for _, k := range kinds {
		for _, p := range KindPermissions(k) {
			if p.Name == perm {
				return true
			}
		}
	}
	return false
}

// RoleInfo describes a role for people choosing one.
type RoleInfo struct {
	Role        Role               `json:"role" enum:"admin,reseller,owner,collaborator,readonly"`
	Title       string             `json:"title"`
	Description string             `json:"description"`
	Patterns    []string           `json:"patterns" doc:"The permissions the role carries, as patterns such as * or *.read"`
	Levels      []store.ScopeLevel `json:"levels" doc:"Where the role can be given: on the server, a reseller or an account"`
}

// Roles describes every role, from the most to the least powerful.
func Roles() []RoleInfo {
	return []RoleInfo{
		{RoleAdmin, "Admin", "Runs the whole server: every module, every account and every setting.", RolePermissions[RoleAdmin], RoleLevels(RoleAdmin)},
		{RoleReseller, "Reseller", "Sells hosting from this server: their own plans, customers and accounts, and nothing of the server's own.", RolePermissions[RoleReseller], RoleLevels(RoleReseller)},
		{RoleOwner, "Account owner", "Runs one account: its sites, mail, databases, files and people, within its plan. Only resellers and admins change the account's limits.", RolePermissions[RoleOwner], RoleLevels(RoleOwner)},
		{RoleCollaborator, "Collaborator", "Sees what they're given and does only what their permissions say, on a whole account or on single resources.", RolePermissions[RoleCollaborator], RoleLevels(RoleCollaborator)},
		{RoleReadOnly, "Read-only", "Sees everything their grant reaches and changes nothing.", RolePermissions[RoleReadOnly], RoleLevels(RoleReadOnly)},
	}
}

// Access is how far a role reaches with a permission.
type Access string

// Access levels in the permission matrix.
const (
	// AccessEverywhere is a permission the role has on everything, as an
	// admin's grant on the server covers every scope.
	AccessEverywhere Access = "everywhere"
	// AccessOwnScope is a permission the role has within the scope it was
	// granted on.
	AccessOwnScope Access = "scope"
	// AccessByName is a special permission the role has only when a grant
	// names it.
	AccessByName Access = "named"
	// AccessNone is a permission the role never has.
	AccessNone Access = "none"
)

// MatrixRow is one permission in the permission matrix, with how far each
// role reaches with it.
type MatrixRow struct {
	Permission Permission      `json:"permission"`
	Roles      map[Role]Access `json:"roles"`
}

// MatrixFor works out the permission matrix for some permissions: for each,
// how far each role reaches with it when granted where the role belongs.
// A collaborator counts with its default read access.
func MatrixFor(perms []Permission) []MatrixRow {
	out := make([]MatrixRow, 0, len(perms))
	for _, perm := range slices.Sorted(slices.Values(slices.Compact(slices.Sorted(slices.Values(perms))))) {
		row := MatrixRow{Permission: perm, Roles: map[Role]Access{}}
		for _, info := range Roles() {
			row.Roles[info.Role] = roleAccess(info.Role, perm)
		}
		out = append(out, row)
	}
	return out
}

func roleAccess(r Role, perm Permission) Access {
	if perm.Special() {
		// Only a server admin can hold core.override, since it loosens
		// what protects every account.
		switch {
		case r == RoleAdmin, perm != PermOverride && (r == RoleReseller || r == RoleOwner):
			return AccessByName
		}
		return AccessNone
	}
	for _, level := range RoleLevels(r) {
		scope := store.Scope{Level: level}
		if level != store.ScopeServer {
			scope.ID = store.NewID("acct")
		}
		if permits(Grant{Role: r, Scope: scope}, perm) {
			if r == RoleAdmin {
				return AccessEverywhere
			}
			return AccessOwnScope
		}
	}
	return AccessNone
}
