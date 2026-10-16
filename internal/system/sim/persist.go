package sim

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/system"
)

// snapshotVersion changes when the saved format does.
const snapshotVersion = 1

// saveDelay gathers the changes of a burst of operations into one save.
const saveDelay = 250 * time.Millisecond

// Open returns the machine kept in dir, creating a freshly installed one
// from opts the first time. The machine saves itself to dir/machine.json
// shortly after every change, and keeps a copy of its files under dir/root
// so programs on the host, such as the edge in development mode, can read
// what the agent wrote there. Close saves what is left.
func Open(dir string, opts Options) (*Machine, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return nil, err
	}
	file := filepath.Join(abs, "machine.json")
	data, err := os.ReadFile(file)
	var m *Machine
	switch {
	case errors.Is(err, fs.ErrNotExist):
		m = New(opts)
	case err != nil:
		return nil, err
	default:
		if m, err = load(data, opts); err != nil {
			return nil, fmt.Errorf("the simulated server in %s can't be read (remove the directory to start over): %w", abs, err)
		}
	}
	m.store = &store{dir: abs, m: m, mirrored: map[string]mirrorEntry{}}
	// The copy on disk may be stale or missing; start it over.
	if err := os.RemoveAll(m.store.root()); err != nil {
		return nil, err
	}
	if err := m.store.save(); err != nil {
		return nil, err
	}
	return m, nil
}

// Flush saves a machine opened with Open right away, for callers that need
// what an operation wrote to be on disk now.
func (m *Machine) Flush() error {
	if m.store == nil {
		return nil
	}
	return m.store.save()
}

// Close saves a machine opened with Open for the last time.
func (m *Machine) Close() error {
	if m.store == nil {
		return nil
	}
	m.store.stop()
	return m.store.save()
}

// HostPath returns where a machine path is on the host for a machine opened
// with Open, or "" for one that lives in memory.
func (m *Machine) HostPath(p string) string {
	if m.store == nil {
		return ""
	}
	return filepath.Join(m.store.root(), filepath.FromSlash(path.Clean("/"+p)))
}

type store struct {
	dir string
	m   *Machine

	mu      sync.Mutex
	timer   *time.Timer
	stopped bool

	saving   sync.Mutex
	mirrored map[string]mirrorEntry
}

// mirrorEntry is what the copy on disk of a path was last made from.
type mirrorEntry struct {
	mode    fs.FileMode
	size    int
	modTime time.Time
	target  string
}

func (s *store) root() string { return filepath.Join(s.dir, "root") }

// schedule saves the machine soon. The caller holds the machine's lock.
func (s *store) schedule() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.timer != nil || s.stopped {
		return
	}
	s.timer = time.AfterFunc(saveDelay, func() {
		s.mu.Lock()
		stopped := s.stopped
		s.mu.Unlock()
		if stopped {
			return
		}
		if err := s.save(); err != nil {
			fmt.Fprintf(os.Stderr, "saving the simulated server: %v\n", err)
		}
	})
}

func (s *store) stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopped = true
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
}

// mirrorAction is one change to the copy of the files on disk.
type mirrorAction struct {
	path   string
	remove bool
	mode   fs.FileMode
	data   []byte
	target string
}

func (s *store) save() error {
	s.saving.Lock()
	defer s.saving.Unlock()
	// This save covers whatever a pending one would have.
	s.mu.Lock()
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
	s.mu.Unlock()

	m := s.m
	m.mu.Lock()
	data, err := json.Marshal(m.snapshot())
	actions := s.diff()
	m.mu.Unlock()
	if err != nil {
		return err
	}
	tmp := filepath.Join(s.dir, "machine.json.tmp")
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, filepath.Join(s.dir, "machine.json")); err != nil {
		return err
	}
	return s.apply(actions)
}

// diff lists what changed in the files since the last save. The caller
// holds the machine's lock.
func (s *store) diff() []mirrorAction {
	m := s.m
	var actions []mirrorAction
	for p, n := range m.files {
		e := mirrorEntry{mode: n.mode, size: len(n.data), modTime: n.modTime, target: n.target}
		if old, ok := s.mirrored[p]; ok && old == e {
			continue
		}
		s.mirrored[p] = e
		a := mirrorAction{path: p, mode: n.mode, target: n.target}
		if n.mode.IsRegular() {
			a.data = slices.Clone(n.data)
		}
		actions = append(actions, a)
	}
	for p := range s.mirrored {
		if _, ok := m.files[p]; !ok {
			delete(s.mirrored, p)
			actions = append(actions, mirrorAction{path: p, remove: true})
		}
	}
	// Parents before children when creating, children before parents when
	// removing.
	sort.Slice(actions, func(i, j int) bool {
		a, b := actions[i], actions[j]
		if a.remove != b.remove {
			return b.remove
		}
		if a.remove {
			return len(a.path) > len(b.path)
		}
		return a.path < b.path
	})
	return actions
}

// apply changes the copy on disk. The host user owns everything there, so
// modes keep at least the owner's access.
func (s *store) apply(actions []mirrorAction) error {
	root := s.root()
	var errs []error
	for _, a := range actions {
		host := filepath.Join(root, filepath.FromSlash(a.path))
		switch {
		case a.remove:
			if err := os.RemoveAll(host); err != nil {
				errs = append(errs, err)
			}
		case a.mode.IsDir():
			if fi, err := os.Lstat(host); err == nil && !fi.IsDir() {
				os.Remove(host)
			}
			if err := os.MkdirAll(host, 0o755); err != nil {
				errs = append(errs, err)
				continue
			}
			os.Chmod(host, a.mode.Perm()|0o700)
		case a.mode&fs.ModeSymlink != 0:
			target := a.target
			if path.IsAbs(target) {
				target = filepath.Join(root, filepath.FromSlash(target))
			}
			os.Remove(host)
			if err := os.Symlink(target, host); err != nil {
				errs = append(errs, err)
			}
		case a.mode.IsRegular():
			if fi, err := os.Lstat(host); err == nil && fi.IsDir() {
				os.RemoveAll(host)
			}
			if err := os.MkdirAll(filepath.Dir(host), 0o755); err != nil {
				errs = append(errs, err)
				continue
			}
			tmp := host + ".racksail-tmp"
			if err := os.WriteFile(tmp, a.data, a.mode.Perm()|0o600); err != nil {
				errs = append(errs, err)
				continue
			}
			if err := os.Rename(tmp, host); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

// snapshot is everything a machine is, in a form JSON keeps.
type snapshot struct {
	Version  int                             `json:"version"`
	Facts    system.MachineFacts             `json:"facts"`
	Files    map[string]fileJSON             `json:"files"`
	Users    map[string]*system.User         `json:"users"`
	Groups   map[string]*system.Group        `json:"groups"`
	Units    map[string]unitJSON             `json:"units"`
	Enabled  map[string]bool                 `json:"enabled"`
	Builtin  map[string]bool                 `json:"builtin"`
	Reloads  int                             `json:"reloads"`
	Packages map[string]system.Package       `json:"packages"`
	Offers   map[string]string               `json:"offers"`
	Sources  map[string]system.AptSource     `json:"sources"`
	Archives map[string]system.Archive       `json:"archives"`
	Subvols  map[string]*system.Subvolume    `json:"subvolumes"`
	Quotas   map[string]int64                `json:"quotas"`
	NextSub  uint64                          `json:"nextSubvolume"`
	BtrfsFS  map[string]btrfsJSON            `json:"btrfs"`
	Mounts   []system.Mount                  `json:"mounts"`
	Disks    []*system.Disk                  `json:"disks"`
	Ruleset  []byte                          `json:"ruleset"`
	Sets     map[string]map[string]time.Time `json:"sets"`
	Ifaces   []system.Interface              `json:"interfaces"`
	Routes   []system.Route                  `json:"routes"`
	Hostname string                          `json:"hostname"`
	Sysctl   map[string]string               `json:"sysctl"`
	Procs    map[int]*system.Process         `json:"processes"`
	NextPID  int                             `json:"nextPid"`
	Holds    map[string]bool                 `json:"holds"`
	Smart    map[string]*diskHealth          `json:"smart"`
	Journal  []logEntry                      `json:"journal"`
	NextLog  uint64                          `json:"nextLog"`
	Activity map[string]time.Time            `json:"activity"`
	Stats    *statsState                     `json:"stats,omitempty"`
}

type fileJSON struct {
	Mode    fs.FileMode `json:"mode"`
	UID     int         `json:"uid"`
	GID     int         `json:"gid"`
	Data    []byte      `json:"data,omitempty"`
	Target  string      `json:"target,omitempty"`
	ModTime time.Time   `json:"modTime"`
	NoCOW   bool        `json:"noCow,omitempty"`
}

type unitJSON struct {
	Active       string            `json:"active"`
	Sub          string            `json:"sub"`
	PID          int               `json:"pid,omitempty"`
	Since        time.Time         `json:"since,omitzero"`
	Restarts     int               `json:"restarts,omitempty"`
	Props        map[string]string `json:"props,omitempty"`
	RuntimeProps map[string]string `json:"runtimeProps,omitempty"`
}

type btrfsJSON struct {
	Devices  []string             `json:"devices"`
	Profiles system.BtrfsProfiles `json:"profiles"`
	Quotas   bool                 `json:"quotas"`
	Scrub    system.ScrubStatus   `json:"scrub"`
}

// snapshot captures the machine. The caller holds m.mu.
func (m *Machine) snapshot() snapshot {
	s := snapshot{
		Version: snapshotVersion, Facts: m.facts, Files: map[string]fileJSON{}, Users: m.users, Groups: m.groups,
		Units: map[string]unitJSON{}, Enabled: m.enabled, Builtin: m.builtin, Reloads: m.reloads,
		Packages: m.packages, Offers: m.offers, Sources: m.sources, Archives: m.archives, Subvols: m.subvols,
		Quotas: m.quotas, NextSub: m.nextSub, BtrfsFS: map[string]btrfsJSON{}, Mounts: m.mounts, Disks: m.disks,
		Ruleset: m.ruleset, Sets: m.sets, Ifaces: m.ifaces, Routes: m.routes, Hostname: m.hostname, Sysctl: m.sysctl,
		Procs: m.procs, NextPID: m.nextPID, Holds: m.holds, Smart: m.smart, Journal: m.journal, NextLog: m.nextLog,
		Activity: m.activity, Stats: m.stats,
	}
	for p, n := range m.files {
		s.Files[p] = fileJSON{Mode: n.mode, UID: n.uid, GID: n.gid, Data: n.data, Target: n.target, ModTime: n.modTime, NoCOW: n.noCOW}
	}
	for name, rt := range m.units {
		s.Units[name] = unitJSON{Active: rt.active, Sub: rt.sub, PID: rt.pid, Since: rt.since, Restarts: rt.restarts, Props: rt.props, RuntimeProps: rt.runtimeProps}
	}
	for dev, b := range m.btrfsFS {
		s.BtrfsFS[dev] = btrfsJSON{Devices: b.devices, Profiles: b.profiles, Quotas: b.quotas, Scrub: b.scrub}
	}
	return s
}

// load builds a machine from a snapshot, keeping the clock and timing of
// opts.
func load(data []byte, opts Options) (*Machine, error) {
	var s snapshot
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	if s.Version != snapshotVersion {
		return nil, fmt.Errorf("saved by another version (%d, expected %d)", s.Version, snapshotVersion)
	}
	if opts.Clock == nil {
		opts.Clock = time.Now
	}
	orEmpty := func(mp map[string]bool) map[string]bool {
		if mp == nil {
			return map[string]bool{}
		}
		return mp
	}
	m := &Machine{
		clock: opts.Clock, realistic: opts.Realistic, facts: s.Facts,
		files: map[string]*node{}, users: s.Users, groups: s.Groups, units: map[string]*unitRuntime{},
		enabled: orEmpty(s.Enabled), builtin: orEmpty(s.Builtin), reloads: s.Reloads,
		packages: s.Packages, offers: s.Offers, sources: s.Sources, archives: s.Archives,
		subvols: s.Subvols, quotas: s.Quotas, nextSub: s.NextSub, btrfsFS: map[string]*btrfsFS{},
		mounts: s.Mounts, disks: s.Disks, ruleset: s.Ruleset, sets: s.Sets, ifaces: s.Ifaces, routes: s.Routes,
		hostname: s.Hostname, sysctl: s.Sysctl, procs: s.Procs, nextPID: s.NextPID, holds: orEmpty(s.Holds),
		smart: s.Smart, journal: s.Journal, nextLog: s.NextLog, activity: s.Activity, stats: s.Stats,
		programs: map[string]Program{}, failures: map[string][]error{},
	}
	for p, f := range s.Files {
		m.files[p] = &node{mode: f.Mode, uid: f.UID, gid: f.GID, data: f.Data, target: f.Target, modTime: f.ModTime, noCOW: f.NoCOW}
	}
	for name, u := range s.Units {
		m.units[name] = &unitRuntime{active: u.Active, sub: u.Sub, pid: u.PID, since: u.Since, restarts: u.Restarts, props: u.Props, runtimeProps: u.RuntimeProps}
	}
	for dev, b := range s.BtrfsFS {
		m.btrfsFS[dev] = &btrfsFS{devices: b.Devices, profiles: b.Profiles, quotas: b.Quotas, scrub: b.Scrub}
	}
	// Maps JSON left out when they were empty.
	for _, mp := range []*map[string]string{&m.offers, &m.sysctl} {
		if *mp == nil {
			*mp = map[string]string{}
		}
	}
	if m.users == nil || m.groups == nil || m.packages == nil || m.procs == nil || m.subvols == nil {
		return nil, errors.New("the saved machine is incomplete")
	}
	if m.sources == nil {
		m.sources = map[string]system.AptSource{}
	}
	if m.archives == nil {
		m.archives = map[string]system.Archive{}
	}
	if m.quotas == nil {
		m.quotas = map[string]int64{}
	}
	if m.sets == nil {
		m.sets = map[string]map[string]time.Time{}
	}
	if m.smart == nil {
		m.smart = map[string]*diskHealth{}
	}
	if m.activity == nil {
		m.activity = map[string]time.Time{}
	}
	return m, nil
}
