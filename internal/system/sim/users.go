package sim

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"sort"

	"github.com/vilanobeachflorida/racksail/internal/system"
)

var accountNamePattern = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)

type simUsers struct{ m *Machine }

func (u simUsers) LookupUser(ctx context.Context, name string) (system.User, error) {
	m := u.m
	if err := m.lock(ctx); err != nil {
		return system.User{}, err
	}
	defer m.mu.Unlock()
	usr, ok := m.users[name]
	if !ok {
		return system.User{}, notFound("user " + name)
	}
	return cloneUser(usr), nil
}

func (u simUsers) LookupGroup(ctx context.Context, name string) (system.Group, error) {
	m := u.m
	if err := m.lock(ctx); err != nil {
		return system.Group{}, err
	}
	defer m.mu.Unlock()
	g, ok := m.groups[name]
	if !ok {
		return system.Group{}, notFound("group " + name)
	}
	return cloneGroup(g), nil
}

func (u simUsers) ListUsers(ctx context.Context) ([]system.User, error) {
	m := u.m
	if err := m.lock(ctx); err != nil {
		return nil, err
	}
	defer m.mu.Unlock()
	out := make([]system.User, 0, len(m.users))
	for _, usr := range m.users {
		out = append(out, cloneUser(usr))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UID < out[j].UID })
	return out, nil
}

func (u simUsers) ListGroups(ctx context.Context) ([]system.Group, error) {
	m := u.m
	if err := m.lock(ctx); err != nil {
		return nil, err
	}
	defer m.mu.Unlock()
	out := make([]system.Group, 0, len(m.groups))
	for _, g := range m.groups {
		out = append(out, cloneGroup(g))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].GID < out[j].GID })
	return out, nil
}

func (u simUsers) CreateUser(ctx context.Context, usr system.User) error {
	m := u.m
	m.delay(ctx, opCreateUser, 0)
	if err := m.lock(ctx); err != nil {
		return err
	}
	defer m.mu.Unlock()
	if err := m.record("users", "create_user", usr.Name); err != nil {
		return err
	}
	if !accountNamePattern.MatchString(usr.Name) {
		return invalid("user name %q", usr.Name)
	}
	if _, ok := m.users[usr.Name]; ok {
		return fmt.Errorf("user %s: %w", usr.Name, system.ErrExists)
	}
	if usr.UID == 0 {
		usr.UID = m.freeID(usr.System)
	} else {
		for _, other := range m.users {
			if other.UID == usr.UID {
				return fmt.Errorf("uid %d: %w", usr.UID, system.ErrExists)
			}
		}
	}
	if usr.GID == 0 {
		if _, ok := m.groups[usr.Name]; ok {
			return fmt.Errorf("group %s: %w", usr.Name, system.ErrExists)
		}
		gid := usr.UID
		for _, g := range m.groups {
			if g.GID == gid {
				gid = m.freeID(usr.System)
				break
			}
		}
		m.groups[usr.Name] = &system.Group{Name: usr.Name, GID: gid, System: usr.System}
		usr.GID = gid
	} else if m.groupName(usr.GID) == fmt.Sprint(usr.GID) {
		return notFound(fmt.Sprintf("group with gid %d", usr.GID))
	}
	for _, g := range usr.Groups {
		grp, ok := m.groups[g]
		if !ok {
			return notFound("group " + g)
		}
		if !slices.Contains(grp.Members, usr.Name) {
			grp.Members = append(grp.Members, usr.Name)
		}
	}
	if usr.Shell == "" {
		usr.Shell = "/usr/sbin/nologin"
	}
	created := cloneUser(&usr)
	m.users[usr.Name] = &created
	return nil
}

func (u simUsers) UpdateUser(ctx context.Context, usr system.User) error {
	m := u.m
	if err := m.lock(ctx); err != nil {
		return err
	}
	defer m.mu.Unlock()
	if err := m.record("users", "update_user", usr.Name); err != nil {
		return err
	}
	cur, ok := m.users[usr.Name]
	if !ok {
		return notFound("user " + usr.Name)
	}
	for _, g := range usr.Groups {
		if _, ok := m.groups[g]; !ok {
			return notFound("group " + g)
		}
	}
	for _, g := range m.groups {
		g.Members = slices.DeleteFunc(g.Members, func(n string) bool { return n == usr.Name })
		if slices.Contains(usr.Groups, g.Name) {
			g.Members = append(g.Members, usr.Name)
		}
	}
	cur.Groups = slices.Clone(usr.Groups)
	cur.Shell = usr.Shell
	cur.Comment = usr.Comment
	cur.Locked = usr.Locked
	if usr.Home != "" {
		cur.Home = usr.Home
	}
	if usr.SubIDs != nil {
		ids := *usr.SubIDs
		cur.SubIDs = &ids
	} else {
		cur.SubIDs = nil
	}
	return nil
}

func (u simUsers) DeleteUser(ctx context.Context, name string, removeHome bool) error {
	m := u.m
	if err := m.lock(ctx); err != nil {
		return err
	}
	defer m.mu.Unlock()
	if err := m.record("users", "delete_user", name); err != nil {
		return err
	}
	usr, ok := m.users[name]
	if !ok {
		return notFound("user " + name)
	}
	if usr.UID == 0 {
		return invalid("refusing to delete root")
	}
	for _, p := range m.procs {
		if p.UID == usr.UID {
			return invalid("user %s still has running processes", name)
		}
	}
	if removeHome && usr.Home != "" && usr.Home != "/" && usr.Home != "/nonexistent" {
		m.removeTree(usr.Home)
	}
	delete(m.users, name)
	for _, g := range m.groups {
		g.Members = slices.DeleteFunc(g.Members, func(n string) bool { return n == name })
	}
	if g, ok := m.groups[name]; ok && g.GID == usr.GID && len(g.Members) == 0 {
		delete(m.groups, name)
	}
	return nil
}

func (u simUsers) CreateGroup(ctx context.Context, g system.Group) error {
	m := u.m
	if err := m.lock(ctx); err != nil {
		return err
	}
	defer m.mu.Unlock()
	if err := m.record("users", "create_group", g.Name); err != nil {
		return err
	}
	if !accountNamePattern.MatchString(g.Name) {
		return invalid("group name %q", g.Name)
	}
	if _, ok := m.groups[g.Name]; ok {
		return fmt.Errorf("group %s: %w", g.Name, system.ErrExists)
	}
	if g.GID == 0 {
		g.GID = m.freeID(g.System)
	} else if m.groupName(g.GID) != fmt.Sprint(g.GID) {
		return fmt.Errorf("gid %d: %w", g.GID, system.ErrExists)
	}
	for _, member := range g.Members {
		if _, ok := m.users[member]; !ok {
			return notFound("user " + member)
		}
	}
	created := cloneGroup(&g)
	m.groups[g.Name] = &created
	return nil
}

func (u simUsers) UpdateGroup(ctx context.Context, g system.Group) error {
	m := u.m
	if err := m.lock(ctx); err != nil {
		return err
	}
	defer m.mu.Unlock()
	if err := m.record("users", "update_group", g.Name); err != nil {
		return err
	}
	cur, ok := m.groups[g.Name]
	if !ok {
		return notFound("group " + g.Name)
	}
	for _, member := range g.Members {
		if _, ok := m.users[member]; !ok {
			return notFound("user " + member)
		}
	}
	cur.Members = slices.Clone(g.Members)
	return nil
}

func (u simUsers) DeleteGroup(ctx context.Context, name string) error {
	m := u.m
	if err := m.lock(ctx); err != nil {
		return err
	}
	defer m.mu.Unlock()
	if err := m.record("users", "delete_group", name); err != nil {
		return err
	}
	g, ok := m.groups[name]
	if !ok {
		return notFound("group " + name)
	}
	for _, usr := range m.users {
		if usr.GID == g.GID {
			return invalid("group %s is the primary group of %s", name, usr.Name)
		}
	}
	delete(m.groups, name)
	return nil
}

// freeID picks an unused ID in the system (100-999) or regular (1000-19999)
// range, the way useradd does. Account IDs are always given explicitly. The
// caller holds m.mu.
func (m *Machine) freeID(sys bool) int {
	lo, hi := 1000, 19999
	if sys {
		lo, hi = 100, 999
	}
	used := map[int]bool{}
	for _, u := range m.users {
		used[u.UID] = true
	}
	for _, g := range m.groups {
		used[g.GID] = true
	}
	if sys {
		for id := hi; id >= lo; id-- {
			if !used[id] {
				return id
			}
		}
	}
	for id := lo; id <= hi; id++ {
		if !used[id] {
			return id
		}
	}
	return hi + 1
}

func cloneUser(u *system.User) system.User {
	c := *u
	c.Groups = slices.Clone(u.Groups)
	if u.SubIDs != nil {
		ids := *u.SubIDs
		c.SubIDs = &ids
	}
	return c
}

func cloneGroup(g *system.Group) system.Group {
	c := *g
	c.Members = slices.Clone(g.Members)
	return c
}
