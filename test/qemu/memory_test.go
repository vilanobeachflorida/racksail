package main

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestParseMemory(t *testing.T) {
	m, err := parseMemory("racksail-agent.service 31457280 10485760\nracksail-panel.service 52428800 0\n\n")
	if err != nil {
		t.Fatal(err)
	}
	if m["racksail-agent.service"] != 20<<20 || m.Total() != 70<<20 {
		t.Errorf("memory = %v", m)
	}
	if got := m.String(); got != "70.0 MiB (racksail-agent.service 20.0 MiB, racksail-panel.service 50.0 MiB)" {
		t.Errorf("String = %q", got)
	}
	for _, bad := range []string{"", "racksail-panel.service 1", "racksail-panel.service many 0", "racksail-panel.service 1 none"} {
		if _, err := parseMemory(bad); err == nil {
			t.Errorf("parseMemory(%q) should fail", bad)
		}
	}
	if m, _ := parseMemory("racksail-panel.service 10 20"); m["racksail-panel.service"] != 0 {
		t.Error("more inactive cache than memory, as the counters race, counts as nothing")
	}
}

func TestMedian(t *testing.T) {
	samples := []IdleMemory{{"a": 90}, {"a": 10}, {"a": 50, "b": 5}, {"a": 40}, {"a": 1000}}
	if got := median(samples); got.Total() != 55 {
		t.Errorf("median = %v", got)
	}
}

// fakeCgroups lays out a cgroup tree like the one systemd makes for
// Racksail's platform slice, with each service's memory in MiB and its
// inactive file cache.
func fakeCgroups(t *testing.T, services map[string][2]int) string {
	t.Helper()
	root := t.TempDir()
	slice := filepath.Join(root, "racksail.slice")
	// The accounts' slice sits inside the platform slice and isn't counted.
	if err := os.MkdirAll(filepath.Join(slice, "racksail-accounts.slice"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, mem := range services {
		dir := filepath.Join(slice, name)
		current := strconv.Itoa(mem[0]<<20) + "\n"
		stat := "anon 1234\nfile 99\nactive_file 5\ninactive_file " + strconv.Itoa(mem[1]<<20) + "\nslab 7\n"
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "memory.current"), []byte(current), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "memory.stat"), []byte(stat), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestCheckIdleMemory(t *testing.T) {
	installStubs(t, map[string]string{"ssh": sshStub})
	server := SSH{Key: "unused", Port: 22, User: "admin"}
	ctx := context.Background()
	cgroups := fakeCgroups(t, map[string][2]int{
		"racksail-agent.service": {30, 10},
		"racksail-panel.service": {50, 0},
	})

	logs := t.TempDir()
	if err := checkIdleMemory(ctx, server, cgroups, 0, 3, time.Millisecond, 100, logs); err != nil {
		t.Fatal(err)
	}
	report, _ := os.ReadFile(filepath.Join(logs, "memory.txt"))
	if !strings.Contains(string(report), "sample 3: 70.0 MiB") || !strings.Contains(string(report), "median: 70.0 MiB (racksail-agent.service 20.0 MiB, racksail-panel.service 50.0 MiB)") {
		t.Errorf("memory.txt = %s", report)
	}

	err := checkIdleMemory(ctx, server, cgroups, 0, 1, time.Millisecond, 64, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "70.0 MiB") || !strings.Contains(err.Error(), "over the 64 MiB budget") {
		t.Errorf("services over the budget must fail with what they hold: %v", err)
	}

	empty := fakeCgroups(t, nil)
	if err := checkIdleMemory(ctx, server, empty, 0, 1, time.Millisecond, 100, t.TempDir()); err == nil || !strings.Contains(err.Error(), "no services run") {
		t.Errorf("a slice without services must fail: %v", err)
	}

	waitCtx, cancel := context.WithTimeout(ctx, 10*time.Millisecond)
	defer cancel()
	if err := checkIdleMemory(waitCtx, server, cgroups, time.Hour, 1, time.Millisecond, 100, t.TempDir()); err == nil {
		t.Error("settling must stop when the run ends")
	}
}
