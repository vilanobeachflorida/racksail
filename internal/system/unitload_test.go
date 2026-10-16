package system

import (
	"errors"
	"io/fs"
	"path"
	"slices"
	"strings"
	"testing"
)

// memTree is a filesystem for LoadUnitConfig: files by path, with links
// written as "-> target".
type memTree map[string]string

func (t memTree) Lstat(p string) (FileInfo, error) {
	if v, ok := t[p]; ok {
		if target, ok := strings.CutPrefix(v, "-> "); ok {
			return FileInfo{Path: p, Mode: fs.ModeSymlink | 0o777, Target: target}, nil
		}
		return FileInfo{Path: p, Mode: 0o644, Size: int64(len(v))}, nil
	}
	for q := range t {
		if strings.HasPrefix(q, p+"/") {
			return FileInfo{Path: p, Mode: fs.ModeDir | 0o755}, nil
		}
	}
	return FileInfo{}, ErrNotFound
}

func (t memTree) ReadDir(p string) ([]FileInfo, error) {
	seen := map[string]bool{}
	var out []FileInfo
	for q := range t {
		rest, ok := strings.CutPrefix(q, p+"/")
		if !ok {
			continue
		}
		name, _, _ := strings.Cut(rest, "/")
		if seen[name] {
			continue
		}
		seen[name] = true
		info, err := t.Lstat(path.Join(p, name))
		if err != nil {
			return nil, err
		}
		out = append(out, info)
	}
	if len(out) == 0 {
		return nil, ErrNotFound
	}
	return out, nil
}

func (t memTree) ReadFile(p string) ([]byte, error) {
	v, ok := t[p]
	if !ok || strings.HasPrefix(v, "-> ") {
		return nil, ErrNotFound
	}
	return []byte(v), nil
}

func limitsOf(t *testing.T, name string, dirs []string, tree memTree) map[string]string {
	t.Helper()
	files, err := LoadUnitConfig(name, dirs, tree)
	if err != nil {
		t.Fatalf("LoadUnitConfig(%s): %v", name, err)
	}
	limits, err := UnitResourceLimits(name, files)
	if err != nil {
		t.Fatalf("UnitResourceLimits(%s): %v", name, err)
	}
	return limits
}

// Only the unit file of the highest priority counts: one in /etc replaces
// the package's in /usr/lib rather than adding to it.
func TestLoadUnitConfigTakesOneUnitFile(t *testing.T) {
	tree := memTree{
		"/usr/lib/systemd/system/mariadb.service": "[Service]\nMemoryMax=1G\nTasksMax=100\n",
		"/etc/systemd/system/mariadb.service":     "[Service]\nMemoryMax=2G\n",
	}
	got := limitsOf(t, "mariadb.service", PersistentUnitSearchPath(), tree)
	if got["MemoryMax"] != "2147483648" || got["TasksMax"] != "" {
		t.Errorf("limits = %v", got)
	}
}

// Drop-ins apply in the order of their names whichever directory they are
// in, so racksail-limits.conf in /etc/systemd/system comes after the
// 50-MemoryMax.conf set-property wrote in system.control, and wins.
func TestLoadUnitConfigAppliesDropInsByName(t *testing.T) {
	const slice = "racksail-accounts-blog.slice"
	tree := memTree{
		"/etc/systemd/system/" + slice:                                  "[Slice]\nMemoryMax=512M\n",
		"/etc/systemd/system/" + slice + ".d/racksail-limits.conf":      "[Slice]\nMemoryMax=1G\n",
		"/etc/systemd/system.control/" + slice + ".d/50-MemoryMax.conf": "[Slice]\nMemoryMax=2G\n",
	}
	if got := limitsOf(t, slice, PersistentUnitSearchPath(), tree); got["MemoryMax"] != "1073741824" {
		t.Errorf("MemoryMax = %q, want racksail-limits.conf's 1G", got["MemoryMax"])
	}
}

// A drop-in hides those of the same name further down the search path: in a
// directory of lower priority, or in a less specific directory, and a link
// to /dev/null hides one without adding anything.
func TestLoadUnitConfigMasksDropInsOfTheSameName(t *testing.T) {
	tree := memTree{
		"/usr/lib/systemd/system/knot.service":                  "[Service]\nExecStart=/usr/sbin/knotd\n",
		"/usr/lib/systemd/system/knot.service.d/10-limits.conf": "[Service]\nMemoryMax=1G\n",
		"/etc/systemd/system/knot.service.d/10-limits.conf":     "[Service]\nTasksMax=50\n",
		"/usr/lib/systemd/system/knot.service.d/20-tasks.conf":  "[Service]\nTasksMax=70\n",
		"/etc/systemd/system/knot.service.d/20-tasks.conf":      "-> /dev/null",
		// A link that leads nowhere is skipped, and hides its namesakes too.
		"/etc/systemd/system/knot.service.d/30-cpu.conf":     "-> /etc/racksail/gone.conf",
		"/usr/lib/systemd/system/knot.service.d/30-cpu.conf": "[Service]\nCPUWeight=900\n",
		// The type-wide directory comes last, so the unit's own 10-limits.conf
		// hides this one even from a directory of lower priority.
		"/etc/systemd/system/service.d/10-limits.conf": "[Service]\nMemoryHigh=3G\n",
	}
	got := limitsOf(t, "knot.service", PersistentUnitSearchPath(), tree)
	want := map[string]string{"TasksMax": "50"}
	if !mapsEqual(got, want) {
		t.Errorf("limits = %v, want %v", got, want)
	}
}

func mapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// Drop-ins for a dash prefix of the name and the type-wide directory apply
// too, more specific ones first when names clash.
func TestLoadUnitConfigReadsPrefixAndTypeDropIns(t *testing.T) {
	const slice = "racksail-accounts-blog.slice"
	tree := memTree{
		"/etc/systemd/system/racksail-accounts-.slice.d/30-tasks.conf": "[Slice]\nTasksMax=300\n",
		"/usr/lib/systemd/system/racksail-.slice.d/40-io.conf":         "[Slice]\nIOWeight=50\n",
		"/etc/systemd/system/slice.d/50-cpu.conf":                      "[Slice]\nCPUWeight=20\n",
		"/etc/systemd/system/slice.d/30-tasks.conf":                    "[Slice]\nTasksMax=1\n",
	}
	got := limitsOf(t, slice, PersistentUnitSearchPath(), tree)
	want := map[string]string{"TasksMax": "300", "IOWeight": "50", "CPUWeight": "20"}
	if !mapsEqual(got, want) {
		t.Errorf("limits = %v, want %v", got, want)
	}
	// racksail-accounts.slice itself is no account's: only racksail-.slice.d
	// and slice.d reach it.
	if got := limitsOf(t, AccountsSlice, PersistentUnitSearchPath(), tree); !mapsEqual(got, map[string]string{"IOWeight": "50", "CPUWeight": "20", "TasksMax": "1"}) {
		t.Errorf("racksail-accounts.slice limits = %v", got)
	}
}

func TestDropInDirNames(t *testing.T) {
	for name, want := range map[string][]string{
		"knot.service": {"knot.service.d"},
		"racksail-accounts-blog.slice": {
			"racksail-accounts-blog.slice.d", "racksail-accounts-.slice.d", "racksail-.slice.d",
		},
		"racksail-accounts-.slice": {"racksail-accounts-.slice.d", "racksail-.slice.d"},
		"racksail-addon@site.service": {
			"racksail-addon@site.service.d", "racksail-addon@.service.d", "racksail-.service.d",
			"racksail-@site.service.d", "racksail-@.service.d",
		},
		"-.slice": {"-.slice.d"},
	} {
		if got := dropInDirNames(name); !slices.Equal(got, want) {
			t.Errorf("dropInDirNames(%s) = %v, want %v", name, got, want)
		}
	}
}

// An instance without a file of its own is loaded from its template, whose
// drop-ins apply to it, before its own.
func TestLoadUnitConfigUsesTheTemplate(t *testing.T) {
	tree := memTree{
		"/etc/systemd/system/racksail-site@.service":                      "[Service]\nMemoryMax=1G\nTasksMax=10\n",
		"/etc/systemd/system/racksail-site@.service.d/10-tasks.conf":      "[Service]\nTasksMax=20\n",
		"/etc/systemd/system/racksail-site@blog.service.d/20-memory.conf": "[Service]\nMemoryMax=2G\n",
	}
	got := limitsOf(t, "racksail-site@blog.service", PersistentUnitSearchPath(), tree)
	if !mapsEqual(got, map[string]string{"MemoryMax": "2147483648", "TasksMax": "20"}) {
		t.Errorf("limits = %v", got)
	}
}

// Names linked to the unit's file are the unit's too, so their drop-ins
// apply, whether the unit is asked for by its own name or by an alias.
func TestLoadUnitConfigReadsAliasDropIns(t *testing.T) {
	tree := memTree{
		"/usr/lib/systemd/system/mariadb.service":                  "[Service]\nMemoryMax=1G\n",
		"/etc/systemd/system/mysql.service":                        "-> /usr/lib/systemd/system/mariadb.service",
		"/etc/systemd/system/mysqld.service":                       "-> mysql.service",
		"/etc/systemd/system/mysql.service.d/50-limits.conf":       "[Service]\nTasksMax=40\n",
		"/etc/systemd/system/mysqld.service.d/60-limits.conf":      "[Service]\nMemoryHigh=900M\n",
		"/etc/systemd/system/mariadb.service.d/40-limits.conf":     "[Service]\nCPUWeight=300\n",
		"/etc/systemd/system/broken.service":                       "-> /usr/lib/systemd/system/gone.service",
		"/etc/systemd/system/unrelated.service.d/10-limits.conf":   "[Service]\nTasksMax=1\n",
		"/usr/lib/systemd/system/unrelated.service":                "[Service]\nTasksMax=2\n",
		"/usr/lib/systemd/system/mariadb.service.d/45-memory.conf": "[Service]\nMemoryMax=3G\n",
	}
	want := map[string]string{"MemoryMax": "3221225472", "TasksMax": "40", "MemoryHigh": "943718400", "CPUWeight": "300"}
	for _, name := range []string{"mariadb.service", "mysql.service"} {
		if got := limitsOf(t, name, PersistentUnitSearchPath(), tree); !mapsEqual(got, want) {
			t.Errorf("%s limits = %v, want %v", name, got, want)
		}
	}
}

// A unit whose file is empty or links to /dev/null is masked: nothing of
// its configuration applies, its drop-ins included.
func TestLoadUnitConfigMaskedUnit(t *testing.T) {
	for _, file := range []string{"", "-> /dev/null"} {
		tree := memTree{
			"/usr/lib/systemd/system/memcached.service":                  "[Service]\nMemoryMax=1G\n",
			"/etc/systemd/system/memcached.service":                      file,
			"/etc/systemd/system/memcached.service.d/racksail-tune.conf": "[Service]\nTasksMax=9\n",
		}
		files, err := LoadUnitConfig("memcached.service", PersistentUnitSearchPath(), tree)
		if err != nil || len(files) != 0 {
			t.Errorf("a unit masked with %q = %q, %v", file, files, err)
		}
	}
}

// The persistent search path leaves out /run, which a reboot empties; the
// full one has it, where set-property --runtime writes.
func TestLoadUnitConfigRuntimeDirectories(t *testing.T) {
	const slice = "racksail-accounts-blog.slice"
	tree := memTree{
		"/etc/systemd/system/" + slice:                                  "[Slice]\nMemoryMax=1G\n",
		"/run/systemd/system.control/" + slice + ".d/50-MemoryMax.conf": "[Slice]\nMemoryMax=512M\n",
	}
	if got := limitsOf(t, slice, PersistentUnitSearchPath(), tree); got["MemoryMax"] != "1073741824" {
		t.Errorf("persistent MemoryMax = %q", got["MemoryMax"])
	}
	if got := limitsOf(t, slice, UnitSearchPath, tree); got["MemoryMax"] != "536870912" {
		t.Errorf("runtime MemoryMax = %q", got["MemoryMax"])
	}
	// A persistent set-property of the same limit has the same file name in
	// a directory of higher priority, so it hides the runtime one.
	tree["/etc/systemd/system.control/"+slice+".d/50-MemoryMax.conf"] = "[Slice]\nMemoryMax=2G\n"
	if got := limitsOf(t, slice, UnitSearchPath, tree); got["MemoryMax"] != "2147483648" {
		t.Errorf("MemoryMax with both set-property drop-ins = %q", got["MemoryMax"])
	}
	for _, d := range PersistentUnitSearchPath() {
		if strings.HasPrefix(d, "/run/") {
			t.Errorf("the persistent search path has %s", d)
		}
	}
}

// Settings count only in the unit type's own section, the last one wins and
// an empty one resets the limit; values that can't be compared with
// certainty are left out.
func TestUnitResourceLimits(t *testing.T) {
	files := [][]byte{
		[]byte("[Unit]\nMemoryMax=5G\n[Slice]\nMemoryMax=1G\nTasksMax=10\nCPUQuota=150%\nMemoryHigh=50%\n"),
		[]byte("[Slice]\nTasksMax=\nCPUWeight = 70\n[Service]\nIOWeight=10\n"),
		[]byte("[Slice]\r\nMemoryMax=2G\r\nnot a setting\r\n=nokey\r\n"),
	}
	got, err := UnitResourceLimits("racksail-accounts-blog.slice", files)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"MemoryMax": "2147483648", "CPUQuota": "1500000", "CPUWeight": "70"}
	if !mapsEqual(got, want) {
		t.Errorf("limits = %v, want %v", got, want)
	}
	if got, _ := UnitResourceLimits("racksail-backup.timer", files); len(got) != 0 {
		t.Errorf("a timer has no limits: %v", got)
	}
	// What makes systemd refuse a file leaves the limits unknown.
	for _, bad := range []string{"[Slice\nMemoryMax=1G\n", "[Slice]\nMemoryMax=1G\xff\n"} {
		if _, err := UnitResourceLimits("racksail-accounts-blog.slice", [][]byte{[]byte(bad)}); !errors.Is(err, ErrInvalid) {
			t.Errorf("UnitResourceLimits(%q) = %v", bad, err)
		}
	}
}

// Continued lines and comments are read as systemd reads them.
func TestUnitSettingsContinuations(t *testing.T) {
	sections, err := unitSettings([]byte("\xef\xbb\xbf[Service]\nEnvironment=A=1 \\\n# a comment\n  B=2\nMemoryMax=\\\n1G\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(sections) != 1 || len(sections[0].Entries) != 2 {
		t.Fatalf("sections = %+v", sections)
	}
	// The backslash becomes a blank, and the next line keeps its own.
	if e := sections[0].Entries[0]; e.Key != "Environment" || e.Value != "A=1    B=2" {
		t.Errorf("continued entry = %+v", e)
	}
	if e := sections[0].Entries[1]; e.Key != "MemoryMax" || e.Value != "1G" {
		t.Errorf("entry = %+v", e)
	}
}

func FuzzUnitSettings(f *testing.F) {
	for _, seed := range []string{"[Slice]\nMemoryMax=1G\n", "[Service]\nA=\\\n\\\n", "\r\r[X]\x00k=v", "[\n"} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		sections, err := unitSettings(data)
		if err != nil {
			return
		}
		for _, s := range sections {
			for _, e := range s.Entries {
				if e.Key == "" || strings.ContainsAny(e.Key+e.Value, "\n\r\x00") {
					t.Fatalf("entry %+v of %q", e, data)
				}
			}
		}
	})
}
