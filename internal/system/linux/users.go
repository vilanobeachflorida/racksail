package linux

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/vilanobeachflorida/racksail/internal/system"
)

var namePattern = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)

func checkName(kind, name string) error {
	if !namePattern.MatchString(name) {
		return fmt.Errorf("%s name %q: %w", kind, name, system.ErrInvalid)
	}
	return nil
}

type users struct{ h *host }

// accountDB is a snapshot of the local account files.
type accountDB struct {
	users  []system.User
	groups []system.Group
}

func (h *host) readOptional(p string) ([]byte, error) {
	data, err := os.ReadFile(h.path(p))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return data, err
}

func (h *host) accounts() (*accountDB, error) {
	passwd, err := os.ReadFile(h.path("/etc/passwd"))
	if err != nil {
		return nil, fmt.Errorf("reading /etc/passwd: %w", err)
	}
	group, err := os.ReadFile(h.path("/etc/group"))
	if err != nil {
		return nil, fmt.Errorf("reading /etc/group: %w", err)
	}
	shadow, err := h.readOptional("/etc/shadow")
	if err != nil {
		return nil, fmt.Errorf("reading /etc/shadow: %w", err)
	}
	subuid, err := h.readOptional("/etc/subuid")
	if err != nil {
		return nil, fmt.Errorf("reading /etc/subuid: %w", err)
	}
	db := &accountDB{users: parsePasswd(passwd), groups: parseGroup(group)}
	locked := parseShadowLocked(shadow)
	subIDs := parseSubIDs(subuid)
	for i := range db.users {
		u := &db.users[i]
		u.Locked = locked[u.Name]
		if r, ok := subIDs[u.Name]; ok {
			r := r
			u.SubIDs = &r
		}
		for _, g := range db.groups {
			if slices.Contains(g.Members, u.Name) {
				u.Groups = append(u.Groups, g.Name)
			}
		}
	}
	return db, nil
}

// lookupIDs resolves an owner to numeric IDs from the account files. An
// empty owner means the root account.
func (h *host) lookupIDs(o system.Owner) (int, int, error) {
	db, err := h.accounts()
	if err != nil {
		return 0, 0, err
	}
	uid, gid := 0, 0
	if root, ok := db.user("root"); ok {
		uid, gid = root.UID, root.GID
	}
	if o.User != "" {
		u, ok := db.user(o.User)
		if !ok {
			return 0, 0, fmt.Errorf("user %s: %w", o.User, system.ErrNotFound)
		}
		uid, gid = u.UID, u.GID
	}
	if o.Group != "" {
		g, ok := db.group(o.Group)
		if !ok {
			return 0, 0, fmt.Errorf("group %s: %w", o.Group, system.ErrNotFound)
		}
		gid = g.GID
	}
	return uid, gid, nil
}

// AccountIDs returns a lookup function for OSRunner.Accounts that reads the
// account files under root.
func AccountIDs(root string) func(name string) (int, int, error) {
	h := &host{root: root}
	return func(name string) (int, int, error) { return h.lookupIDs(system.Owner{User: name}) }
}

func (db *accountDB) user(name string) (system.User, bool) {
	for _, u := range db.users {
		if u.Name == name {
			return u, true
		}
	}
	return system.User{}, false
}

func (db *accountDB) group(name string) (system.Group, bool) {
	for _, g := range db.groups {
		if g.Name == name {
			return g, true
		}
	}
	return system.Group{}, false
}

func (db *accountDB) userName(uid int) string {
	for _, u := range db.users {
		if u.UID == uid {
			return u.Name
		}
	}
	return strconv.Itoa(uid)
}

func (db *accountDB) groupName(gid int) string {
	for _, g := range db.groups {
		if g.GID == gid {
			return g.Name
		}
	}
	return strconv.Itoa(gid)
}

// parsePasswd reads /etc/passwd entries. Malformed lines are skipped, as the
// C library does.
func parsePasswd(data []byte) []system.User {
	var out []system.User
	eachLine(data, func(line string) {
		f := strings.Split(line, ":")
		if len(f) != 7 {
			return
		}
		uid, err1 := strconv.Atoi(f[2])
		gid, err2 := strconv.Atoi(f[3])
		if err1 != nil || err2 != nil {
			return
		}
		comment, _, _ := strings.Cut(f[4], ",")
		out = append(out, system.User{Name: f[0], UID: uid, GID: gid, Comment: comment, Home: f[5], Shell: f[6], System: uid < 1000})
	})
	return out
}

func parseGroup(data []byte) []system.Group {
	var out []system.Group
	eachLine(data, func(line string) {
		f := strings.Split(line, ":")
		if len(f) != 4 {
			return
		}
		gid, err := strconv.Atoi(f[2])
		if err != nil {
			return
		}
		g := system.Group{Name: f[0], GID: gid, System: gid < 1000}
		if f[3] != "" {
			g.Members = strings.Split(f[3], ",")
		}
		out = append(out, g)
	})
	return out
}

func parseShadowLocked(data []byte) map[string]bool {
	out := map[string]bool{}
	eachLine(data, func(line string) {
		f := strings.Split(line, ":")
		if len(f) < 2 {
			return
		}
		out[f[0]] = strings.HasPrefix(f[1], "!") || f[1] == "*"
	})
	return out
}

// parseSubIDs reads /etc/subuid, keeping the first range of each user.
func parseSubIDs(data []byte) map[string]system.IDRange {
	out := map[string]system.IDRange{}
	eachLine(data, func(line string) {
		f := strings.Split(line, ":")
		if len(f) != 3 {
			return
		}
		start, err1 := strconv.Atoi(f[1])
		count, err2 := strconv.Atoi(f[2])
		if err1 != nil || err2 != nil {
			return
		}
		if _, seen := out[f[0]]; !seen {
			out[f[0]] = system.IDRange{Start: start, Count: count}
		}
	})
	return out
}

func eachLine(data []byte, fn func(string)) {
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fn(line)
	}
}

func (u *users) LookupUser(ctx context.Context, name string) (system.User, error) {
	db, err := u.h.accounts()
	if err != nil {
		return system.User{}, err
	}
	usr, ok := db.user(name)
	if !ok {
		return system.User{}, fmt.Errorf("user %s: %w", name, system.ErrNotFound)
	}
	return usr, nil
}

func (u *users) LookupGroup(ctx context.Context, name string) (system.Group, error) {
	db, err := u.h.accounts()
	if err != nil {
		return system.Group{}, err
	}
	g, ok := db.group(name)
	if !ok {
		return system.Group{}, fmt.Errorf("group %s: %w", name, system.ErrNotFound)
	}
	return g, nil
}

func (u *users) ListUsers(ctx context.Context) ([]system.User, error) {
	db, err := u.h.accounts()
	if err != nil {
		return nil, err
	}
	sort.Slice(db.users, func(i, j int) bool { return db.users[i].UID < db.users[j].UID })
	return db.users, nil
}

func (u *users) ListGroups(ctx context.Context) ([]system.Group, error) {
	db, err := u.h.accounts()
	if err != nil {
		return nil, err
	}
	sort.Slice(db.groups, func(i, j int) bool { return db.groups[i].GID < db.groups[j].GID })
	return db.groups, nil
}

func idRange(r system.IDRange) string {
	return fmt.Sprintf("%d-%d", r.Start, r.Start+r.Count-1)
}

func (u *users) CreateUser(ctx context.Context, usr system.User) error {
	if err := checkName("user", usr.Name); err != nil {
		return err
	}
	for _, g := range usr.Groups {
		if err := checkName("group", g); err != nil {
			return err
		}
	}
	shell := usr.Shell
	if shell == "" {
		shell = "/usr/sbin/nologin"
	}
	// Subordinate IDs are assigned explicitly below, never from login.defs.
	args := []string{"--no-create-home", "--shell", shell, "-K", "SUB_UID_COUNT=0", "-K", "SUB_GID_COUNT=0"}
	if usr.Home != "" {
		args = append(args, "--home-dir", usr.Home)
	}
	if usr.Comment != "" {
		args = append(args, "--comment", usr.Comment)
	}
	if usr.System {
		args = append(args, "--system")
	}
	if usr.UID != 0 {
		args = append(args, "--uid", strconv.Itoa(usr.UID))
	}
	if usr.GID != 0 {
		args = append(args, "--gid", strconv.Itoa(usr.GID))
	} else {
		args = append(args, "--user-group")
	}
	if len(usr.Groups) > 0 {
		args = append(args, "--groups", strings.Join(usr.Groups, ","))
	}
	args = append(args, "--", usr.Name)
	if _, err := u.h.exec(ctx, progUseradd, args...); err != nil {
		return fmt.Errorf("creating user %s: %w", usr.Name, err)
	}
	if usr.SubIDs != nil {
		r := idRange(*usr.SubIDs)
		if _, err := u.h.exec(ctx, progUsermod, "--add-subuids", r, "--add-subgids", r, "--", usr.Name); err != nil {
			return fmt.Errorf("assigning subordinate ids to %s: %w", usr.Name, err)
		}
	}
	return nil
}

func (u *users) UpdateUser(ctx context.Context, usr system.User) error {
	if err := checkName("user", usr.Name); err != nil {
		return err
	}
	cur, err := u.LookupUser(ctx, usr.Name)
	if err != nil {
		return err
	}
	for _, g := range usr.Groups {
		if err := checkName("group", g); err != nil {
			return err
		}
	}
	shell := usr.Shell
	if shell == "" {
		shell = cur.Shell
	}
	args := []string{"--shell", shell, "--comment", usr.Comment, "--groups", strings.Join(usr.Groups, ",")}
	if usr.Home != "" && usr.Home != cur.Home {
		args = append(args, "--home", usr.Home)
	}
	if usr.Locked && !cur.Locked {
		args = append(args, "--lock")
	} else if !usr.Locked && cur.Locked {
		args = append(args, "--unlock")
	}
	if !sameRange(cur.SubIDs, usr.SubIDs) {
		if cur.SubIDs != nil {
			r := idRange(*cur.SubIDs)
			args = append(args, "--del-subuids", r, "--del-subgids", r)
		}
		if usr.SubIDs != nil {
			r := idRange(*usr.SubIDs)
			args = append(args, "--add-subuids", r, "--add-subgids", r)
		}
	}
	args = append(args, "--", usr.Name)
	if _, err := u.h.exec(ctx, progUsermod, args...); err != nil {
		return fmt.Errorf("updating user %s: %w", usr.Name, err)
	}
	return nil
}

func sameRange(a, b *system.IDRange) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func (u *users) DeleteUser(ctx context.Context, name string, removeHome bool) error {
	if err := checkName("user", name); err != nil {
		return err
	}
	if name == "root" {
		return fmt.Errorf("refusing to delete root: %w", system.ErrInvalid)
	}
	args := []string{}
	if removeHome {
		args = append(args, "--remove")
	}
	args = append(args, "--", name)
	if _, err := u.h.exec(ctx, progUserdel, args...); err != nil {
		return fmt.Errorf("deleting user %s: %w", name, err)
	}
	return nil
}

func (u *users) CreateGroup(ctx context.Context, g system.Group) error {
	if err := checkName("group", g.Name); err != nil {
		return err
	}
	args := []string{}
	if g.System {
		args = append(args, "--system")
	}
	if g.GID != 0 {
		args = append(args, "--gid", strconv.Itoa(g.GID))
	}
	args = append(args, "--", g.Name)
	if _, err := u.h.exec(ctx, progGroupadd, args...); err != nil {
		return fmt.Errorf("creating group %s: %w", g.Name, err)
	}
	if len(g.Members) > 0 {
		return u.UpdateGroup(ctx, g)
	}
	return nil
}

func (u *users) UpdateGroup(ctx context.Context, g system.Group) error {
	if err := checkName("group", g.Name); err != nil {
		return err
	}
	for _, m := range g.Members {
		if err := checkName("user", m); err != nil {
			return err
		}
	}
	if _, err := u.h.exec(ctx, progGpasswd, "--members", strings.Join(g.Members, ","), g.Name); err != nil {
		return fmt.Errorf("setting members of %s: %w", g.Name, err)
	}
	return nil
}

func (u *users) DeleteGroup(ctx context.Context, name string) error {
	if err := checkName("group", name); err != nil {
		return err
	}
	if _, err := u.h.exec(ctx, progGroupdel, "--", name); err != nil {
		return fmt.Errorf("deleting group %s: %w", name, err)
	}
	return nil
}
