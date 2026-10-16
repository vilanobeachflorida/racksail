package docs

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// unitSetting returns a setting of one of the unit files packaging/systemd
// ships, or "" when the unit doesn't set it.
func unitSetting(t *testing.T, unit, key string) string {
	t.Helper()
	value := ""
	for _, line := range strings.Split(read(t, "../packaging/systemd/"+unit), "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(line), "="); ok && k == key {
			value = v
		}
	}
	return value
}

var codeSpan = regexp.MustCompile("`([^`]+)`")

// TestPlatformWeightsAreDocumented holds packaging/README.md's table of
// the platform units' CPU and I/O weights to the units themselves, and the
// units to what the README and the system layer's page say of them: the
// accounts slice sits inside racksail.slice next to the three services, and
// each service outweighs all the accounts together.
func TestPlatformWeightsAreDocumented(t *testing.T) {
	var header []string
	documented := map[string]bool{}
	for _, line := range strings.Split(read(t, "../packaging/README.md"), "\n") {
		if !strings.HasPrefix(line, "|") {
			header = nil
			continue
		}
		cells := strings.Split(strings.Trim(line, "|"), "|")
		for i := range cells {
			cells[i] = strings.Trim(strings.TrimSpace(cells[i]), "`")
		}
		switch {
		case header == nil:
			header = cells
			continue
		case !slices.Contains(header, "CPUWeight") || strings.HasPrefix(cells[0], "---"):
			continue
		}
		for _, unit := range codeSpan.FindAllStringSubmatch(line, -1) {
			if !strings.Contains(unit[1], ".") {
				continue
			}
			documented[unit[1]] = true
			for i, key := range header[1:] {
				if got := unitSetting(t, unit[1], key); got != cells[i+1] {
					t.Errorf("packaging/README.md says %s has %s=%s, but its unit has %q", unit[1], key, cells[i+1], got)
				}
			}
		}
	}
	services := []string{"racksail-agent.service", "racksail-panel.service", "racksail-edge.service"}
	for _, unit := range append(slices.Clone(services), "racksail-accounts.slice") {
		if !documented[unit] {
			t.Errorf("packaging/README.md's table of weights leaves out %s", unit)
		}
	}
	for _, key := range []string{"CPUWeight", "IOWeight"} {
		accounts, err := strconv.Atoi(unitSetting(t, "racksail-accounts.slice", key))
		if err != nil {
			t.Fatalf("racksail-accounts.slice has no %s: %v", key, err)
		}
		for _, svc := range services {
			if slice := unitSetting(t, svc, "Slice"); slice != "racksail.slice" {
				t.Errorf("%s runs in %q, not racksail.slice next to the accounts", svc, slice)
			}
			weight, err := strconv.Atoi(unitSetting(t, svc, key))
			if err != nil || weight <= accounts {
				t.Errorf("%s has %s=%d, which doesn't outweigh all the accounts together (%d), as the docs say it does", svc, key, weight, accounts)
			}
		}
	}
	if got := unitSetting(t, "racksail.slice", "CPUWeight"); got != "" {
		t.Errorf("racksail.slice sets CPUWeight=%s; the docs say it keeps systemd's default, sharing the machine evenly with the system's services", got)
	}
}
