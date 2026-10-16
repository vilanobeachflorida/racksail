package architecture

import (
	"os"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

// decision returns the row of decisions.md for a decision, such as R4.
func decision(t *testing.T, id string) string {
	t.Helper()
	data, err := os.ReadFile("decisions.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "| "+id+" |") {
			return line
		}
	}
	t.Fatalf("decisions.md has no decision %s", id)
	return ""
}

// TestLicenseExceptionsAreDecisions keeps the license check from letting
// through anything decision R4 doesn't name: every dependency
// packaging/licenses/exceptions.yaml allows outside the allowlist is listed
// in R4 with its reason, as R4 says they all are.
func TestLicenseExceptionsAreDecisions(t *testing.T) {
	data, err := os.ReadFile("../../packaging/licenses/exceptions.yaml")
	if err != nil {
		t.Fatal(err)
	}
	type entry struct {
		Module  string `yaml:"module"`
		Package string `yaml:"package"`
		License string `yaml:"license"`
		Allow   bool   `yaml:"allow"`
	}
	var ex struct {
		Go  []entry `yaml:"go"`
		Npm []entry `yaml:"npm"`
	}
	if err := yaml.Unmarshal(data, &ex); err != nil {
		t.Fatal(err)
	}
	r4 := decision(t, "R4")
	allowed := 0
	for _, e := range append(ex.Go, ex.Npm...) {
		if !e.Allow {
			continue
		}
		allowed++
		name := e.Module + e.Package
		if !strings.Contains(r4, "`"+name+"`") {
			t.Errorf("the license check allows %s under %s, which decision R4 doesn't list among its exceptions", name, e.License)
		}
		if !strings.Contains(r4, e.License) {
			t.Errorf("decision R4 doesn't say %s is %s", name, e.License)
		}
	}
	if allowed == 0 && strings.Contains(r4, "CC0") {
		t.Error("decision R4 lists exceptions the license check no longer needs")
	}
}
