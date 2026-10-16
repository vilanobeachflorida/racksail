package modules_test

import (
	"slices"
	"testing"

	"github.com/vilanobeachflorida/racksail/internal/modules"
	_ "github.com/vilanobeachflorida/racksail/modules/all"
)

// Every capability a bundled module's manifest needs is provided by
// another bundled module, one it can count on being on with it.
func TestEveryNeededCapabilityIsProvided(t *testing.T) {
	reg := modules.Default()
	var provided []string
	for _, m := range reg.All() {
		provided = append(provided, m.Manifest().Capabilities.Provides...)
	}
	needed := 0
	for _, m := range reg.All() {
		man := m.Manifest()
		for _, c := range man.Capabilities.Needs {
			needed++
			if !slices.Contains(provided, c) {
				t.Errorf("%s needs %s, which no module provides", man.ID, c)
			}
		}
	}
	if needed == 0 {
		t.Fatal("no module needs a capability, so the test checks nothing")
	}
	if err := reg.Err(); err != nil {
		t.Error(err)
	}
}
