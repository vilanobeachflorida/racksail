package auth

import (
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"unicode"

	"github.com/vilanobeachflorida/racksail/internal/store"
)

// PermissionInfo describes a permission that isn't an operation on a kind,
// such as reading the audit log, for the permission matrix, the token form
// and the docs. Kinds' permissions are described by their kinds.
type PermissionInfo struct {
	Name        Permission `json:"name" doc:"The permission, such as core.audit.read"`
	Description string     `json:"description" doc:"What the permission lets someone do"`
	// Levels are the levels of the scopes the permission is checked
	// against: where what it is about belongs, such as the server for the
	// panel's metrics. Empty means any.
	Levels []store.ScopeLevel `json:"levels,omitempty" doc:"Where the permission is checked: on the server, a reseller or an account; anywhere when empty"`
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
		{PermOverride, "Let hooks and rules loosen safety rules such as sandbox minimums. Shown in red and logged.", []store.ScopeLevel{store.ScopeServer}},
		{PermSecretsRead, "Read secret values, such as passwords kept for resources.", nil},
		{PermImpersonate, "View the panel as one of your customers, and act for them, with every step recorded.", nil},
	} {
		RegisterPermission(p.Name, p.Description, p.Levels...)
	}
}

// RegisterPermission describes a permission the core or a module checks
// itself, outside the operations of a kind, and returns it. levels are
// where it is checked, such as only against the server for something of
// the whole server's; without them, it may be checked anywhere. It is meant
// for package-level variables; problems are reported by Err.
func RegisterPermission(name Permission, description string, levels ...store.ScopeLevel) Permission {
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
		permissions.byName[name] = PermissionInfo{Name: name, Description: description, Levels: levels}
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
	many := pluralOf(info.Title)
	// They're checked where the kind's resources belong.
	levels := append([]store.ScopeLevel{info.Scope}, info.Levels...)
	out := []PermissionInfo{
		{KindPermission(info.Name, "read"), "See " + many + ".", levels},
		{KindPermission(info.Name, "create"), "Create " + many + ".", levels},
		{KindPermission(info.Name, "update"), "Change " + many + ".", levels},
		{KindPermission(info.Name, "delete"), "Delete " + many + ".", levels},
	}
	for _, a := range info.Actions {
		out = append(out, PermissionInfo{KindPermission(info.Name, a.Name), a.Summary, levels})
	}
	return out
}

// pluralOf names a kind's resources in a sentence, from its title: Sign-in
// policy becomes sign-in policies, leaving abbreviations such as DNS alone.
func pluralOf(title string) string {
	r := []rune(title)
	if len(r) > 1 && !unicode.IsUpper(r[1]) {
		r[0] = unicode.ToLower(r[0])
	}
	s := string(r)
	lower := strings.ToLower(s)
	switch {
	case len(lower) > 1 && strings.HasSuffix(lower, "y") && !strings.ContainsRune("aeiou", rune(lower[len(lower)-2])):
		return s[:len(s)-1] + "ies"
	case strings.HasSuffix(lower, "s"), strings.HasSuffix(lower, "x"), strings.HasSuffix(lower, "z"),
		strings.HasSuffix(lower, "ch"), strings.HasSuffix(lower, "sh"):
		return s + "es"
	}
	return s + "s"
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
	Role        Role               `json:"role" enum:"admin,reseller,owner,collaborator,readonly" doc:"The role's name, as grants give it"`
	Title       string             `json:"title" doc:"What the role is called"`
	Description string             `json:"description" doc:"What it lets someone do"`
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
// how far each role reaches with it when granted where the role belongs,
// counting only where the permission is checked. A collaborator counts with
// its default read access. A permission listed twice counts once.
func MatrixFor(perms []PermissionInfo) []MatrixRow {
	byName := map[Permission]PermissionInfo{}
	for _, p := range perms {
		if _, seen := byName[p.Name]; !seen {
			byName[p.Name] = p
		}
	}
	out := make([]MatrixRow, 0, len(byName))
	for _, name := range slices.Sorted(maps.Keys(byName)) {
		row := MatrixRow{Permission: name, Roles: map[Role]Access{}}
		for _, info := range Roles() {
			row.Roles[info.Role] = roleAccess(info.Role, byName[name])
		}
		out = append(out, row)
	}
	return out
}

func roleAccess(r Role, p PermissionInfo) Access {
	perm := p.Name
	for _, level := range RoleLevels(r) {
		if !reaches(level, p.Levels) {
			continue
		}
		if perm.Special() {
			// Any grant can name a special permission, but core.override
			// only an admin's on the whole server, since it loosens what
			// protects every account.
			if perm == PermOverride && (r != RoleAdmin || level != store.ScopeServer) {
				continue
			}
			return AccessByName
		}
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

// reaches reports whether a grant on a scope of the given level reaches
// scopes of one of levels, any when there are none: the server reaches
// everything, a reseller itself and its customers' accounts, and an account
// itself.
func reaches(grant store.ScopeLevel, levels []store.ScopeLevel) bool {
	if len(levels) == 0 || grant == store.ScopeServer {
		return true
	}
	for _, l := range levels {
		if l == grant || (grant == store.ScopeReseller && l == store.ScopeAccount) {
			return true
		}
	}
	return false
}
