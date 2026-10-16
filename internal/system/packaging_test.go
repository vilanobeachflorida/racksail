package system

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// packaging/systemd keeps the platform units as they are installed with the
// binary at /usr/bin/racksail, for distribution packages. They must not
// drift from what the installer writes.
func TestPackagedUnitsMatch(t *testing.T) {
	units, err := PlatformUnits("/usr/bin/racksail")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join("..", "..", "packaging", "systemd")
	if *update {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		for name, content := range units {
			if err := os.WriteFile(filepath.Join(dir, name), content, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("%v (run go test -update to create them)", err)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".md" {
			continue
		}
		if _, ok := units[e.Name()]; !ok {
			t.Errorf("packaging/systemd/%s isn't a platform unit", e.Name())
		}
	}
	for name, content := range units {
		got, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || !bytes.Equal(got, content) {
			t.Errorf("packaging/systemd/%s differs from PlatformUnits (run go test -update): %v", name, err)
		}
	}
}
