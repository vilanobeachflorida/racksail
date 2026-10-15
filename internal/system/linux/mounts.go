package linux

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/vilanobeachflorida/racksail/internal/system"
)

type mountEntry struct {
	// Root is the path inside the filesystem that is mounted, such as /@srv
	// for a Btrfs subvolume.
	Root    string
	Target  string
	FSType  string
	Source  string
	Options []string
}

// parseMountinfo reads /proc/self/mountinfo.
func parseMountinfo(data []byte) []mountEntry {
	var out []mountEntry
	eachLine(data, func(line string) {
		f := strings.Fields(line)
		sep := -1
		for i := 6; i < len(f); i++ {
			if f[i] == "-" {
				sep = i
				break
			}
		}
		if len(f) < 10 || sep < 0 || sep+2 >= len(f) {
			return
		}
		out = append(out, mountEntry{
			Root:    unescapeMount(f[3]),
			Target:  unescapeMount(f[4]),
			Options: strings.Split(f[5], ","),
			FSType:  f[sep+1],
			Source:  unescapeMount(f[sep+2]),
		})
	})
	return out
}

// unescapeMount decodes the octal escapes the kernel uses for spaces, tabs,
// newlines and backslashes in mount paths.
func unescapeMount(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			if n, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(n))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

type mounts struct{ h *host }

func (m *mounts) List(ctx context.Context) ([]system.Mount, error) {
	data, err := os.ReadFile(m.h.path("/proc/self/mountinfo"))
	if err != nil {
		return nil, fmt.Errorf("reading mountinfo: %w", err)
	}
	entries := parseMountinfo(data)
	out := make([]system.Mount, len(entries))
	for i, e := range entries {
		out[i] = system.Mount{Source: e.Source, Target: e.Target, FSType: e.FSType, Options: e.Options}
	}
	return out, nil
}

var (
	mountOptionPattern = regexp.MustCompile(`^[A-Za-z0-9=._:/@+-]+$`)
	mountSourcePattern = regexp.MustCompile(`^[A-Za-z0-9/][A-Za-z0-9=._:/@+-]*$`)
	fsTypePattern      = regexp.MustCompile(`^[a-z0-9.]+$`)
)

func (m *mounts) Mount(ctx context.Context, mt system.Mount) error {
	if err := checkPath(mt.Target); err != nil {
		return err
	}
	if !mountSourcePattern.MatchString(mt.Source) || !fsTypePattern.MatchString(mt.FSType) {
		return fmt.Errorf("mount %s of %q (%s): %w", mt.Target, mt.Source, mt.FSType, system.ErrInvalid)
	}
	args := []string{"-t", mt.FSType}
	if len(mt.Options) > 0 {
		for _, o := range mt.Options {
			if !mountOptionPattern.MatchString(o) {
				return fmt.Errorf("mount option %q: %w", o, system.ErrInvalid)
			}
		}
		args = append(args, "-o", strings.Join(mt.Options, ","))
	}
	args = append(args, "--", mt.Source, mt.Target)
	_, err := m.h.exec(ctx, progMount, args...)
	return wrap("mounting "+mt.Target, err)
}

func (m *mounts) Unmount(ctx context.Context, target string) error {
	if err := checkPath(target); err != nil {
		return err
	}
	if target == "/" {
		return fmt.Errorf("refusing to unmount /: %w", system.ErrInvalid)
	}
	_, err := m.h.exec(ctx, progUmount, "--", target)
	return wrap("unmounting "+target, err)
}
