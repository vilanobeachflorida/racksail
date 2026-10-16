package system

import (
	"errors"
	"fmt"
	"maps"
	"path"
	"slices"
	"strings"
	"unicode/utf8"
)

// Where systemd keeps the drop-ins `systemctl set-property` writes, one per
// property, named 50-<property>.conf.
const (
	ControlDir = "/etc/systemd/system.control"
	// RuntimeControlDir holds those of `set-property --runtime`, which a
	// reboot empties.
	RuntimeControlDir = "/run/systemd/system.control"
)

// UnitSearchPath is where systemd looks for system units and their
// drop-ins, highest priority first (systemd.unit(5), "Unit File Load
// Path"). /lib/systemd/system is the same directory as /usr/lib/systemd/system
// on Debian 13, whose /usr is merged; it is read last only for systems that
// still keep them apart.
var UnitSearchPath = []string{
	ControlDir, RuntimeControlDir, "/run/systemd/transient", "/run/systemd/generator.early",
	UnitDir, "/etc/systemd/system.attached", "/run/systemd/system", "/run/systemd/system.attached",
	"/run/systemd/generator", "/usr/local/lib/systemd/system", "/usr/lib/systemd/system",
	"/lib/systemd/system", "/run/systemd/generator.late",
}

// PersistentUnitSearchPath is UnitSearchPath without the directories under
// /run, which a reboot empties: where a unit's configuration after the next
// boot comes from.
func PersistentUnitSearchPath() []string {
	var out []string
	for _, d := range UnitSearchPath {
		if !strings.HasPrefix(d, "/run/") {
			out = append(out, d)
		}
	}
	return out
}

// UnitTree is what loading a unit's configuration reads of a filesystem.
type UnitTree interface {
	// Lstat describes a path without following a final symbolic link, and
	// is ErrNotFound when there is nothing there.
	Lstat(p string) (FileInfo, error)
	// ReadDir lists a directory, and is ErrNotFound when there is none.
	ReadDir(p string) ([]FileInfo, error)
	// ReadFile reads a regular file.
	ReadFile(p string) ([]byte, error)
}

// maxUnitLinks bounds a chain of symbolic links between unit files.
const maxUnitLinks = 16

// LoadUnitConfig returns the files systemd applies to a unit when it loads
// it from dirs, a search path highest priority first, in the order it
// applies them:
//
//   - the unit's file, from the first directory that has one of its name,
//     or for an instance with none, of its template's. A file that is empty
//     or a link to /dev/null masks the unit, which then has no
//     configuration at all, and a link that leads nowhere is passed over;
//   - its drop-ins: those in <unit>.d in every directory, then those of its
//     template, of each dash prefix of its name (racksail-accounts-.slice.d
//     and racksail-.slice.d for racksail-accounts-blog.slice) and of the
//     other names linked to its file, and last those of the type-wide
//     directory, such as slice.d. A drop-in hides every one of the same
//     name later in that order, and they apply sorted by name, whichever
//     directory they come from.
func LoadUnitConfig(name string, dirs []string, t UnitTree) ([][]byte, error) {
	if !ValidUnitName(name) {
		return nil, fmt.Errorf("unit name %q: %w", name, ErrInvalid)
	}
	frag, err := unitFragment(name, dirs, t)
	if err != nil {
		return nil, err
	}
	if frag.masked {
		return nil, nil
	}
	var out [][]byte
	names := []string{name}
	if frag.path != "" {
		out = append(out, frag.data)
		aliases, err := unitAliases(name, frag, dirs, t)
		if err != nil {
			return nil, err
		}
		names = append(names, aliases...)
	}
	var dropInDirs []string
	for _, n := range names {
		for _, dir := range dirs {
			for _, d := range dropInDirNames(n) {
				dropInDirs = append(dropInDirs, path.Join(dir, d))
			}
		}
	}
	typ := strings.TrimPrefix(path.Ext(name), ".")
	for _, dir := range dirs {
		dropInDirs = append(dropInDirs, path.Join(dir, typ+".d"))
	}
	chosen := map[string]string{}
	read := map[string]bool{}
	for _, dir := range dropInDirs {
		if read[dir] {
			continue
		}
		read[dir] = true
		entries, err := t.ReadDir(dir)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("drop-ins of %s: %w", name, err)
		}
		for _, e := range entries {
			base := path.Base(e.Path)
			if e.IsDir() || !strings.HasSuffix(base, ".conf") {
				continue
			}
			if _, ok := chosen[base]; !ok {
				chosen[base] = e.Path
			}
		}
	}
	for _, base := range slices.Sorted(maps.Keys(chosen)) {
		end, err := followUnitLinks(chosen[base], t)
		if errors.Is(err, ErrNotFound) || end == devNull {
			// A link to /dev/null, or one that leads nowhere, which systemd
			// skips when it reads it, only hides drop-ins of the same name.
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("drop-in %s of %s: %w", chosen[base], name, err)
		}
		data, err := t.ReadFile(end)
		if err != nil {
			return nil, fmt.Errorf("drop-in %s of %s: %w", chosen[base], name, err)
		}
		out = append(out, data)
	}
	return out, nil
}

const devNull = "/dev/null"

// fragment is the file a unit is loaded from.
type fragment struct {
	// path is where the file ends up once links are followed; empty when
	// the unit has none.
	path string
	data []byte
	// fromTemplate says the file is the template's, for an instance with
	// no file of its own.
	fromTemplate bool
	masked       bool
}

// unitFragment finds the file a unit is loaded from: the first in dirs
// named after it, or for an instance with none, after its template.
func unitFragment(name string, dirs []string, t UnitTree) (fragment, error) {
	candidates := []string{name}
	if tmpl, ok := unitTemplate(name); ok {
		candidates = append(candidates, tmpl)
	}
	for i, n := range candidates {
		for _, dir := range dirs {
			p := path.Join(dir, n)
			if _, err := t.Lstat(p); errors.Is(err, ErrNotFound) {
				continue
			} else if err != nil {
				return fragment{}, fmt.Errorf("unit file %s: %w", p, err)
			}
			end, err := followUnitLinks(p, t)
			if errors.Is(err, ErrNotFound) {
				continue
			}
			if err != nil {
				return fragment{}, fmt.Errorf("unit file %s: %w", p, err)
			}
			if end == devNull {
				return fragment{masked: true}, nil
			}
			data, err := t.ReadFile(end)
			if err != nil {
				return fragment{}, fmt.Errorf("unit file %s: %w", p, err)
			}
			if len(data) == 0 {
				return fragment{masked: true}, nil
			}
			return fragment{path: end, data: data, fromTemplate: i > 0}, nil
		}
	}
	return fragment{}, nil
}

// followUnitLinks returns where a chain of symbolic links from p ends: a
// file, or /dev/null for a link that masks what it stands for. A link that
// leads nowhere is ErrNotFound.
func followUnitLinks(p string, t UnitTree) (string, error) {
	for range maxUnitLinks {
		info, err := t.Lstat(p)
		if err != nil {
			return "", err
		}
		if !info.IsSymlink() {
			if info.IsDir() {
				return "", fmt.Errorf("%s is a directory: %w", p, ErrInvalid)
			}
			return p, nil
		}
		target := info.Target
		if !path.IsAbs(target) {
			target = path.Join(path.Dir(p), target)
		}
		if p = path.Clean(target); p == devNull {
			return p, nil
		}
	}
	return "", fmt.Errorf("%s: too many levels of symbolic links: %w", p, ErrInvalid)
}

// unitAliases returns the unit's other names: those linked to its file in
// the top level of the search path's directories, as an Alias= in [Install]
// makes them, and, when name is itself such a link, the name of the file.
func unitAliases(name string, frag fragment, dirs []string, t UnitTree) ([]string, error) {
	ext := path.Ext(name)
	found := map[string]bool{}
	if base := path.Base(frag.path); !frag.fromTemplate && base != name && path.Ext(base) == ext && ValidUnitName(base) {
		found[base] = true
	}
	for _, dir := range dirs {
		entries, err := t.ReadDir(dir)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("unit directory %s: %w", dir, err)
		}
		for _, e := range entries {
			base := path.Base(e.Path)
			if !e.IsSymlink() || base == name || found[base] || path.Ext(base) != ext || !ValidUnitName(base) {
				continue
			}
			// Links that lead nowhere are passed over, as systemd passes them over.
			if end, err := followUnitLinks(e.Path, t); err == nil && end == frag.path {
				found[base] = true
			}
		}
	}
	return slices.Sorted(maps.Keys(found)), nil
}

// unitTemplate returns the template of an instance, such as
// racksail-addon@.service for racksail-addon@site.service.
func unitTemplate(name string) (string, bool) {
	ext := path.Ext(name)
	prefix, instance, ok := strings.Cut(strings.TrimSuffix(name, ext), "@")
	if !ok || instance == "" {
		return "", false
	}
	return prefix + "@" + ext, true
}

// dropInDirNames are the drop-in directories systemd reads for a unit in
// each directory of its search path, most specific first: its own, its
// template's and those of each dash prefix of its name, each with their
// own in turn. A name's prefix runs to its last dash, a trailing dash
// skipped once, so racksail-accounts-.slice is a prefix of
// racksail-accounts-blog.slice and racksail-.slice of both.
func dropInDirNames(name string) []string {
	var out []string
	seen := map[string]bool{}
	var walk func(n string)
	walk = func(n string) {
		if seen[n] {
			return
		}
		seen[n] = true
		out = append(out, n+".d")
		ext := path.Ext(n)
		prefix, instance, hasAt := strings.Cut(strings.TrimSuffix(n, ext), "@")
		isInstance := hasAt && instance != ""
		if isInstance {
			walk(prefix + "@" + ext)
		}
		p, chopped := prefix, false
		for {
			i := strings.LastIndexByte(p, '-')
			if i <= 0 {
				return
			}
			if i+1 < len(p) || chopped {
				p = p[:i+1]
				break
			}
			p, chopped = p[:i], true
		}
		if isInstance {
			walk(p + "@" + instance + ext)
		} else {
			walk(p + ext)
		}
	}
	walk(name)
	return out
}

// resourceSections are where each unit type keeps its resource-control
// settings.
var resourceSections = map[string]string{
	"service": "Service", "socket": "Socket", "slice": "Slice", "mount": "Mount", "swap": "Swap", "scope": "Scope",
}

// ResourceControlSection returns the section a unit's resource-control
// settings go in, such as Slice for racksail-accounts-blog.slice; false for
// a unit type that has none, such as a timer.
func ResourceControlSection(name string) (string, bool) {
	s, ok := resourceSections[strings.TrimPrefix(path.Ext(name), ".")]
	return s, ok
}

// UnitResourceLimits reads the resource-control limits a unit's
// configuration sets, from its files in the order systemd applies them
// (LoadUnitConfig): the settings in its type's section, such as [Slice],
// the last assignment of each winning and an empty one resetting it. Values
// are reduced with CanonicalLimit, and those it can't reduce with certainty
// are left out.
func UnitResourceLimits(name string, files [][]byte) (map[string]string, error) {
	section, ok := ResourceControlSection(name)
	raw := map[string]string{}
	for _, data := range files {
		sections, err := unitSettings(data)
		if err != nil {
			return nil, fmt.Errorf("configuration of %s: %w", name, err)
		}
		for _, s := range sections {
			if !ok || s.Name != section {
				continue
			}
			for _, e := range s.Entries {
				switch {
				case !slices.Contains(ResourceControlProperties, e.Key):
				case e.Value == "":
					delete(raw, e.Key)
				default:
					raw[e.Key] = e.Value
				}
			}
		}
	}
	out := map[string]string{}
	for k, v := range raw {
		if canon, ok := CanonicalLimit(k, v); ok {
			out[k] = canon
		}
	}
	return out, nil
}

// unitSettings reads the settings of a unit file or drop-in as systemd
// loads them, which is more forgiving than ParseUnitFile: lines end at a
// line feed, a carriage return or a NUL; a leading byte order mark is
// skipped; comments, continued lines and trimming work as ParseUnitFile
// says; and the lines systemd warns about and ignores, those without an
// equals sign or a key and those before the first section, are skipped.
// What makes systemd refuse the whole file, a line that isn't valid UTF-8
// or a malformed section header, is an error here too.
func unitSettings(data []byte) ([]UnitSection, error) {
	text := strings.TrimPrefix(string(data), "\xef\xbb\xbf")
	text = strings.NewReplacer("\r\n", "\n", "\r", "\n", "\x00", "\n").Replace(text)
	var sections []UnitSection
	var continued string
	inContinuation := false
	line := func(l string) error {
		l = strings.Trim(l, unitWhitespace)
		switch {
		case l == "":
			return nil
		case !utf8.ValidString(l):
			return fmt.Errorf("a line isn't valid UTF-8: %w", ErrInvalid)
		case l[0] == '[':
			if !strings.HasSuffix(l, "]") || !safeSectionName(l[1:len(l)-1]) {
				return fmt.Errorf("section header %q: %w", l, ErrInvalid)
			}
			sections = append(sections, UnitSection{Name: l[1 : len(l)-1]})
			return nil
		case len(sections) == 0:
			return nil
		}
		key, value, ok := strings.Cut(l, "=")
		if key = strings.Trim(key, unitWhitespace); ok && key != "" {
			s := &sections[len(sections)-1]
			s.Entries = append(s.Entries, UnitEntry{Key: key, Value: strings.Trim(value, unitWhitespace)})
		}
		return nil
	}
	for _, raw := range strings.Split(text, "\n") {
		if first := strings.TrimLeft(raw, unitWhitespace); first != "" && (first[0] == '#' || first[0] == ';') {
			continue
		}
		l := raw
		if inContinuation {
			l = continued + raw
		}
		escaped := false
		for i := 0; i < len(l); i++ {
			if escaped {
				escaped = false
			} else if l[i] == '\\' {
				escaped = true
			}
		}
		if escaped {
			continued, inContinuation = l[:len(l)-1]+" ", true
			continue
		}
		inContinuation = false
		if err := line(l); err != nil {
			return nil, err
		}
	}
	if inContinuation {
		if err := line(continued); err != nil {
			return nil, err
		}
	}
	return sections, nil
}

// safeSectionName reports whether systemd takes a section header's name:
// one without control characters, quotes or backslashes.
func safeSectionName(n string) bool {
	for _, r := range n {
		if r < ' ' || r == 0x7f || r == '"' || r == '\'' || r == '\\' {
			return false
		}
	}
	return true
}
