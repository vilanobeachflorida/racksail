package main

import (
	"cmp"
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/system"
)

// IdleMemory is what each of Racksail's own services holds, in bytes.
type IdleMemory map[string]int64

// Total is what the services hold together.
func (m IdleMemory) Total() int64 {
	var total int64
	for _, n := range m {
		total += n
	}
	return total
}

func mib(n int64) string {
	return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
}

func (m IdleMemory) String() string {
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	slices.Sort(names)
	parts := make([]string, len(names))
	for i, name := range names {
		parts[i] = name + " " + mib(m[name])
	}
	return mib(m.Total()) + " (" + strings.Join(parts, ", ") + ")"
}

// memoryScript prints every service in Racksail's platform slice with its
// cgroup's memory.current and the inactive file cache in it. The budget
// leaves that cache out, as container runtimes do for a working set: the
// kernel drops it first, and a service doesn't hold it.
func memoryScript(cgroups string) string {
	return `for d in ` + cgroups + `/` + system.PlatformSlice + `/*.service; do
	[ -d "$d" ] || continue
	printf '%s %s %s\n' "${d##*/}" "$(cat "$d/memory.current")" "$(awk '$1 == "inactive_file" { print $2 }' "$d/memory.stat")"
done`
}

// parseMemory reads what memoryScript prints.
func parseMemory(out string) (IdleMemory, error) {
	m := IdleMemory{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		if len(f) != 3 {
			return nil, fmt.Errorf("reading the services' memory: unexpected line %q", line)
		}
		current, err := strconv.ParseInt(f[1], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("reading %s's memory.current: %w", f[0], err)
		}
		inactive, err := strconv.ParseInt(f[2], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("reading %s's inactive file cache: %w", f[0], err)
		}
		m[f[0]] = max(current-inactive, 0)
	}
	if len(m) == 0 {
		return nil, fmt.Errorf("no services run in %s, so there is no memory to measure", system.PlatformSlice)
	}
	return m, nil
}

// median returns the sample whose total is the median, so one busy moment
// doesn't decide the measurement.
func median(samples []IdleMemory) IdleMemory {
	sorted := slices.Clone(samples)
	slices.SortFunc(sorted, func(a, b IdleMemory) int { return cmp.Compare(a.Total(), b.Total()) })
	return sorted[len(sorted)/2]
}

// Idle memory is measured this many times, this far apart.
const (
	memorySamples = 5
	memoryEvery   = 10 * time.Second
)

// checkIdleMemory lets the server settle, measures what Racksail's own
// services hold and compares it with the budget in MiB. Every module is off
// on a fresh install, which decision R5 says idles under 100 MB.
func checkIdleMemory(ctx context.Context, s SSH, cgroups string, settle time.Duration, samples int, every time.Duration, budget int, logs string) error {
	log.Printf("letting the server settle for %s before measuring its memory", settle)
	if err := sleep(ctx, settle); err != nil {
		return err
	}
	var taken []IdleMemory
	var report strings.Builder
	for i := range samples {
		if i > 0 {
			if err := sleep(ctx, every); err != nil {
				return err
			}
		}
		out, err := s.Run(ctx, memoryScript(cgroups))
		if err != nil {
			return fmt.Errorf("measuring the services' memory: %w\n%s", err, strings.TrimSpace(string(out)))
		}
		m, err := parseMemory(string(out))
		if err != nil {
			return err
		}
		taken = append(taken, m)
		fmt.Fprintf(&report, "sample %d: %s\n", i+1, m)
	}
	idle := median(taken)
	fmt.Fprintf(&report, "median: %s\nbudget: %d MiB\n", idle, budget)
	if err := os.WriteFile(filepath.Join(logs, "memory.txt"), []byte(report.String()), 0o644); err != nil {
		return err
	}
	log.Printf("Racksail's services idle at %s; the budget is %d MiB", idle, budget)
	if idle.Total() >= int64(budget)<<20 {
		return fmt.Errorf("idle, Racksail's services hold %s, over the %d MiB budget (decision R5 in docs/architecture/decisions.md)", idle, budget)
	}
	return nil
}

func sleep(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}
